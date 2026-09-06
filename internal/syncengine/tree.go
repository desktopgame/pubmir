package syncengine

import (
	"strings"

	"github.com/desktopgame/pubmir/internal/gitrepo"
)

// emptyTreeSha is git's well-known empty-tree object id (present in every
// repository by construction; it never needs to be written).
const emptyTreeSha = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// buildNestedTree writes the tree objects for a flat list of blob entries
// (each with a full "/"-separated path) into repo, and returns the sha of
// the resulting root tree. It only ever writes new tree objects into repo's
// object database — the blobs referenced by entries must already exist
// there.
func buildNestedTree(repo *gitrepo.Repo, entries []gitrepo.TreeEntry) (string, error) {
	if len(entries) == 0 {
		return emptyTreeSha, nil
	}

	var direct []gitrepo.TreeEntry
	groups := map[string][]gitrepo.TreeEntry{}
	var order []string
	for _, e := range entries {
		dir, rest, nested := strings.Cut(e.Path, "/")
		if !nested {
			direct = append(direct, e)
			continue
		}
		if _, ok := groups[dir]; !ok {
			order = append(order, dir)
		}
		e.Path = rest
		groups[dir] = append(groups[dir], e)
	}

	final := append([]gitrepo.TreeEntry{}, direct...)
	for _, dir := range order {
		subSha, err := buildNestedTree(repo, groups[dir])
		if err != nil {
			return "", err
		}
		final = append(final, gitrepo.TreeEntry{Mode: "040000", Type: "tree", Sha: subSha, Path: dir})
	}
	return repo.MkTree(final)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
