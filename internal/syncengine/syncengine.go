// Package syncengine implements `pubmir sync`: it enforces every safety
// precondition from init.md §11/§13, then replays unsynced commits from one
// side of a private/mirror pair onto the other as freshly-built, fully
// sanitized (or restored) commits.
package syncengine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/leakcheck"
	"github.com/desktopgame/pubmir/internal/pairing"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/tokenize"
)

// BuiltCommit is one commit produced by a sync run.
type BuiltCommit struct {
	SourceSha string
	TargetSha string
	Message   string // first line only
}

// Options controls interactive behavior.
type Options struct {
	Yes    bool // skip the mirror->private confirmation prompt
	Stdin  io.Reader
	Stdout io.Writer
}

// Report summarizes a completed (or no-op) sync run.
type Report struct {
	Direction string // "private->mirror" or "mirror->private"
	Branch    string
	Applied   []BuiltCommit
	NoOp      bool
	Message   string // human-readable summary when NoOp or cancelled
}

// Run executes `pubmir sync` from cwd.
func Run(cwd string, opts Options) (*Report, error) {
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

	var privateSide, mirrorSide *pairing.Side
	if pair.Self.Config.Role == config.RolePrivate {
		privateSide, mirrorSide = pair.Self, pair.Other
	} else {
		privateSide, mirrorSide = pair.Other, pair.Self
	}

	if err := checkSecretsNotTracked(privateSide); err != nil {
		return nil, err
	}

	branch, err := checkBranchMatch(privateSide, mirrorSide)
	if err != nil {
		return nil, err
	}

	pre, err := preflight(privateSide, mirrorSide, branch)
	if err != nil {
		return nil, err
	}

	if pair.Self.Config.Role == config.RolePrivate {
		return runPrivateToMirror(privateSide, mirrorSide, branch, pre)
	}
	return runMirrorToPrivate(privateSide, mirrorSide, branch, pre, opts)
}

// checkSecretsNotTracked enforces init.md §13.2: pubmir must refuse to run
// if private's secret-value file has been committed to git, regardless of
// whether its content would actually be excluded from what sync copies —
// a tracked secrets file is unsafe by itself (e.g. it would be pushed to
// any remote private uses).
func checkSecretsNotTracked(privateSide *pairing.Side) error {
	for _, name := range []string{config.EnvFileName, config.AltSecretsFile} {
		tracked, err := privateSide.Repo.IsTracked(name)
		if err != nil {
			return err
		}
		if tracked {
			return fmt.Errorf("%s is tracked by git in the private repository; remove it from the index (e.g. `git rm --cached %s`) before syncing", name, name)
		}
	}
	return nil
}

func checkBranchMatch(privateSide, mirrorSide *pairing.Side) (string, error) {
	pb, err := privateSide.Repo.CurrentBranch()
	if err != nil {
		return "", fmt.Errorf("private repository: %w", err)
	}
	mb, err := mirrorSide.Repo.CurrentBranch()
	if err != nil {
		return "", fmt.Errorf("mirror repository: %w", err)
	}
	if pb != mb {
		return "", fmt.Errorf("branch mismatch: private is on %q but mirror is on %q; sync only supports matching branch names", pb, mb)
	}
	return pb, nil
}

// preflightResult carries everything the direction-specific runners need
// after preconditions have been verified.
type preflightResult struct {
	branchState         *state.BranchState
	privateHead         string
	privateHasHead      bool
	mirrorHead          string
	mirrorHasHead       bool
	privateUnsyncedShas []string
	mirrorUnsyncedShas  []string
}

