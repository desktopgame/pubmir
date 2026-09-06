// Package gitrepo wraps git plumbing commands as Go functions. It never
// implements git's object format itself; every operation shells out to the
// system git binary.
package gitrepo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func osEnviron() []string { return os.Environ() }

// Repo represents a single git repository, addressed by its --git-dir and
// working tree root. Operations run with GIT_DIR/--work-tree pinned to this
// repo so a process can hold handles to two unrelated repositories (private
// and mirror) at once without ever confusing their object databases.
type Repo struct {
	Root   string // working tree root (absolute)
	GitDir string // .git directory (absolute)
}

// Open resolves root into a Repo, verifying it is a git working tree (not
// bare, not a subdirectory of one masquerading as a root).
func Open(root string) (*Repo, error) {
	r := &Repo{Root: root}
	out, err := r.runRaw(nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", root, err)
	}
	r.GitDir = strings.TrimSpace(out)
	top, err := r.runRaw(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s has no working tree (bare repos are not supported): %w", root, err)
	}
	r.Root = strings.TrimSpace(top)
	return r, nil
}

// runRaw executes git with the given args in the context of this repo's
// git-dir/work-tree, before Root/GitDir are necessarily populated (used by
// Open itself).
func (r *Repo) runRaw(stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Root
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// run executes git against this repo's object database via --git-dir, so it
// works regardless of the caller's current working directory and never
// touches another repo's odb.
func (r *Repo) run(stdin []byte, args ...string) (string, error) {
	full := append([]string{"--git-dir=" + r.GitDir, "--work-tree=" + r.Root}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Root
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// runWithEnv is like run but with additional environment variables set
// (e.g. GIT_AUTHOR_* / GIT_COMMITTER_* to replay an original commit's
// identity and timestamp exactly).
func (r *Repo) runWithEnv(stdin []byte, env []string, args ...string) (string, error) {
	full := append([]string{"--git-dir=" + r.GitDir, "--work-tree=" + r.Root}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Root
	cmd.Env = append(osEnviron(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return out.String(), nil
}

// CurrentBranch returns the short branch name HEAD points to. Works even on
// an unborn branch (no commits yet).
func (r *Repo) CurrentBranch() (string, error) {
	out, err := r.run(nil, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("HEAD is not a branch (detached?): %w", err)
	}
	return strings.TrimSpace(out), nil
}

// RevParseVerify resolves rev to a sha. ok is false (with no error) if rev
// does not exist, e.g. an unborn HEAD.
func (r *Repo) RevParseVerify(rev string) (sha string, ok bool, err error) {
	out, err := r.run(nil, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(out), true, nil
}

// IsAncestor reports whether ancestor is an ancestor of (or equal to)
// descendant.
func (r *Repo) IsAncestor(ancestor, descendant string) (bool, error) {
	_, err := r.run(nil, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	// merge-base --is-ancestor exits 1 for "no", and >1 for real errors.
	var exitErr *exec.ExitError
	if ee, isExit := asExitError(err); isExit {
		exitErr = ee
		if exitErr.ExitCode() == 1 {
			return false, nil
		}
	}
	return false, err
}

func asExitError(err error) (*exec.ExitError, bool) {
	for e := err; e != nil; e = errorsUnwrap(e) {
		if ee, ok := e.(*exec.ExitError); ok {
			return ee, true
		}
	}
	return nil, false
}

func errorsUnwrap(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return nil
}

// RevList returns commit shas in the given range, oldest first, in
// topological order (parents always precede children).
func (r *Repo) RevList(rangeSpec string) ([]string, error) {
	out, err := r.run(nil, "rev-list", "--reverse", "--topo-order", rangeSpec)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// RevListCount returns the number of commits in rangeSpec.
func (r *Repo) RevListCount(rangeSpec string) (int, error) {
	out, err := r.run(nil, "rev-list", "--count", rangeSpec)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, err
	}
	return n, nil
}

// RevListAllBlobs returns the sha of every unique blob reachable from any
// ref, for full-history leak scanning. Object types are resolved with a
// single `cat-file --batch-check` process rather than one process per
// object, so the cost stays reasonable on repositories with long histories.
func (r *Repo) RevListAllBlobs() ([]string, error) {
	out, err := r.run(nil, "rev-list", "--objects", "--all")
	if err != nil {
		return nil, err
	}

	var ids strings.Builder
	for _, line := range splitLines(out) {
		sha, _, _ := strings.Cut(line, " ")
		if sha == "" {
			continue
		}
		ids.WriteString(sha)
		ids.WriteByte('\n')
	}
	if ids.Len() == 0 {
		return nil, nil
	}

	checked, err := r.run([]byte(ids.String()), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range splitLines(checked) {
		sha, typ, ok := strings.Cut(line, " ")
		if ok && typ == "blob" {
			shas = append(shas, sha)
		}
	}
	return shas, nil
}

// AllTrackedPathsAllCommits returns every distinct path that has ever been
// tracked in any commit reachable from any ref, for full-history path
// leak scanning.
func (r *Repo) AllTrackedPathsAllCommits() ([]string, error) {
	out, err := r.run(nil, "rev-list", "--all")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, sha := range splitLines(out) {
		entries, err := r.LsTreeRecursive(sha + "^{tree}")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !seen[e.Path] {
				seen[e.Path] = true
				paths = append(paths, e.Path)
			}
		}
	}
	return paths, nil
}

// HasRemote reports whether any remote is configured. Only the fact is
// returned, never the URL, which may itself be sensitive.
func (r *Repo) HasRemote() (bool, error) {
	out, err := r.run(nil, "remote")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// ShortSha abbreviates an object id for display.
func ShortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
