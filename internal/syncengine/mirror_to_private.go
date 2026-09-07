package syncengine

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/tokenize"
)

// buildMirrorToPrivate replays each mirror commit in shas (oldest first,
// already verified non-merge) onto private, restoring real secret values in
// place of <PUBMIR:KEY> tokens. Like buildPrivateToMirror, it only writes
// loose objects and never touches a ref, so an aborted run leaves private
// untouched.
func buildMirrorToPrivate(
	mirror, private *gitrepo.Repo,
	shas []string,
	currentSecrets map[string]string,
	bs *state.BranchState,
	carryForward []gitrepo.TreeEntry,
	stubPatterns []string,
	privateStubs []gitrepo.TreeEntry,
	reserved map[string]bool,
) ([]BuiltCommit, error) {
	parentOf := map[string]string{} // mirror sha -> private sha
	for _, m := range bs.Mapping {
		parentOf[m.Mirror] = m.Private
	}

	var built []BuiltCommit
	for _, sha := range shas {
		commit, err := mirror.CommitInfo(sha)
		if err != nil {
			return nil, err
		}

		entries, err := mirror.LsTreeRecursive(commit.Tree)
		if err != nil {
			return nil, err
		}

		if err := verifyStubsUntouched(mirror, sha, entries, privateStubs); err != nil {
			return nil, err
		}

		var privateEntries []gitrepo.TreeEntry
		for _, e := range entries {
			// A stub is a placeholder artifact, not an editable view of the
			// private file: its content must never travel back. private's
			// own real blob is re-inserted via privateStubs below.
			if tokenize.MatchesAny(e.Path, stubPatterns) {
				continue
			}
			// mirror's own .pubmir.yml/.gitignore/skill file must never
			// overwrite private's; carryForward re-inserts private's own
			// copies below (nothing to carry forward for the skill file,
			// which only ever exists on the mirror side).
			if slices.Contains(config.BookkeepingPaths, e.Path) {
				continue
			}
			if loc := tokenize.TokenPattern.FindString(e.Path); loc != "" {
				return nil, fmt.Errorf("commit %s: path %q contains an unresolved pubmir token (%s); path tokenization is not supported, resolve manually before syncing", sha, e.Path, loc)
			}
			content, err := mirror.CatFileBlob(e.Sha)
			if err != nil {
				return nil, err
			}
			restored, err := tokenize.Detokenize(content, currentSecrets, reserved)
			if err != nil {
				return nil, fmt.Errorf("commit %s, path %q: %w", sha, e.Path, err)
			}
			newBlobSha, err := private.HashObjectWrite(restored)
			if err != nil {
				return nil, err
			}
			privateEntries = append(privateEntries, gitrepo.TreeEntry{
				Mode: e.Mode, Type: e.Type, Sha: newBlobSha, Path: e.Path,
			})
		}

		privateEntries = append(privateEntries, privateStubs...)

		privateTree, err := buildNestedTree(private, append(privateEntries, carryForward...))
		if err != nil {
			return nil, err
		}

		restoredMessageBytes, err := tokenize.Detokenize([]byte(commit.Message), currentSecrets, reserved)
		if err != nil {
			return nil, fmt.Errorf("commit %s message: %w", sha, err)
		}
		restoredMessage := string(restoredMessageBytes)

		var privateParents []string
		for _, p := range commit.Parents {
			pp, ok := parentOf[p]
			if !ok {
				return nil, fmt.Errorf("commit %s: parent %s has not been synced to private yet", sha, p)
			}
			privateParents = append(privateParents, pp)
		}

		privateSha, err := private.CommitTree(privateTree, privateParents, restoredMessage, commit.Author, commit.Committer)
		if err != nil {
			return nil, err
		}

		parentOf[sha] = privateSha
		built = append(built, BuiltCommit{SourceSha: sha, TargetSha: privateSha, Message: firstLine(restoredMessage)})
	}
	return built, nil
}

// verifyStubsUntouched refuses the sync if a stub file was edited, removed
// or renamed in mirror. Stubs are one-way protected content: pubmir has no
// way to translate an edited placeholder back into a real private file, so
// the only safe response is to stop and let a human decide.
func verifyStubsUntouched(mirror *gitrepo.Repo, mirrorCommit string, mirrorEntries, privateStubs []gitrepo.TreeEntry) error {
	byPath := make(map[string]gitrepo.TreeEntry, len(mirrorEntries))
	for _, e := range mirrorEntries {
		byPath[e.Path] = e
	}

	for _, ps := range privateStubs {
		e, ok := byPath[ps.Path]
		if !ok {
			return fmt.Errorf("stubbed file was removed or renamed in the mirror (commit %s):\n\n  %s\n\n"+
				"Stub files are not writable through pubmir. Refusing to apply this change to the private repository.",
				mirrorCommit, ps.Path)
		}
		if !stub.IsRegularFileMode(e.Mode) {
			return fmt.Errorf("stubbed file was replaced by a non-regular entry in the mirror (commit %s):\n\n  %s\n\n"+
				"Stub files are not writable through pubmir. Refusing to apply this change to the private repository.",
				mirrorCommit, ps.Path)
		}
		content, err := mirror.CatFileBlob(e.Sha)
		if err != nil {
			return err
		}
		if !bytes.Equal(content, stub.Content) {
			return fmt.Errorf("stubbed file was modified in the mirror (commit %s):\n\n  %s\n\n"+
				"Stub files are not writable through pubmir. Refusing to apply this change to the private repository.",
				mirrorCommit, ps.Path)
		}
	}
	return nil
}