// preflight enforces init.md §11 (divergence) and §13.1 points 6-7
// (history-rewrite detection, merge-commit rejection) before any object is
// built.
func preflight(privateSide, mirrorSide *pairing.Side, branch string) (*preflightResult, error) {
	st, err := state.Load(privateSide.Root)
	if err != nil {
		return nil, err
	}
	bs := st.Branch(branch)

	privateHead, privateHasHead, err := privateSide.Repo.RevParseVerify("HEAD")
	if err != nil {
		return nil, err
	}
	mirrorHead, mirrorHasHead, err := mirrorSide.Repo.RevParseVerify("HEAD")
	if err != nil {
		return nil, err
	}

	if err := checkNotRewritten(privateSide.Repo, "private", bs.LastPrivateSha, privateHead, privateHasHead); err != nil {
		return nil, err
	}
	if err := checkNotRewritten(mirrorSide.Repo, "mirror", bs.LastMirrorSha, mirrorHead, mirrorHasHead); err != nil {
		return nil, err
	}

	privateShas, err := unsyncedShas(privateSide.Repo, bs.LastPrivateSha, privateHead, privateHasHead)
	if err != nil {
		return nil, err
	}
	mirrorShas, err := unsyncedShas(mirrorSide.Repo, bs.LastMirrorSha, mirrorHead, mirrorHasHead)
	if err != nil {
		return nil, err
	}

	// On the very first sync for this branch, both sides typically already
	// carry one independent commit each (pubmir's own `init` scaffolding
	// commit). That is not divergence — there is no prior sync point for
	// them to have diverged from — so the mutual-unsynced-commits guard
	// only applies once a sync has actually happened at least once.
	isBootstrap := bs.LastPrivateSha == "" && bs.LastMirrorSha == ""
	if !isBootstrap && len(privateShas) > 0 && len(mirrorShas) > 0 {
		return nil, fmt.Errorf(
			"Both repositories contain unsynchronized changes.\n\n"+
				"  private: +%d commit(s)\n"+
				"  mirror:  +%d commit(s)\n\n"+
				"Automatic bidirectional merge is intentionally disabled.\n"+
				"Run `pubmir status` and resolve the divergence before syncing.",
			len(privateShas), len(mirrorShas))
	}

	if err := checkNoMergeCommits(privateSide.Repo, privateShas); err != nil {
		return nil, err
	}
	if err := checkNoMergeCommits(mirrorSide.Repo, mirrorShas); err != nil {
		return nil, err
	}

	return &preflightResult{
		branchState:         bs,
		privateHead:         privateHead,
		privateHasHead:      privateHasHead,
		mirrorHead:          mirrorHead,
		mirrorHasHead:       mirrorHasHead,
		privateUnsyncedShas: privateShas,
		mirrorUnsyncedShas:  mirrorShas,
	}, nil
}

func checkNotRewritten(repo *gitrepo.Repo, label, lastSha, head string, hasHead bool) error {
	if lastSha == "" {
		// Nothing has ever been synced on this branch, so there is no
		// recorded history that could have been rewritten.
		return nil
	}
	if !hasHead {
		// A previously-synced branch that now has no commits at all was
		// deleted or reset. Treating it as "nothing to sync" would let the
		// next sync silently recreate the ref and orphan the old history.
		return fmt.Errorf("%s history vanished: commit %s was synced previously, but the current branch now has no commits (deleted or reset?); resolve manually before syncing", label, lastSha)
	}
	ok, err := repo.IsAncestor(lastSha, head)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s history was rewritten: last-synced commit %s is no longer an ancestor of HEAD (rebase/amend/force-push?); resolve manually before syncing", label, lastSha)
	}
	return nil
}

func unsyncedShas(repo *gitrepo.Repo, lastSha, head string, hasHead bool) ([]string, error) {
	if !hasHead {
		return nil, nil
	}
	rangeSpec := head
	if lastSha != "" {
		rangeSpec = lastSha + ".." + head
	}
	return repo.RevList(rangeSpec)
}

func checkNoMergeCommits(repo *gitrepo.Repo, shas []string) error {
	for _, sha := range shas {
		c, err := repo.CommitInfo(sha)
		if err != nil {
			return err
		}
		if len(c.Parents) > 1 {
			return fmt.Errorf("commit %s has %d parents (a merge commit); merge commits are not supported by this MVP (single-branch only)", sha, len(c.Parents))
		}
	}
	return nil
}

