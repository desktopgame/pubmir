package syncengine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/desktopgame/pubmir/internal/cli"
	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/syncengine"
)

func restoreFixture(t *testing.T) (original pair, mirror, target, env string) {
	t.Helper()
	original = setupPair(t)
	setSecrets(t, original.private, map[string]string{"IP": "203.0.113.42"})
	setStubPatterns(t, original.private, []string{"cert.cer"})
	writeFile(t, original.private, "doc.txt", "host=203.0.113.42\n")
	writeFile(t, original.private, "cert.cer", "old certificate")
	commitAll(t, original.private, "host 203.0.113.42\n\nbody 203.0.113.42")
	if _, err := sync(t, original.private, true); err != nil {
		t.Fatal(err)
	}
	root := tempDir(t)
	mirror, target, env = filepath.Join(root, "mirror"), filepath.Join(root, "private"), filepath.Join(root, "new.env")
	runGit(t, root, "clone", "-q", original.mirror, mirror)
	runGit(t, mirror, "config", "user.name", "Test")
	runGit(t, mirror, "config", "user.email", "test@example.invalid")
	runGit(t, mirror, "config", "core.autocrlf", "false")
	writeFile(t, root, "new.env", "IP=198.51.100.27\n")
	return
}

func TestRestoreHistoryAndBidirectionalSync(t *testing.T) {
	original, mirror, target, env := restoreFixture(t)
	head := runGit(t, mirror, "rev-parse", "HEAD")
	objects := runGit(t, mirror, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	chdir(t, mirror)
	if err := cli.RunRestore([]string{"--to", target, "--secrets", env}); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, mirror, "rev-parse", "HEAD"); got != head {
		t.Fatal("mirror history changed")
	}
	if got := runGit(t, mirror, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)"); got != objects {
		t.Fatal("restore wrote objects into mirror")
	}
	if got := runGit(t, target, "show", "HEAD:doc.txt"); got != "host=198.51.100.27\n" {
		t.Fatalf("not restored: %q", got)
	}
	if got := runGit(t, target, "show", "HEAD:cert.cer"); got != string(stub.Content) {
		t.Fatalf("stub replaced: %q", got)
	}
	if got := runGit(t, target, "log", "--format=%B"); !strings.Contains(got, "body 198.51.100.27") {
		t.Fatal("message body not restored")
	}
	if got := runGit(t, target, "rev-list", "--count", "HEAD"); got != runGit(t, mirror, "rev-list", "--count", "HEAD") {
		t.Fatal("history not preserved")
	}
	if got := runGit(t, target, "remote"); got != "" {
		t.Fatalf("private has remote: %s", got)
	}
	if got := runGit(t, target, "status", "--porcelain"); got != "" {
		t.Fatalf("private dirty: %s", got)
	}
	if got := runGit(t, mirror, "status", "--porcelain"); got != "" {
		t.Fatalf("mirror dirty: %s", got)
	}
	for _, root := range []string{mirror, target} {
		st, err := state.Load(root)
		if err != nil || st.Branch("main").LastMirrorSha != strings.TrimSpace(head) {
			t.Fatalf("state not ready: %v", err)
		}
	}
	if findings := checkMirror(t, pair{private: target, mirror: mirror}); len(findings) > 0 {
		t.Fatalf("restored pair check failed: %v", findings)
	}
	runGit(t, target, "config", "user.name", "Test")
	runGit(t, target, "config", "user.email", "test@example.invalid")
	writeFile(t, target, "doc.txt", "new host=198.51.100.27\n")
	writeFile(t, target, "cert.cer", "new certificate")
	commitAll(t, target, "update new host")
	if _, err := sync(t, target, true); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, mirror, "show", "HEAD:doc.txt"); got != "new host=<PUBMIR:IP>\n" {
		t.Fatalf("new secret leaked: %q", got)
	}
	writeFile(t, mirror, "doc.txt", "AI edit <PUBMIR:IP>\n")
	commitAll(t, mirror, "AI edit")
	if _, err := sync(t, mirror, true); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, target, "show", "HEAD:doc.txt"); got != "AI edit 198.51.100.27\n" {
		t.Fatalf("reverse sync failed: %q", got)
	}
	if got := runGit(t, target, "show", "HEAD:cert.cer"); got != "new certificate" {
		t.Fatal("new certificate lost")
	}
	if got := runGit(t, original.private, "show", "HEAD:doc.txt"); got != "host=203.0.113.42\n" {
		t.Fatal("old private was changed")
	}
	if got := runGit(t, mirror, "log", "--all", "-p"); strings.Contains(got, "198.51.100.27") {
		t.Fatal("new secret entered mirror history")
	}
	if _, err := os.Stat(filepath.Join(mirror, config.EnvFileName)); !os.IsNotExist(err) {
		t.Fatal("secret file written into mirror")
	}
	current, _, err := secrets.LoadCurrentAndRecord(target)
	if err != nil || current["IP"] != "198.51.100.27" {
		t.Fatalf("new secrets not saved: %v", err)
	}
}

