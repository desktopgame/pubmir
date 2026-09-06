package syncengine

import (
	"fmt"

	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/tokenize"
)

// buildPrivateToMirror replays each private commit in shas (oldest first,
// already verified non-merge) onto mirror as a brand-new, fully sanitized
// commit chain. It only ever writes loose objects into mirror's object
// database — it never touches any ref, so a failure partway through leaves
// mirror completely unaffected.
func buildPrivateToMirror(
	private, mirror *gitrepo.Repo,
	shas []string,
	excludePatterns []string,
	stubPatterns []string,
	mapper *tokenize.Mapper,
	bs *state.BranchState,
	carryForward []gitrepo.TreeEntry,
) ([]BuiltCommit, error) {
	parentOf := map[string]string{} // private sha -> mirror sha
	for _, m := range bs.Mapping {
		parentOf[m.Private] = m.Mirror
	}

	var built []BuiltCommit
	for _, sha := range shas {
		commit, err := private.CommitInfo(sha)
		if err != nil {
			return nil, err
		}

		entries, err := private.LsTreeRecursive(commit.Tree)
		if err != nil {
			return nil, err
		}

		// Classification order is exclude > stub > tokenize, and the path
		// leak check runs before stubbing: hiding a file's content does not
		// make a secret value in its *path* safe.
		var mirrorEntries []gitrepo.TreeEntry
		for _, e := range entries {
			if tokenize.IsExcluded(e.Path, excludePatterns) {
				continue
			}
			if key, leak := mapper.FindLeakingKey(e.Path); leak {
				return nil, fmt.Errorf("commit %s: path %q contains a secret value (maps to %s); sync aborted, mirror untouched", sha, e.Path, key)
			}

			if tokenize.MatchesAny(e.Path, stubPatterns) {
				if !stub.IsRegularFileMode(e.Mode) {
					return nil, fmt.Errorf("commit %s: path %q matches a stub pattern but is not a regular file (mode %s); stubbing symlinks and submodules is not supported", sha, e.Path, e.Mode)
				}
				// The private blob is never read, so its content cannot
				// reach mirror's object database even as an unreferenced
				// object.
				stubSha, err := mirror.HashObjectWrite(stub.Content)
				if err != nil {
					return nil, err
				}
				mirrorEntries = append(mirrorEntries, gitrepo.TreeEntry{
					Mode: e.Mode, Type: e.Type, Sha: stubSha, Path: e.Path,
				})
				continue
			}

			content, err := private.CatFileBlob(e.Sha)
			if err != nil {
				return nil, err
			}
			sanitized := mapper.Tokenize(content)
			newBlobSha, err := mirror.HashObjectWrite(sanitized)
			if err != nil {
				return nil, err
			}
			mirrorEntries = append(mirrorEntries, gitrepo.TreeEntry{
				Mode: e.Mode, Type: e.Type, Sha: newBlobSha, Path: e.Path,
			})
		}

		mirrorTree, err := buildNestedTree(mirror, append(mirrorEntries, carryForward...))
		if err != nil {
			return nil, err
		}

		sanitizedMessage := string(mapper.Tokenize([]byte(commit.Message)))

		var mirrorParents []string
		for _, p := range commit.Parents {
			mp, ok := parentOf[p]
			if !ok {
				return nil, fmt.Errorf("commit %s: parent %s has not been synced to mirror yet", sha, p)
			}
			mirrorParents = append(mirrorParents, mp)
		}

		mirrorSha, err := mirror.CommitTree(mirrorTree, mirrorParents, sanitizedMessage, commit.Author, commit.Committer)
		if err != nil {
			return nil, err
		}

		parentOf[sha] = mirrorSha
		built = append(built, BuiltCommit{SourceSha: sha, TargetSha: mirrorSha, Message: firstLine(sanitizedMessage)})
	}
	return built, nil
}