func runPrivateToMirror(privateSide, mirrorSide *pairing.Side, branch string, pre *preflightResult) (*Report, error) {
	if len(pre.privateUnsyncedShas) == 0 {
		return &Report{Direction: "private->mirror", Branch: branch, NoOp: true, Message: "private is up to date, nothing to sync"}, nil
	}

	clean, err := mirrorSide.Repo.IsClean()
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, fmt.Errorf("mirror working tree is not clean; commit or discard changes there before syncing (private->mirror would otherwise discard them)")
	}

	current, history, err := secrets.LoadCurrentAndRecord(privateSide.Root)
	if err != nil {
		return nil, err
	}
	mapper := tokenize.NewMapper(current, history.Values)
	excludePatterns := append(append([]string{}, config.ForcedExcludes...), privateSide.Config.Exclude...)

	carryForward, err := carryForwardEntries(mirrorSide.Repo, pre.mirrorHead, pre.mirrorHasHead)
	if err != nil {
		return nil, err
	}

	built, err := buildPrivateToMirror(privateSide.Repo, mirrorSide.Repo, pre.privateUnsyncedShas, excludePatterns, mapper, pre.branchState, carryForward)
	if err != nil {
		return nil, err
	}

	if err := gateBuiltCommits(mirrorSide.Repo, built, mapper, current); err != nil {
		return nil, err
	}

	bs := pre.branchState
	for _, bc := range built {
		bs.Record(bc.SourceSha, bc.TargetSha)
	}

	newTip := built[len(built)-1].TargetSha
	if err := finalizeSync(mirrorSide.Repo, "mirror", "refs/heads/"+branch, pre.mirrorHead, pre.mirrorHasHead, newTip,
		privateSide.Root, mirrorSide.Root, branch, bs); err != nil {
		return nil, err
	}

	return &Report{Direction: "private->mirror", Branch: branch, Applied: built}, nil
}

// gateBuiltCommits is the pre-ref-update safety gate: it re-reads every
// object buildPrivateToMirror just wrote into mirror's object database and
// verifies that no known secret value survived tokenization and that no
// token refers to an unknown key. The objects exist only as unreferenced
// loose objects at this point, so failing here leaves mirror's refs and
// working tree completely untouched.
func gateBuiltCommits(mirror *gitrepo.Repo, built []BuiltCommit, mapper *tokenize.Mapper, current map[string]string) error {
	for _, bc := range built {
		for _, f := range leakcheck.ScanBytesForLeaks([]byte(bc.Message), mapper, current) {
			return fmt.Errorf("leak check failed on commit %s message: %s; mirror was not modified", bc.SourceSha, f.Detail)
		}

		commit, err := mirror.CommitInfo(bc.TargetSha)
		if err != nil {
			return err
		}
		entries, err := mirror.LsTreeRecursive(commit.Tree)
		if err != nil {
			return err
		}
		for _, e := range entries {
			// Bookkeeping files are pubmir's own content, carried forward
			// verbatim rather than tokenized (see config.BookkeepingPaths).
			if slices.Contains(config.BookkeepingPaths, e.Path) {
				continue
			}
			content, err := mirror.CatFileBlob(e.Sha)
			if err != nil {
				return err
			}
			for _, f := range leakcheck.ScanBytesForLeaks(content, mapper, current) {
				return fmt.Errorf("leak check failed on commit %s, path %q: %s; mirror was not modified", bc.SourceSha, e.Path, f.Detail)
			}
		}
	}
	return nil
}

