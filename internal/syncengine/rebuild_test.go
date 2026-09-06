package syncengine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/syncengine"
)

func rebuild(t *testing.T, dir string, yes bool) (*syncengine.RebuildReport, error) {
	t.Helper()
	return syncengine.Rebuild(dir, syncengine.Options{Yes: yes})
}

// mirrorBlobExists reports whether content with exactly these bytes is
// present anywhere in mirror's object database.
func mirrorHasContent(t *testing.T, mirrorDir, content string) bool {
	t.Helper()
	// git hash-object computes the id without writing, so this asks "would
	// that content already be here".
	sha := strings.TrimSpace(runGitStdin(t, mirrorDir, content, "hash-object", "--stdin"))
	return gitSucceeds(t, mirrorDir, "cat-file", "-e", sha+"^{object}")
}

func TestRebuildReplacesEntireHistory(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.private, "a.txt", "first\n")
	commitAll(t, p.private, "A")
	writeFile(t, p.private, "b.txt", "second\n")
	commitAll(t, p.private, "B")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}
	before := headSha(t, p.mirror)

	oldShas := strings.Fields(runGit(t, p.mirror, "rev-list", "HEAD"))

	// Changing a rule is what makes the rebuilt objects genuinely different:
	// every commit is regenerated, so every mirror SHA on the branch moves.
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42", "WORD": "second"})

	report, err := rebuild(t, p.private, true)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if len(report.Rebuilt) != len(oldShas) {
		t.Fatalf("rebuilt %d commits, want one per private commit (%d)", len(report.Rebuilt), len(oldShas))
	}

	after := headSha(t, p.mirror)
	if after == before {
		t.Fatalf("mirror tip did not change after a rule change (still %s)", before)
	}
	// The old tip must no longer be part of the branch: its history was
	// replaced, not extended. (Commits whose rebuilt content is byte-identical
	// keep their SHA — object construction is deterministic — so only the
	// commits the rule actually affects are expected to move.)
	if gitSucceeds(t, p.mirror, "merge-base", "--is-ancestor", before, after) {
		t.Fatalf("old tip %s is still an ancestor of %s; history was extended, not rebuilt", before, after)
	}

	// The new rule is applied across the whole history, not just the tip.
	if got, _ := os.ReadFile(filepath.Join(p.mirror, "a.txt")); string(got) != "first\n" {
		t.Fatalf("a.txt after rebuild = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(p.mirror, "b.txt")); string(got) != "<PUBMIR:WORD>\n" {
		t.Fatalf("b.txt after rebuild = %q, want the newly registered value tokenized", got)
	}
	if clean := runGit(t, p.mirror, "status", "--porcelain"); strings.TrimSpace(clean) != "" {
		t.Fatalf("mirror not clean after rebuild:\n%s", clean)
	}
}

// The core use case: a value that was not registered as a secret leaked into
// mirror history; registering it and rebuilding must purge it.
func TestRebuildPurgesHistoricalSecret(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "notes.txt", "connect to 203.0.113.42 now\n")
	commitAll(t, p.private, "add notes")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	if !mirrorHasContent(t, p.mirror, "connect to 203.0.113.42 now\n") {
		t.Fatal("precondition: the unregistered value should have leaked into mirror")
	}

	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	if _, err := rebuild(t, p.private, true); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(p.mirror, "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "connect to <PUBMIR:IP> now\n"; string(got) != want {
		t.Fatalf("after rebuild = %q, want %q", got, want)
	}
	if leaked := runGit(t, p.mirror, "log", "--all", "-S", "203.0.113.42", "--oneline"); strings.TrimSpace(leaked) != "" {
		t.Fatalf("the value is still reachable in mirror history:\n%s", leaked)
	}
	if findings := checkMirror(t, p); len(findings) != 0 {
		t.Fatalf("check should pass after rebuild, got %+v", findings)
	}
}

func TestRebuildAppliesNewStubRuleToHistory(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "config/production.yml", realConfig)
	commitAll(t, p.private, "add config")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}
	if !mirrorHasContent(t, p.mirror, realConfig) {
		t.Fatal("precondition: real config should be in mirror before stubbing")
	}

	setStubPatterns(t, p.private, []string{"config/production.yml"})
	if _, err := rebuild(t, p.private, true); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(p.mirror, "config/production.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(stub.Content) {
		t.Fatalf("expected the placeholder after rebuild, got %q", got)
	}
	// The old real content must no longer be reachable from any ref.
	if out := runGit(t, p.mirror, "log", "--all", "-S", "hunter2", "--oneline"); strings.TrimSpace(out) != "" {
		t.Fatalf("private content still reachable in mirror history:\n%s", out)
	}
}

func TestRebuildAppliesNewExcludeRuleToHistory(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "internal/private.txt", "internal only\n")
	commitAll(t, p.private, "add internal file")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.mirror, "internal/private.txt")); err != nil {
		t.Fatalf("precondition: file should be in mirror before excluding: %v", err)
	}

	setExcludePatterns(t, p.private, []string{"internal/**"})
	if _, err := rebuild(t, p.private, true); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if _, err := os.Stat(filepath.Join(p.mirror, "internal/private.txt")); !os.IsNotExist(err) {
		t.Fatalf("excluded path should be gone from mirror (err=%v)", err)
	}
	if out := runGit(t, p.mirror, "log", "--all", "--", "internal/private.txt"); strings.TrimSpace(out) != "" {
		t.Fatalf("excluded path still present in rebuilt history:\n%s", out)
	}
}

