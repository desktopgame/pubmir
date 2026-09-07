package syncengine

import (
	"fmt"
	"io"
	"os"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/pairing"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/tokenize"
)

// RebuildReport summarizes a completed (or cancelled) rebuild.
type RebuildReport struct {
	Branch    string
	Rebuilt   []BuiltCommit
	Cancelled bool
	HasRemote bool
	OldTip    string
	HasOldTip bool
}

// Rebuild regenerates the whole mirror branch from private's history using
// the rules in effect right now, rather than the ones that happened to be
// configured when each commit was first synced. It exists because `sync`
// only ever carries new commits across: a secret that leaked into an older
// mirror commit stays there until the history itself is rebuilt.
//
// private is always the source of truth. Mirror-side work that has not been
// synced back is never discarded silently — the rebuild refuses instead.
func Rebuild(cwd string, opts Options) (*RebuildReport, error) {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}

	pair, err := pairing.Resolve(cwd)
	if err != nil {
		return nil, err
	}
	if pair.Self.Config.Role != config.RolePrivate {
		return nil, fmt.Errorf("pubmir rebuild must be run from the private repository")
	}
	privateSide, mirrorSide := pair.Self, pair.Other

	if err := checkSecretsNotTracked(privateSide); err != nil {
		return nil, err
	}
	if err := checkNoRetiredSecrets(privateSide); err != nil {
		return nil, err
	}
	if err := checkExampleTokensNotSecrets(privateSide); err != nil {
		return nil, err
	}
	branch, err := checkBranchMatch(privateSide, mirrorSide)
	if err != nil {
		return nil, err
	}

	privateClean, err := privateSide.Repo.IsClean()
	if err != nil {
		return nil, err
	}
	if !privateClean {
		return nil, fmt.Errorf("private working tree is not clean; commit or discard changes before rebuilding")
	}
	mirrorClean, err := mirrorSide.Repo.IsClean()
	if err != nil {
		return nil, err
	}
	if !mirrorClean {
		return nil, fmt.Errorf("mirror working tree is not clean; commit or discard changes there before rebuilding (rebuild would otherwise discard them)")
	}

	privateHead, privateHasHead, err := privateSide.Repo.RevParseVerify("HEAD")
	if err != nil {
		return nil, err
	}
	if !privateHasHead {
		return nil, fmt.Errorf("private branch %q has no commits to rebuild from", branch)
	}
	mirrorHead, mirrorHasHead, err := mirrorSide.Repo.RevParseVerify("HEAD")
	if err != nil {
		return nil, err
	}

	st, err := state.Load(privateSide.Root)
	if err != nil {
		return nil, err
	}
	bs := st.Branch(branch)

	// Refuse to discard mirror-side work that never made it back to private.
	// Unlike sync, a broken or missing state file is not itself fatal here —
	// rebuild is one of the ways to recover from that — but then pubmir
	// cannot prove the mirror holds nothing unsynced, so the confirmation
	// prompt says so explicitly instead.
	stateKnown := bs.LastMirrorSha != ""
	if stateKnown && mirrorHasHead {
		unsynced, err := unsyncedShas(mirrorSide.Repo, bs.LastMirrorSha, mirrorHead, mirrorHasHead)
		if err != nil {
			return nil, err
		}
		if len(unsynced) > 0 {
			return nil, fmt.Errorf("Mirror contains %d commit(s) that have not been synchronized back to private.\n\n"+
				"Rebuild would discard these commits.\n\n"+
				"Synchronize or otherwise resolve them before rebuilding.", len(unsynced))
		}
	}

	// The whole of private's history, root first.
	allShas, err := unsyncedShas(privateSide.Repo, "", privateHead, privateHasHead)
	if err != nil {
		return nil, err
	}
	if err := checkNoMergeCommits(privateSide.Repo, allShas); err != nil {
		return nil, fmt.Errorf("cannot rebuild: %w", err)
	}

	if !opts.Yes {
		printRebuildWarning(opts.Stdout, privateSide.Root, mirrorSide.Root, branch, stateKnown, mirrorHasHead)
		if !confirm(opts.Stdin, opts.Stdout) {
			return &RebuildReport{Branch: branch, Cancelled: true}, nil
		}
	}

	current, history, err := secrets.LoadCurrentAndRecord(privateSide.Root)
	if err != nil {
		return nil, err
	}
	mapper := tokenize.NewMapper(current, history.Values)
	excludePatterns := append(append([]string{}, config.ForcedExcludes...), privateSide.Config.Exclude...)

	carryForward, err := carryForwardEntries(mirrorSide.Repo, mirrorHead, mirrorHasHead)
	if err != nil {
		return nil, err
	}

	// A fresh mapping: every commit is rebuilt from the root, so no parent
	// may be resolved through the old, now-obsolete correspondence.
	rebuiltState := &state.BranchState{}

	built, err := buildPrivateToMirror(privateSide.Repo, mirrorSide.Repo, allShas, excludePatterns,
		privateSide.Config.Stub, mapper, rebuiltState, carryForward)
	if err != nil {
		return nil, err
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("private branch %q produced no commits to rebuild", branch)
	}

	if err := gateBuiltCommits(mirrorSide.Repo, built, gatePolicy{
		mapper:          mapper,
		currentSecrets:  current,
		excludePatterns: excludePatterns,
		stubPatterns:    privateSide.Config.Stub,
		reservedTokens:  tokenize.ReservedSet(privateSide.Config.ExampleTokens),
	}); err != nil {
		return nil, err
	}

	for _, bc := range built {
		rebuiltState.Record(bc.SourceSha, bc.TargetSha)
	}

	newTip := built[len(built)-1].TargetSha
	if err := finalizeSync(mirrorSide.Repo, "mirror", "refs/heads/"+branch, mirrorHead, mirrorHasHead, newTip,
		privateSide.Root, mirrorSide.Root, branch, rebuiltState); err != nil {
		return nil, err
	}

	hasRemote, err := mirrorSide.Repo.HasRemote()
	if err != nil {
		return nil, err
	}

	return &RebuildReport{
		Branch:    branch,
		Rebuilt:   built,
		HasRemote: hasRemote,
		OldTip:    mirrorHead,
		HasOldTip: mirrorHasHead,
	}, nil
}

func printRebuildWarning(out io.Writer, privateRoot, mirrorRoot, branch string, stateKnown, mirrorHasHead bool) {
	fmt.Fprintf(out, "WARNING: pubmir will rebuild the entire mirror history.\n\n"+
		"Private:\n  %s\n\nMirror:\n  %s\n\nBranch:\n  %s\n\n"+
		"All mirror commit SHAs on this branch will change.\n\n"+
		"If this mirror has already been pushed to a remote repository,\n"+
		"a force push will be required afterward.\n\n",
		privateRoot, mirrorRoot, branch)
	if !stateKnown && mirrorHasHead {
		fmt.Fprintf(out, "No sync state is recorded for this branch, so pubmir cannot verify that\n"+
			"the mirror holds no unsynchronized work. Any such commits will be discarded.\n\n")
	}
}
