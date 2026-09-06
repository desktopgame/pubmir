package syncengine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRetiredKeyPair syncs with two secrets registered, then drops one from
// .pubmir.env while its value stays in the local history — the "I decided
// this doesn't need hiding after all" situation.
func setupRetiredKeyPair(t *testing.T) pair {
	t.Helper()
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{
		"DOMAIN": "example.internal",
		"IP":     "203.0.113.42",
	})
	writeFile(t, p.private, "notes.md", "host example.internal at 203.0.113.42\n")
	commitAll(t, p.private, "add notes")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	return p
}

func assertRetiredKeyError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error naming the retired key")
	}
	msg := err.Error()
	for _, want := range []string{"DOMAIN", "secrets-history.json", ".pubmir.env"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error should mention %q so the cause and the fix are obvious, got: %v", want, err)
		}
	}
	if strings.Contains(msg, "example.internal") {
		t.Fatalf("the error must not print the secret value itself: %v", err)
	}
}

func TestSyncExplainsRetiredSecretKey(t *testing.T) {
	p := setupRetiredKeyPair(t)
	mirrorBefore := headSha(t, p.mirror)

	writeFile(t, p.private, "notes.md", "host example.internal at 203.0.113.42 (updated)\n")
	commitAll(t, p.private, "update notes")

	_, err := sync(t, p.private, false)
	assertRetiredKeyError(t, err)
	if after := headSha(t, p.mirror); after != mirrorBefore {
		t.Fatalf("mirror changed despite the refusal: %s -> %s", mirrorBefore, after)
	}
}

func TestMirrorToPrivateSyncExplainsRetiredSecretKey(t *testing.T) {
	p := setupRetiredKeyPair(t)
	privateBefore := headSha(t, p.private)

	writeFile(t, p.mirror, "other.md", "AI edit\n")
	commitAll(t, p.mirror, "AI: add file")

	_, err := sync(t, p.mirror, true)
	assertRetiredKeyError(t, err)
	if after := headSha(t, p.private); after != privateBefore {
		t.Fatalf("private changed despite the refusal: %s -> %s", privateBefore, after)
	}
}

func TestRebuildExplainsRetiredSecretKey(t *testing.T) {
	p := setupRetiredKeyPair(t)
	mirrorBefore := headSha(t, p.mirror)

	_, err := rebuild(t, p.private, true)
	assertRetiredKeyError(t, err)
	if after := headSha(t, p.mirror); after != mirrorBefore {
		t.Fatalf("mirror changed despite the refusal: %s -> %s", mirrorBefore, after)
	}
}

func TestCheckReportsRetiredKeyInsteadOfUnknownTokenNoise(t *testing.T) {
	p := setupRetiredKeyPair(t)

	findings := checkMirror(t, p)
	if !hasFinding(findings, "retired-secret") {
		t.Fatalf("expected a retired-secret finding, got %+v", findings)
	}
	// The unresolvable tokens are a symptom of the retirement, so they must
	// not drown out the cause.
	if hasFinding(findings, "unknown-token") {
		t.Fatalf("unknown-token findings should be suppressed for the retired key, got %+v", findings)
	}
}

// Clearing the history entry is the documented way to say "this really is
// not a secret any more"; a rebuild then publishes the real value.
func TestRebuildAfterClearingHistoryPublishesValue(t *testing.T) {
	p := setupRetiredKeyPair(t)

	historyPath := filepath.Join(p.private, ".pubmir", "secrets-history.json")
	if err := os.WriteFile(historyPath, []byte(`{"values":{"IP":["203.0.113.42"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := rebuild(t, p.private, true); err != nil {
		t.Fatalf("rebuild should succeed once the history entry is gone: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(p.mirror, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "host example.internal at <PUBMIR:IP>\n"; string(got) != want {
		t.Fatalf("after rebuild = %q, want %q", got, want)
	}
	if findings := checkMirror(t, p); len(findings) != 0 {
		t.Fatalf("check should pass after the rebuild, got %+v", findings)
	}
}

// Restoring the key is the other valid answer, and must work without any
// manual history surgery.
func TestRestoringRetiredKeyResumesNormalOperation(t *testing.T) {
	p := setupRetiredKeyPair(t)

	setSecrets(t, p.private, map[string]string{
		"DOMAIN": "example.internal",
		"IP":     "203.0.113.42",
	})

	if _, err := rebuild(t, p.private, true); err != nil {
		t.Fatalf("rebuild should succeed once the key is restored: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(p.mirror, "notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "host <PUBMIR:DOMAIN> at <PUBMIR:IP>\n"; string(got) != want {
		t.Fatalf("after rebuild = %q, want %q", got, want)
	}
}
