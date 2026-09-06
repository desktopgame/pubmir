package gitrepo

import (
	"fmt"
	"strings"
)

// Ident is a commit author/committer identity, including the raw date so
// that git commit-tree reproduces the original timestamp exactly.
type Ident struct {
	Name  string
	Email string
	Date  string // e.g. "1700000000 +0900", as produced by `git log --format=%ad`
}

// Commit is the subset of a commit object pubmir needs to replay it onto
// another repository.
type Commit struct {
	Sha       string
	Tree      string
	Parents   []string
	Message   string
	Author    Ident
	Committer Ident
}

// TreeEntry is one line of `git ls-tree`.
type TreeEntry struct {
	Mode string // e.g. "100644", "100755", "120000", "040000"
	Type string // "blob", "tree", "commit" (submodule)
	Sha  string
	Path string // full path relative to the tree root, "/"-separated
}

const fieldSep = "\x1f"
const recordSep = "\x1e"

// CommitInfo reads a commit object's metadata.
func (r *Repo) CommitInfo(sha string) (*Commit, error) {
	format := strings.Join([]string{
		"%T", "%P", "%an", "%ae", "%ad", "%cn", "%ce", "%cd",
	}, fieldSep) + recordSep + "%B"
	out, err := r.run(nil, "log", "-1", "--format="+format, "--date=raw", sha)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(out, recordSep, 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected git log output for %s", sha)
	}
	fields := strings.Split(parts[0], fieldSep)
	if len(fields) != 8 {
		return nil, fmt.Errorf("unexpected git log field count for %s", sha)
	}
	c := &Commit{
		Sha:     sha,
		Tree:    fields[0],
		Message: strings.TrimRight(parts[1], "\n"),
		Author: Ident{
			Name: fields[2], Email: fields[3], Date: fields[4],
		},
		Committer: Ident{
			Name: fields[5], Email: fields[6], Date: fields[7],
		},
	}
	if p := strings.TrimSpace(fields[1]); p != "" {
		c.Parents = strings.Split(p, " ")
	}
	return c, nil
}

// LsTreeRecursive lists every blob/submodule entry under tree (a tree-ish,
// e.g. "<sha>^{tree}"), recursing into subdirectories. Directory entries
// themselves are not returned.
func (r *Repo) LsTreeRecursive(tree string) ([]TreeEntry, error) {
	out, err := r.run(nil, "ls-tree", "-r", "-z", tree)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for rec := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		// format: "<mode> <type> <sha>\t<path>"
		metaPart, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		meta := strings.Fields(metaPart)
		if len(meta) != 3 {
			continue
		}
		entries = append(entries, TreeEntry{
			Mode: meta[0],
			Type: meta[1],
			Sha:  meta[2],
			Path: path,
		})
	}
	return entries, nil
}

// LookupPath resolves a single top-level path within tree, without
// recursing. ok is false if the path does not exist in tree.
func (r *Repo) LookupPath(tree, path string) (entry TreeEntry, ok bool, err error) {
	out, err := r.run(nil, "ls-tree", tree, "--", path)
	if err != nil {
		return TreeEntry{}, false, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return TreeEntry{}, false, nil
	}
	metaPart, p, cut := strings.Cut(out, "\t")
	if !cut {
		return TreeEntry{}, false, fmt.Errorf("unexpected ls-tree output: %q", out)
	}
	meta := strings.Fields(metaPart)
	if len(meta) != 3 {
		return TreeEntry{}, false, fmt.Errorf("unexpected ls-tree output: %q", out)
	}
	return TreeEntry{Mode: meta[0], Type: meta[1], Sha: meta[2], Path: p}, true, nil
}

// CatFileBlob reads a blob's raw content from this repo's object database.
func (r *Repo) CatFileBlob(sha string) ([]byte, error) {
	out, err := r.run(nil, "cat-file", "blob", sha)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// HashObjectWrite writes data as a blob into this repo's object database and
// returns its sha, without touching the working tree.
func (r *Repo) HashObjectWrite(data []byte) (string, error) {
	out, err := r.run(data, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// MkTree builds a single (non-recursive) tree object from entries, all of
// which must be direct children (blobs or already-built subtrees).
func (r *Repo) MkTree(entries []TreeEntry) (string, error) {
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s %s %s\t%s\n", e.Mode, e.Type, e.Sha, e.Path)
	}
	out, err := r.run([]byte(sb.String()), "mktree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitTree creates a commit object with the given tree/parents/message,
// preserving the original author/committer identity and timestamps, and
// returns its sha. No ref is updated.
func (r *Repo) CommitTree(tree string, parents []string, message string, author, committer Ident) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)
	cmd := r.withIdentEnv(author, committer)
	out, err := cmd.run(args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// identRunner runs git with author/committer identity pinned via env vars.
type identRunner struct {
	r   *Repo
	env []string
}

func (r *Repo) withIdentEnv(author, committer Ident) *identRunner {
	return &identRunner{r: r, env: []string{
		"GIT_AUTHOR_NAME=" + author.Name,
		"GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_AUTHOR_DATE=" + author.Date,
		"GIT_COMMITTER_NAME=" + committer.Name,
		"GIT_COMMITTER_EMAIL=" + committer.Email,
		"GIT_COMMITTER_DATE=" + committer.Date,
	}}
}

func (ir *identRunner) run(args ...string) (string, error) {
	return ir.r.runWithEnv(nil, ir.env, args...)
}

// ZeroSha, passed as oldSha to UpdateRef, asserts that ref must not already
// exist (fresh branch/root-commit creation).
const ZeroSha = "0000000000000000000000000000000000000000"

// UpdateRef sets ref to newSha. oldSha must be the ref's current value
// (ZeroSha to assert it does not exist yet) — a safe compare-and-swap that
// protects against unexpected concurrent or stale state.
func (r *Repo) UpdateRef(ref, newSha, oldSha string) error {
	_, err := r.run(nil, "update-ref", ref, newSha, oldSha)
	return err
}

// ResetHard moves HEAD/the current branch and the working tree to rev. The
// caller must have already verified the working tree is clean.
func (r *Repo) ResetHard(rev string) error {
	_, err := r.run(nil, "reset", "--hard", rev)
	return err
}

// IsClean reports whether the working tree and index have no changes
// (including untracked files).
func (r *Repo) IsClean() (bool, error) {
	out, err := r.run(nil, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// IsTracked reports whether path is present in the git index (used to detect
// a secrets file that was accidentally `git add`ed).
func (r *Repo) IsTracked(path string) (bool, error) {
	out, err := r.run(nil, "ls-files", "--", path)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Add stages paths (used only for pubmir's own tracked scaffolding files,
// e.g. .pubmir.yml and .gitignore — never for arbitrary user content).
func (r *Repo) Add(paths ...string) error {
	args := append([]string{"add", "--"}, paths...)
	_, err := r.run(nil, args...)
	return err
}

// HasStagedChanges reports whether the index differs from HEAD (or, on an
// unborn branch, whether anything is staged at all).
func (r *Repo) HasStagedChanges() (bool, error) {
	out, err := r.run(nil, "diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Commit creates a normal commit from the current index, using the
// repository's configured identity (works on an unborn branch too).
func (r *Repo) Commit(message string) error {
	_, err := r.run(nil, "commit", "-m", message)
	return err
}
