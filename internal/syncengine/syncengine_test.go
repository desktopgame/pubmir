package syncengine_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/desktopgame/pubmir/internal/cli"
	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/leakcheck"
	"github.com/desktopgame/pubmir/internal/pairing"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/syncengine"
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

// runGitStdin is runGit with data piped to the command's stdin.
func runGitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitSucceeds runs git and reports whether it exited zero, for commands
// whose failure is a meaningful answer rather than a test error.
func gitSucceeds(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run() == nil
}

// writeConfigField rewrites private's tracked .pubmir.yml and commits it, so
// the working tree stays clean for the sync or rebuild that follows.
func writeConfigField(t *testing.T, privateDir string, mutate func(*config.Config), message string) {
	t.Helper()
	cfg, err := config.Load(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	mutate(cfg)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, ".pubmir.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, privateDir, message)
}

func setStubPatterns(t *testing.T, privateDir string, patterns []string) {
	t.Helper()
	writeConfigField(t, privateDir, func(c *config.Config) { c.Stub = patterns }, "configure stub patterns")
}

func setExcludePatterns(t *testing.T, privateDir string, patterns []string) {
	t.Helper()
	writeConfigField(t, privateDir, func(c *config.Config) { c.Exclude = patterns }, "configure exclude patterns")
}

func sync(t *testing.T, dir string, yes bool) (*syncengine.Report, error) {
	t.Helper()
	return syncengine.Run(dir, syncengine.Options{Yes: yes})
}

func checkMirror(t *testing.T, p pair) []leakcheck.Finding {
	t.Helper()
	side, err := pairing.Resolve(p.mirror)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := leakcheck.Check(side.Self, side.Other)
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func hasFinding(findings []leakcheck.Finding, category string) bool {
	for _, f := range findings {
		if f.Category == category {
			return true
		}
	}
	return false
}

func headSha(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
}

// exclude/stub only ever govern what leaves private, so mirror's config must
// not advertise settings pubmir would ignore there.
func TestMirrorConfigCarriesRoleOnly(t *testing.T) {
	p := setupPair(t)

	mirrorCfg, err := os.ReadFile(filepath.Join(p.mirror, ".pubmir.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(mirrorCfg)); got != "role: mirror" {
		t.Fatalf("mirror .pubmir.yml = %q, want only the role", got)
	}

	privateCfg, err := os.ReadFile(filepath.Join(p.private, ".pubmir.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(privateCfg), "role: private") ||
		!strings.Contains(string(privateCfg), "exclude:") {
		t.Fatalf("private .pubmir.yml should still carry role and exclude, got %q", privateCfg)
	}

	// A mirror config without those keys must still load and pair cleanly.
	if _, err := pairing.Resolve(p.mirror); err != nil {
		t.Fatalf("a role-only mirror config must resolve: %v", err)
	}
}

// Rules configured on the private side keep working while mirror's config
// stays untouched — the drift is expected, not a sign of a missed update.
func TestPrivateRulesApplyWithoutTouchingMirrorConfig(t *testing.T) {
	p := setupPair(t)
	setStubPatterns(t, p.private, []string{"config/production.yml"})
	setExcludePatterns(t, p.private, []string{"internal/**"})
	writeFile(t, p.private, "config/production.yml", realConfig)
	writeFile(t, p.private, "internal/secret.txt", "hidden\n")
	commitAll(t, p.private, "add files")

	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if got, _ := os.ReadFile(filepath.Join(p.mirror, "config/production.yml")); string(got) != string(stub.Content) {
		t.Fatalf("private's stub rule should apply, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.mirror, "internal/secret.txt")); !os.IsNotExist(err) {
		t.Fatalf("private's exclude rule should apply (err=%v)", err)
	}
	if got, _ := os.ReadFile(filepath.Join(p.mirror, ".pubmir.yml")); strings.TrimSpace(string(got)) != "role: mirror" {
		t.Fatalf("mirror config should stay role-only, got %q", got)
	}
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

	// The bundled skill installed by `pubmir init` must survive the sync's
	// wholesale tree replacement (same carry-forward hazard as .pubmir.yml).
	if _, err := os.Stat(filepath.Join(p.mirror, ".claude/skills/pubmir-mirror/SKILL.md")); err != nil {
		t.Fatalf("skill file did not survive private->mirror sync: %v", err)
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

// The pre-ref-update gate must inspect the built file content, not only the
// commit message: a token naming a key that does not exist would otherwise
// reach mirror's ref unnoticed and be undecodable on the way back.
func TestGateScansContentNotOnlyMessage(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}
	mirrorBefore := headSha(t, p.mirror)

	// The message is clean; only the file content carries the bad token.
	writeFile(t, p.private, "a.txt", "value is <PUBMIR:GHOST_KEY>\n")
	commitAll(t, p.private, "innocuous message")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("expected the leak-check gate to reject content with an unknown token")
	}
	if after := headSha(t, p.mirror); after != mirrorBefore {
		t.Fatalf("mirror ref moved despite a failed gate: %s -> %s", mirrorBefore, after)
	}
}

// A previously-synced branch whose commits have vanished must stop the sync
// rather than being treated as "nothing to sync" and silently re-created.
func TestVanishedTargetHistoryRejected(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	// Delete mirror's branch: HEAD still points at refs/heads/main, which
	// now resolves to nothing (an unborn branch).
	runGit(t, p.mirror, "update-ref", "-d", "refs/heads/main")

	writeFile(t, p.private, "a.txt", "further work\n")
	commitAll(t, p.private, "further work")

	_, err := sync(t, p.private, false)
	if err == nil {
		t.Fatal("expected sync to refuse to run against a branch whose synced history vanished")
	}
	if !strings.Contains(err.Error(), "vanished") {
		t.Fatalf("expected a 'history vanished' error, got: %v", err)
	}
}

// A secrets file is just as dangerous in a subdirectory as at the root, so
// the forced excludes must match at any depth.
func TestSecretsFileInSubdirectoryNotSynced(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.private, "sub/.pubmir.env", "IP=203.0.113.42\n")
	writeFile(t, p.private, "docs/notes.txt", "ordinary content\n")
	runGit(t, p.private, "add", "-f", "sub/.pubmir.env", "docs/notes.txt")
	runGit(t, p.private, "commit", "-q", "-m", "stray secrets file in a subdirectory")

	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.mirror, "sub/.pubmir.env")); !os.IsNotExist(err) {
		t.Fatalf("a .pubmir.env in a subdirectory leaked into mirror (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(p.mirror, "docs/notes.txt")); err != nil {
		t.Fatalf("ordinary content should still sync: %v", err)
	}
}

// --- stub files ---------------------------------------------------------

const realConfig = "database_password: hunter2\napi_endpoint: 203.0.113.42\n"

// setupStubbedPair returns a pair where config/production.yml is stubbed and
// already synced once.
func setupStubbedPair(t *testing.T) pair {
	t.Helper()
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	setStubPatterns(t, p.private, []string{"config/production.yml"})
	writeFile(t, p.private, "config/production.yml", realConfig)
	writeFile(t, p.private, "docs/notes.txt", "ordinary content\n")
	commitAll(t, p.private, "add config and docs")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("private->mirror sync: %v", err)
	}
	return p
}

func TestStubReplacesContentInMirror(t *testing.T) {
	p := setupStubbedPair(t)

	got, err := os.ReadFile(filepath.Join(p.mirror, "config/production.yml"))
	if err != nil {
		t.Fatalf("stub file must exist in mirror at the same path: %v", err)
	}
	if string(got) != string(stub.Content) {
		t.Fatalf("mirror stub content = %q, want the generated placeholder", got)
	}
	if strings.Contains(string(got), "hunter2") {
		t.Fatal("private content leaked into the stub")
	}

	// Ordinary files are unaffected by stubbing.
	notes, err := os.ReadFile(filepath.Join(p.mirror, "docs/notes.txt"))
	if err != nil || string(notes) != "ordinary content\n" {
		t.Fatalf("non-stub file = %q, err=%v", notes, err)
	}

	// private's real blob must never have been written into mirror's odb,
	// not even as an unreferenced object.
	privateBlob := strings.TrimSpace(runGit(t, p.private, "rev-parse", "HEAD:config/production.yml"))
	cmd := exec.Command("git", "cat-file", "-e", privateBlob+"^{object}")
	cmd.Dir = p.mirror
	if err := cmd.Run(); err == nil {
		t.Fatalf("private blob %s exists in mirror's object database", privateBlob)
	}
}

func TestExcludeTakesPrecedenceOverStub(t *testing.T) {
	p := setupPair(t)
	// secrets/** is excluded by default; also list it as a stub pattern.
	setStubPatterns(t, p.private, []string{"secrets/**"})
	writeFile(t, p.private, "secrets/prod.yml", realConfig)
	runGit(t, p.private, "add", "-f", "secrets/prod.yml")
	runGit(t, p.private, "commit", "-q", "-m", "add excluded-and-stubbed file")

	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.mirror, "secrets/prod.yml")); !os.IsNotExist(err) {
		t.Fatalf("exclude must win over stub, so no file at all should exist in mirror (err=%v)", err)
	}
}

func TestStubPathWithSecretValueAborts(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	setStubPatterns(t, p.private, []string{"config/*.yml"})
	writeFile(t, p.private, "config/203.0.113.42.yml", realConfig)
	commitAll(t, p.private, "stubbed file whose path leaks a secret")

	if _, err := sync(t, p.private, false); err == nil {
		t.Fatal("a secret value in the path must abort the sync even when the file is stubbed")
	}
}

func TestUntouchedStubDoesNotOverwritePrivateFile(t *testing.T) {
	p := setupStubbedPair(t)

	// The AI edits something else entirely and leaves the stub alone.
	writeFile(t, p.mirror, "docs/notes.txt", "edited by the AI\n")
	commitAll(t, p.mirror, "AI: update notes")

	if _, err := sync(t, p.mirror, true); err != nil {
		t.Fatalf("mirror->private sync: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(p.private, "config/production.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != realConfig {
		t.Fatalf("private's real file was overwritten: got %q, want %q", got, realConfig)
	}
	notes, err := os.ReadFile(filepath.Join(p.private, "docs/notes.txt"))
	if err != nil || string(notes) != "edited by the AI\n" {
		t.Fatalf("the AI's real edit should still land: %q, err=%v", notes, err)
	}
}

func TestModifiedStubRejected(t *testing.T) {
	p := setupStubbedPair(t)
	privateBefore := headSha(t, p.private)

	writeFile(t, p.mirror, "config/production.yml", "port: 8080\n")
	commitAll(t, p.mirror, "AI: edit the stub")

	_, err := sync(t, p.mirror, true)
	if err == nil {
		t.Fatal("editing a stub in mirror must be refused")
	}
	if !strings.Contains(err.Error(), "modified") {
		t.Fatalf("expected a 'modified' stub error, got: %v", err)
	}
	if after := headSha(t, p.private); after != privateBefore {
		t.Fatalf("private changed despite the refusal: %s -> %s", privateBefore, after)
	}
	if got, _ := os.ReadFile(filepath.Join(p.private, "config/production.yml")); string(got) != realConfig {
		t.Fatalf("private's real file must be untouched, got %q", got)
	}
}

func TestDeletedStubRejected(t *testing.T) {
	p := setupStubbedPair(t)

	runGit(t, p.mirror, "rm", "-q", "config/production.yml")
	runGit(t, p.mirror, "commit", "-q", "-m", "AI: delete the stub")

	_, err := sync(t, p.mirror, true)
	if err == nil {
		t.Fatal("deleting a stub in mirror must be refused")
	}
	if !strings.Contains(err.Error(), "removed or renamed") {
		t.Fatalf("expected a removal error, got: %v", err)
	}
}

func TestRenamedStubRejected(t *testing.T) {
	p := setupStubbedPair(t)

	runGit(t, p.mirror, "mv", "config/production.yml", "config/renamed.yml")
	runGit(t, p.mirror, "commit", "-q", "-m", "AI: rename the stub")

	if _, err := sync(t, p.mirror, true); err == nil {
		t.Fatal("renaming a stub in mirror must be refused")
	}
	if got, _ := os.ReadFile(filepath.Join(p.private, "config/production.yml")); string(got) != realConfig {
		t.Fatalf("private's real file must be untouched, got %q", got)
	}
}

func TestCheckAcceptsHealthyStub(t *testing.T) {
	p := setupStubbedPair(t)
	if findings := checkMirror(t, p); len(findings) != 0 {
		t.Fatalf("expected no findings for a healthy stub, got %+v", findings)
	}
}

func TestCheckFlagsModifiedStub(t *testing.T) {
	p := setupStubbedPair(t)
	writeFile(t, p.mirror, "config/production.yml", "someone rewrote this\n")

	findings := checkMirror(t, p)
	if !hasFinding(findings, "stub-modified") {
		t.Fatalf("expected a stub-modified finding, got %+v", findings)
	}
}

func TestCheckFlagsMissingStub(t *testing.T) {
	p := setupStubbedPair(t)
	if err := os.Remove(filepath.Join(p.mirror, "config/production.yml")); err != nil {
		t.Fatal(err)
	}

	findings := checkMirror(t, p)
	if !hasFinding(findings, "stub-missing") {
		t.Fatalf("expected a stub-missing finding, got %+v", findings)
	}
}

func TestCheckFlagsPrivateBlobInMirror(t *testing.T) {
	p := setupStubbedPair(t)

	// Simulate the real content reaching mirror's object database: identical
	// content hashes to the identical object id.
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = p.mirror
	cmd.Stdin = strings.NewReader(realConfig)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seeding mirror odb: %v\n%s", err, out)
	}

	findings := checkMirror(t, p)
	if !hasFinding(findings, "stub-content-leak") {
		t.Fatalf("expected a stub-content-leak finding, got %+v", findings)
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