func TestRestoreRefusesUnsafeInputsWithoutPublishing(t *testing.T) {
	for _, scenario := range []string{"missing-token", "missing-file", "historical-token", "empty-value", "duplicate-value", "secrets-in-mirror", "existing-target", "nested-target", "already-paired", "dirty", "merge", "token-path", "bad-config"} {
		t.Run(scenario, func(t *testing.T) {
			original, mirror, target, env := restoreFixture(t)
			cfg := ""
			switch scenario {
			case "missing-token":
				writeFile(t, filepath.Dir(env), filepath.Base(env), "OTHER=safe\n")
			case "missing-file":
				env += ".missing"
			case "historical-token":
				runGit(t, mirror, "commit", "--allow-empty", "-qm", "old <PUBMIR:OLD_KEY>")
				runGit(t, mirror, "commit", "--allow-empty", "-qm", "latest safe message")
			case "empty-value":
				writeFile(t, filepath.Dir(env), filepath.Base(env), "IP=\n")
			case "duplicate-value":
				writeFile(t, filepath.Dir(env), filepath.Base(env), "IP=198.51.100.27\nOTHER=198.51.100.27\n")
			case "secrets-in-mirror":
				runGit(t, mirror, "config", "core.excludesFile", env+".ignore")
				writeFile(t, filepath.Dir(env), filepath.Base(env)+".ignore", "new.env\n")
				writeFile(t, mirror, "new.env", "IP=198.51.100.27\n")
				env = filepath.Join(mirror, "new.env")
			case "existing-target":
				writeFile(t, target, "keep.txt", "existing data")
			case "nested-target":
				target = filepath.Join(mirror, "private")
			case "already-paired":
				mirror = original.mirror
			case "dirty":
				writeFile(t, mirror, "untracked.txt", "work")
			case "merge":
				runGit(t, mirror, "checkout", "-qb", "side")
				runGit(t, mirror, "commit", "--allow-empty", "-qm", "side")
				runGit(t, mirror, "checkout", "-q", "main")
				runGit(t, mirror, "merge", "--no-ff", "-qm", "merge", "side")
			case "token-path":
				writeFile(t, mirror, "<PUBMIR:IP>.txt", "safe")
				commitAll(t, mirror, "token path")
			case "bad-config":
				cfg = filepath.Join(filepath.Dir(env), "config.yml")
				writeFile(t, filepath.Dir(env), "config.yml", "role: private\nexlcude: [secrets/**]\n")
			}
			head := runGit(t, mirror, "rev-parse", "HEAD")
			if _, err := syncengine.Restore(mirror, target, env, cfg); err == nil {
				t.Fatal("unsafe restore accepted")
			}
			if got := runGit(t, mirror, "rev-parse", "HEAD"); got != head {
				t.Fatal("mirror HEAD changed")
			}
			if scenario == "existing-target" {
				b, err := os.ReadFile(filepath.Join(target, "keep.txt"))
				if err != nil || string(b) != "existing data" {
					t.Fatal("existing data changed")
				}
			} else if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatal("failed restore published destination")
			}
			if scenario != "already-paired" {
				if _, err := os.Stat(filepath.Join(mirror, config.PubmirDirName)); !os.IsNotExist(err) {
					t.Fatal("failed restore left mirror metadata")
				}
			}
		})
	}
}

func TestRestoreWithPrivatePolicyAndExampleTokens(t *testing.T) {
	original, mirror, target, env := restoreFixture(t)
	writeFile(t, mirror, "examples.txt", "<PUBMIR:EXAMPLE>\n")
	commitAll(t, mirror, "example")
	cfg := filepath.Join(filepath.Dir(env), "private.yml")
	writeFile(t, filepath.Dir(env), "private.yml", "role: private\nexclude: [secrets/**]\nstub: ['*.cer']\nexample_tokens: [EXAMPLE]\n")
	if _, err := syncengine.Restore(mirror, target, env, cfg); err != nil {
		t.Fatal(err)
	}
	policy, err := config.Load(target)
	if err != nil || len(policy.Exclude) != 1 || len(policy.Stub) != 1 || len(policy.ExampleTokens) != 1 {
		t.Fatalf("policy lost: %+v, %v", policy, err)
	}
	if got := runGit(t, target, "show", "HEAD:examples.txt"); got != "<PUBMIR:EXAMPLE>\n" {
		t.Fatal("example token expanded")
	}
	if findings := checkMirror(t, pair{private: target, mirror: mirror}); len(findings) != 0 {
		t.Fatalf("check failed: %v (original %s)", findings, original.private)
	}
}
