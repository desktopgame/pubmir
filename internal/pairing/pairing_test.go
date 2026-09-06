package pairing_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/pairing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test-user-noreply.example")
	runGit(t, dir, "config", "user.name", "Test")
}

func writeConfig(t *testing.T, dir string, role config.Role, pair string) {
	t.Helper()
	cfg := &config.Config{Role: role, Exclude: config.DefaultExclude()}
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	local := &config.Local{Pair: pair}
	if err := local.Save(dir); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRejectsSelfPair(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repo")
	initGitRepo(t, dir)
	writeConfig(t, dir, config.RolePrivate, ".")

	if _, err := pairing.Resolve(dir); err == nil {
		t.Fatal("expected error for a repository paired with itself")
	}
}

func TestResolveRejectsNestedPair(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	initGitRepo(t, outer)
	initGitRepo(t, inner)
	writeConfig(t, outer, config.RolePrivate, "inner")
	writeConfig(t, inner, config.RoleMirror, "..")

	if _, err := pairing.Resolve(outer); err == nil {
		t.Fatal("expected error for a pair nested inside its own working tree")
	}
}

func TestResolveRejectsRoleMismatch(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	initGitRepo(t, a)
	initGitRepo(t, b)
	// Both sides claim to be "private" — a valid pair must have opposite
	// roles pointing back at each other.
	writeConfig(t, a, config.RolePrivate, "../b")
	writeConfig(t, b, config.RolePrivate, "../a")

	if _, err := pairing.Resolve(a); err == nil {
		t.Fatal("expected error for role mismatch (both sides private)")
	}
}

func TestResolveRejectsPairNotPointingBack(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	c := filepath.Join(root, "c")
	initGitRepo(t, a)
	initGitRepo(t, b)
	initGitRepo(t, c)
	writeConfig(t, a, config.RolePrivate, "../b")
	writeConfig(t, b, config.RoleMirror, "../c") // b points at c, not a

	if _, err := pairing.Resolve(a); err == nil {
		t.Fatal("expected error when pair's pair does not point back")
	}
}

func TestResolveAcceptsValidPair(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	initGitRepo(t, a)
	initGitRepo(t, b)
	writeConfig(t, a, config.RolePrivate, "../b")
	writeConfig(t, b, config.RoleMirror, "../a")

	p, err := pairing.Resolve(a)
	if err != nil {
		t.Fatalf("expected a valid pair to resolve, got: %v", err)
	}
	if p.Self.Config.Role != config.RolePrivate || p.Other.Config.Role != config.RoleMirror {
		t.Fatalf("unexpected roles: self=%v other=%v", p.Self.Config.Role, p.Other.Config.Role)
	}
}