func TestRebuildRefusesToDiscardUnsyncedMirrorCommits(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	writeFile(t, p.mirror, "a.txt", "AI work not yet synced back\n")
	commitAll(t, p.mirror, "AI: important work")
	mirrorBefore := headSha(t, p.mirror)

	_, err := rebuild(t, p.private, true)
	if err == nil {
		t.Fatal("rebuild must refuse while mirror holds unsynchronized commits")
	}
	if !strings.Contains(err.Error(), "not been synchronized") {
		t.Fatalf("unexpected error: %v", err)
	}
	if after := headSha(t, p.mirror); after != mirrorBefore {
		t.Fatalf("mirror changed despite the refusal: %s -> %s", mirrorBefore, after)
	}
	if got, _ := os.ReadFile(filepath.Join(p.mirror, "a.txt")); string(got) != "AI work not yet synced back\n" {
		t.Fatalf("the AI's commit must survive, got %q", got)
	}
}

func TestRebuildRefusedFromMirrorSide(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	_, err := rebuild(t, p.mirror, true)
	if err == nil {
		t.Fatal("rebuild must refuse to run from the mirror side")
	}
	if !strings.Contains(err.Error(), "must be run from the private repository") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRebuildRejectsMergeCommits(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	runGit(t, p.private, "checkout", "-q", "-b", "feature")
	writeFile(t, p.private, "b.txt", "feature\n")
	commitAll(t, p.private, "feature work")
	runGit(t, p.private, "checkout", "-q", "main")
	runGit(t, p.private, "merge", "-q", "--no-ff", "feature", "-m", "merge feature")

	if _, err := rebuild(t, p.private, true); err == nil {
		t.Fatal("rebuild must refuse when private history contains a merge commit")
	}
}

// A leak-check failure during rebuild must leave the mirror exactly as it
// was: ref, working tree and recorded mapping all unchanged.
func TestRebuildFailureIsAtomic(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}
	mirrorBefore := headSha(t, p.mirror)
	stateBefore, err := os.ReadFile(filepath.Join(p.private, ".pubmir", "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	// A path carrying a secret value fails the build before any ref moves.
	writeFile(t, p.private, "203.0.113.42/oops.txt", "content\n")
	commitAll(t, p.private, "path leak")

	if _, err := rebuild(t, p.private, true); err == nil {
		t.Fatal("expected rebuild to fail on the leaking path")
	}
	if after := headSha(t, p.mirror); after != mirrorBefore {
		t.Fatalf("mirror ref moved despite failure: %s -> %s", mirrorBefore, after)
	}
	if clean := runGit(t, p.mirror, "status", "--porcelain"); strings.TrimSpace(clean) != "" {
		t.Fatalf("mirror working tree changed despite failure:\n%s", clean)
	}
	stateAfter, err := os.ReadFile(filepath.Join(p.private, ".pubmir", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stateAfter) != string(stateBefore) {
		t.Fatal("state mapping changed despite failure")
	}
}

func TestRebuildReplacesMapping(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	// Change a rule so the rebuilt objects genuinely differ from the old ones.
	setSecrets(t, p.private, map[string]string{"WORD": "seed"})
	report, err := rebuild(t, p.private, true)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(p.private, ".pubmir", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	stateText := string(raw)

	privateCount := len(strings.Split(strings.TrimSpace(runGit(t, p.private, "rev-list", "HEAD")), "\n"))
	for _, bc := range report.Rebuilt {
		if !strings.Contains(stateText, bc.TargetSha) {
			t.Fatalf("rebuilt mirror sha %s missing from the mapping", bc.TargetSha)
		}
	}
	if got := strings.Count(stateText, `"private"`); got != privateCount {
		t.Fatalf("mapping has %d entries, want one per private commit (%d)", got, privateCount)
	}

	// Both sides must agree.
	mirrorRaw, err := os.ReadFile(filepath.Join(p.mirror, ".pubmir", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mirrorRaw) != stateText {
		t.Fatal("private and mirror state files diverged after rebuild")
	}
}

func TestRebuildDoesNotPush(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	// A remote that would fail loudly if pubmir ever tried to push to it.
	runGit(t, p.mirror, "remote", "add", "origin", filepath.Join(p.mirror, "does-not-exist.git"))

	report, err := rebuild(t, p.private, true)
	if err != nil {
		t.Fatalf("rebuild must succeed without touching the remote: %v", err)
	}
	if !report.HasRemote {
		t.Fatal("expected the report to note that a remote is configured")
	}
	if out := runGit(t, p.mirror, "branch", "-r"); strings.TrimSpace(out) != "" {
		t.Fatalf("nothing should have been pushed, but remote-tracking refs exist:\n%s", out)
	}
}

func TestRebuildCancelledLeavesMirrorUntouched(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "a.txt", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}
	before := headSha(t, p.mirror)

	var out strings.Builder
	report, err := syncengine.Rebuild(p.private, syncengine.Options{
		Stdin:  strings.NewReader("n\n"),
		Stdout: &out,
	})
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !report.Cancelled {
		t.Fatal("answering 'n' must cancel the rebuild")
	}
	if !strings.Contains(out.String(), "All mirror commit SHAs on this branch will change") {
		t.Fatalf("expected the warning to be shown, got:\n%s", out.String())
	}
	if after := headSha(t, p.mirror); after != before {
		t.Fatalf("mirror changed despite cancellation: %s -> %s", before, after)
	}
}
