package syncengine

import (
	"fmt"
	"slices"

	"pubmir/internal/gitrepo"
	"pubmir/internal/state"
	"pubmir/internal/tokenize"
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

		var privateEntries []gitrepo.TreeEntry
		for _, e := range entries {
			// mirror's own .pubmir.yml/.gitignore must never overwrite
			// private's; carryForward re-inserts private's own copies below.
			if slices.Contains(carryForwardPaths, e.Path) {
				continue
			}
			if loc := tokenize.TokenPattern.FindString(e.Path); loc != "" {
				return nil, fmt.Errorf("commit %s: path %q contains an unresolved pubmir token (%s); path tokenization is not supported, resolve manually before syncing", sha, e.Path, loc)
			}
			content, err := mirror.CatFileBlob(e.Sha)
			if err != nil {
				return nil, err
			}
			restored, err := tokenize.Detokenize(content, currentSecrets)
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

		privateTree, err := buildNestedTree(private, append(privateEntries, carryForward...))
		if err != nil {
			return nil, err
		}

		restoredMessageBytes, err := tokenize.Detokenize([]byte(commit.Message), currentSecrets)
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