func runMirrorToPrivate(privateSide, mirrorSide *pairing.Side, branch string, pre *preflightResult, opts Options) (*Report, error) {
	if len(pre.mirrorUnsyncedShas) == 0 {
		return &Report{Direction: "mirror->private", Branch: branch, NoOp: true, Message: "mirror is up to date, nothing to sync"}, nil
	}

	clean, err := privateSide.Repo.IsClean()
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, fmt.Errorf("private working tree is not clean; commit or discard changes there before syncing (mirror->private would otherwise discard them)")
	}

	current, err := secrets.LoadEnv(privateSide.Root)
	if err != nil {
		return nil, err
	}

	carryForward, err := carryForwardEntries(privateSide.Repo, pre.privateHead, pre.privateHasHead)
	if err != nil {
		return nil, err
	}

	built, err := buildMirrorToPrivate(mirrorSide.Repo, privateSide.Repo, pre.mirrorUnsyncedShas, current, pre.branchState, carryForward)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(opts.Stdout, "Current repository: mirror\nTarget repository:  private\n\n%d commit(s) will be applied to PRIVATE:\n\n", len(built))
	for _, bc := range built {
		fmt.Fprintf(opts.Stdout, "  %s  %s\n", gitrepo.ShortSha(bc.TargetSha), bc.Message)
	}
	fmt.Fprintln(opts.Stdout)

	if !opts.Yes {
		if !confirm(opts.Stdin, opts.Stdout) {
			return &Report{Direction: "mirror->private", Branch: branch, NoOp: true, Message: "cancelled: private repository was not modified"}, nil
		}
	}

	bs := pre.branchState
	for _, bc := range built {
		bs.Record(bc.TargetSha, bc.SourceSha)
	}

	newTip := built[len(built)-1].TargetSha
	if err := finalizeSync(privateSide.Repo, "private", "refs/heads/"+branch, pre.privateHead, pre.privateHasHead, newTip,
		privateSide.Root, mirrorSide.Root, branch, bs); err != nil {
		return nil, err
	}

	return &Report{Direction: "mirror->private", Branch: branch, Applied: built}, nil
}

// carryForwardEntries reads target's own current HEAD tree (if it has one
// yet) for config.BookkeepingPaths, so buildPrivateToMirror/buildMirrorToPrivate
// can re-insert them unchanged into every newly built commit.
func carryForwardEntries(target *gitrepo.Repo, head string, hasHead bool) ([]gitrepo.TreeEntry, error) {
	if !hasHead {
		return nil, nil
	}
	var entries []gitrepo.TreeEntry
	for _, p := range config.BookkeepingPaths {
		e, ok, err := target.LookupPath(head, p)
		if err != nil {
			return nil, err
		}
		if ok {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// finalizeSync commits a built sync to the target repository: it moves the
// branch ref, records the commit mapping, and only then brings the working
// tree up to date.
//
// The order matters. Moving the ref is the point of no return, so state is
// recorded immediately after it and before the purely local working-tree
// reset. Each failure mode reports exactly how far the operation actually
// got — claiming the repository is untouched once its branch has already
// moved would send the user looking in the wrong place.
func finalizeSync(target *gitrepo.Repo, label, ref, oldHead string, hasOldHead bool, newTip string,
	privateRoot, mirrorRoot, branch string, bs *state.BranchState) error {

	old := gitrepo.ZeroSha
	if hasOldHead {
		old = oldHead
	}
	if err := target.UpdateRef(ref, newTip, old); err != nil {
		return fmt.Errorf("%s repository was left untouched: updating %s failed: %w", label, ref, err)
	}

	if err := state.SaveBoth(privateRoot, mirrorRoot, branch, bs); err != nil {
		return fmt.Errorf("the commits WERE applied to %s (%s now points at %s), but recording sync state failed: %w\n"+
			"Until the state file is written both sides will look unsynchronized and `pubmir sync` will report divergence; "+
			"fix the underlying problem, then re-run `pubmir status` to confirm", label, ref, newTip, err)
	}

	if err := target.ResetHard(newTip); err != nil {
		return fmt.Errorf("the commits WERE applied to %s and the sync was recorded, but its working tree could not be updated: %w\n"+
			"Run `git -C %s reset --hard %s` to finish", label, err, target.Root, newTip)
	}
	return nil
}

func confirm(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "Proceed? [y/N] ")
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}
