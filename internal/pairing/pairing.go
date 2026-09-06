// Package pairing resolves a repository's paired counterpart and enforces
// the repository-boundary safety checks from init.md §13.1 before any sync,
// status, or check operation is allowed to proceed.
package pairing

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
)

// Side is one repository (private or mirror) with its config loaded.
type Side struct {
	Root   string
	Config *config.Config
	Local  *config.Local
	Repo   *gitrepo.Repo
}

// Pair is a fully-resolved, safety-checked private/mirror pair, addressed
// from Self's point of view.
type Pair struct {
	Self  *Side
	Other *Side
}

// Resolve loads cwd's pubmir config, resolves and loads its paired
// repository, and enforces every boundary check in init.md §13.1 (points
// 1-4; branch-match, history-rewrite, and merge-commit checks are
// sync-specific and live in syncengine).
func Resolve(cwd string) (*Pair, error) {
	self, err := loadSide(cwd)
	if err != nil {
		return nil, err
	}

	otherRoot, err := self.Local.ResolvePairRoot(self.Root)
	if err != nil {
		return nil, err
	}

	if err := checkNotSamePath(self.Root, otherRoot); err != nil {
		return nil, err
	}
	if err := checkNotNested(self.Root, otherRoot); err != nil {
		return nil, err
	}

	other, err := loadSide(otherRoot)
	if err != nil {
		return nil, fmt.Errorf("pair repository: %w", err)
	}

	if err := checkGitDirNotShared(self, other); err != nil {
		return nil, err
	}
	if err := checkRoleConsistency(self, other); err != nil {
		return nil, err
	}

	return &Pair{Self: self, Other: other}, nil
}

func loadSide(root string) (*Side, error) {
	repo, err := gitrepo.Open(root)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return nil, err
	}
	local, err := config.LoadLocal(repo.Root)
	if err != nil {
		return nil, err
	}
	return &Side{Root: repo.Root, Config: cfg, Local: local, Repo: repo}, nil
}

func checkNotSamePath(a, b string) error {
	if samePath(a, b) {
		return fmt.Errorf("private and mirror both resolve to %s: a repository cannot be paired with itself", a)
	}
	return nil
}

func checkNotNested(a, b string) error {
	if isSubPath(a, b) || isSubPath(b, a) {
		return fmt.Errorf("private (%s) and mirror (%s) may not be nested inside one another", a, b)
	}
	return nil
}

func checkGitDirNotShared(self, other *Side) error {
	if samePath(self.Repo.GitDir, other.Repo.GitDir) {
		return fmt.Errorf("private and mirror share the same .git directory (%s): they must be completely separate repositories", self.Repo.GitDir)
	}
	return nil
}

func checkRoleConsistency(self, other *Side) error {
	wantOther := config.RoleMirror
	if self.Config.Role == config.RoleMirror {
		wantOther = config.RolePrivate
	}
	if other.Config.Role != wantOther {
		return fmt.Errorf("role mismatch: this repository is %q but pair repository %s is %q (expected %q)",
			self.Config.Role, other.Root, other.Config.Role, wantOther)
	}
	otherPairRoot, err := other.Local.ResolvePairRoot(other.Root)
	if err != nil {
		return err
	}
	if !samePath(otherPairRoot, self.Root) {
		return fmt.Errorf("pair mismatch: %s's pair points to %s, not back to this repository (%s)",
			other.Root, otherPairRoot, self.Root)
	}
	return nil
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// isSubPath reports whether child is strictly inside parent.
func isSubPath(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
