package syncengine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pubmir/internal/cli"
	"pubmir/internal/leakcheck"
	"pubmir/internal/pairing"
	"pubmir/internal/secrets"
	"pubmir/internal/syncengine"
)

// tempDir is like t.TempDir(), but tolerates Windows occasionally holding a
// brief file lock (e.g. from antivirus scanning just-written git objects)
// past the end of the test by retrying cleanup instead of failing the test
// over what is a host artifact, not a pubmir correctness issue.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pubmir-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var err error
		for range 10 {
			if err = os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("tempDir cleanup: giving up removing %s: %v", dir, err)
	})
	return dir
}

// --- test helpers -----------------------------------------------------

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test-user-noreply.example")
	runGit(t, dir, "config", "user.name", "Test")
	// Keep blob content byte-identical to what tests write and compare;
	// Windows defaults core.autocrlf=true, which would otherwise rewrite
	// LF to CRLF on checkout and break exact-content assertions.
	runGit(t, dir, "config", "core.autocrlf", "false")
}

// restoreCwd must be called once per test (not per chdir) so the process
// working directory - shared across the whole test binary - is always put
// back before the next test's t.TempDir() is removed.
func restoreCwd(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", message)
}

// pair is a freshly pubmir-initialized private/mirror repo pair sharing a
// parent temp directory, ready for the first real content commit.
type pair struct {
	private, mirror string
}

func setupPair(t *testing.T) pair {
	t.Helper()
	restoreCwd(t)
	root := tempDir(t)
	priv := filepath.Join(root, "private")
	mir := filepath.Join(root, "mirror")
	initGitRepo(t, priv)
	initGitRepo(t, mir)

	chdir(t, priv)
	if err := cli.RunInit([]string{"--role", "private", "--pair", "../mirror"}); err != nil {
		t.Fatalf("init private: %v", err)
	}
	chdir(t, mir)
	if err := cli.RunInit([]string{"--role", "mirror", "--pair", "../private"}); err != nil {
		t.Fatalf("init mirror: %v", err)
	}
	return pair{private: priv, mirror: mir}
}

func setSecrets(t *testing.T, privateDir string, kv map[string]string) {
	t.Helper()
	var sb strings.Builder
	for k, v := range kv {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(v)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(secrets.EnvPath(privateDir), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sync(t *testing.T, dir string, yes bool) (*syncengine.Report, error) {
	t.Helper()
	return syncengine.Run(dir, syncengine.Options{Yes: yes})
}

func headSha(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

// --- happy path ---------------------------------------------------------

func TestBootstrapAndRoundTrip(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{
		"VPS_PUBLIC_IP": "203.0.113.42",
		"VPS_USER":      "actual-user",
	})
	writeFile(t, p.private, "docs/notes.txt", "ssh actual-user@203.0.113.42\n")
	writeFile(t, p.private, "secrets/robot.key", "should-never-sync\n")
	commitAll(t, p.private, "Add SSH note for 203.0.113.42")

	report, err := sync(t, p.private, false)
	if err != nil {
		t.Fatalf("private->mirror sync: %v", err)
	}
	if len(report.Applied) == 0 {
		t.Fatal("expected commits to be applied")
	}

	got, err := os.ReadFile(filepath.Join(p.mirror, "docs/notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ssh <PUBMIR:VPS_USER>@<PUBMIR:VPS_PUBLIC_IP>\n"; string(got) != want {
		t.Fatalf("mirror content = %q, want %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(p.mirror, "secrets/robot.key")); !os.IsNotExist(err) {
		t.Fatalf("excluded file leaked into mirror (err=%v)", err)
	}

	// Each side must keep its OWN .pubmir.yml (this used to get clobbered
	// by the other side's tree during the mirror->private direction).
	mirrorCfg, err := os.ReadFile(filepath.Join(p.mirror, ".pubmir.yml"))
	if err != nil || !strings.Contains(string(mirrorCfg), "role: mirror") {
		t.Fatalf(".pubmir.yml in mirror = %q, err=%v", mirrorCfg, err)
	}

	clean := runGit(t, p.mirror, "status", "--porcelain")
	if strings.TrimSpace(clean) != "" {
		t.Fatalf("mirror working tree not clean after sync:\n%s", clean)
	}

	// AI edits mirror; sync back to private.
	writeFile(t, p.mirror, "docs/notes.txt", "ssh <PUBMIR:VPS_USER>@<PUBMIR:VPS_PUBLIC_IP> -p 2222\n")
	commitAll(t, p.mirror, "AI: add port note for <PUBMIR:VPS_PUBLIC_IP>")

	if _, err := sync(t, p.mirror, true); err != nil {
		t.Fatalf("mirror->private sync: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(p.private, "docs/notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ssh actual-user@203.0.113.42 -p 2222\n"; string(restored) != want {
		t.Fatalf("private content after round trip = %q, want %q", restored, want)
	}

	privateCfg, err := os.ReadFile(filepath.Join(p.private, ".pubmir.yml"))
	if err != nil || !strings.Contains(string(privateCfg), "role: private") {
		t.Fatalf(".pubmir.yml in private = %q, err=%v", privateCfg, err)
	}

	clean = runGit(t, p.private, "status", "--porcelain")
	if strings.TrimSpace(clean) != "" {
		t.Fatalf("private working tree not clean after sync:\n%s", clean)
	}

	// Re-running sync with nothing new must be a no-op both ways.
	report, err = sync(t, p.mirror, true)
	if err != nil {
		t.Fatalf("second mirror sync: %v", err)
	}
	if !report.NoOp {
		t.Fatalf("expected no-op on repeat sync, got %+v", report)
	}
	report, err = sync(t, p.private, false)
	if err != nil {
		t.Fatalf("second private sync: %v", err)
	}
	if !report.NoOp {
		t.Fatalf("expected no-op on repeat sync, got %+v", report)
	}

	// `check` must be satisfied with the result.
	side, err := pairing.Resolve(p.mirror)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := leakcheck.Check(side.Self, side.Other)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no leak findings, got %+v", findings)
	}
}

func TestSecretRotationHistoryStillTokenizesOldValue(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"VPS_PUBLIC_IP": "203.0.113.42"})
	writeFile(t, p.private, "docs/a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	// A commit made with the OLD value, but not yet synced when the value
	// gets rotated.
	writeFile(t, p.private, "docs/legacy.txt", "ssh actual@203.0.113.42\n")
	commitAll(t, p.private, "legacy note")
	setSecrets(t, p.private, map[string]string{"VPS_PUBLIC_IP": "203.0.113.99"})

	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("post-rotation sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(p.mirror, "docs/legacy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ssh actual@<PUBMIR:VPS_PUBLIC_IP>\n"; string(got) != want {
		t.Fatalf("legacy content = %q, want %q (old value must still tokenize via history)", got, want)
	}
}

// --- safety guards --------------------------------------------------------

func TestUnknownTokenAbortsWithoutTouchingPrivate(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	before := headSha(t, p.private)
	writeFile(t, p.mirror, "a.txt", "bad token <PUBMIR:UNKNOWN_KEY>\n")
	commitAll(t, p.mirror, "bad")

	if _, err := sync(t, p.mirror, true); err == nil {
		t.Fatal("expected unknown-token error")
	}
	if after := headSha(t, p.private); after != before {
		t.Fatalf("private HEAD changed despite abort: %s -> %s", before, after)
	}
}

func TestMergeCommitRejected(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")

	runGit(t, p.private, "checkout", "-q", "-b", "feature")
	writeFile(t, p.private, "b.txt", "feature\n")
	commitAll(t, p.private, "feature work")
	runGit(t, p.private, "checkout", "-q", "main")
	runGit(t, p.private, "merge", "-q", "--no-ff", "feature", "-m", "merge feature")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected merge commit to be rejected")
	}
}

func TestDivergenceRejected(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	writeFile(t, p.private, "a.txt", "private change\n")
	commitAll(t, p.private, "private change")
	writeFile(t, p.mirror, "a.txt", "mirror change\n")
	commitAll(t, p.mirror, "mirror change")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected divergence error")
	}
}

func TestBranchMismatchRejected(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	runGit(t, p.mirror, "checkout", "-q", "-b", "other")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected branch mismatch error")
	}
}

func TestHistoryRewriteRejected(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	runGit(t, p.private, "commit", "--amend", "-q", "-m", "rewritten")
	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected history-rewrite error")
	}
}

func TestPathLeakAborts(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.private, "203.0.113.42-backup/file.txt", "content\n")
	commitAll(t, p.private, "oops path leak")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected path-leak error")
	}
	if entries, _ := os.ReadDir(p.mirror); len(entries) > 0 {
		for _, e := range entries {
			if e.Name() == "203.0.113.42-backup" {
				t.Fatal("leaking path must not have been created in mirror")
			}
		}
	}
}

func TestSecretsFileTrackedAborts(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	runGit(t, p.private, "add", "-f", ".pubmir.env")
	runGit(t, p.private, "commit", "-q", "-m", "oops committed secrets")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected error because .pubmir.env is tracked by git")
	}
}
