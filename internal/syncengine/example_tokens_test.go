package syncengine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/desktopgame/pubmir/internal/config"
)

func setExampleTokens(t *testing.T, privateDir string, names []string) {
	t.Helper()
	writeConfigField(t, privateDir, func(c *config.Config) { c.ExampleTokens = names }, "configure example tokens")
}

// The reported bug: an AI writing documentation in mirror that mentions the
// token syntax itself (e.g. "don't guess the value of <PUBMIR:KEY>") could
// not be synced back to private at all, and — worse — a naive fix that just
// silently accepted the token would replace it with an empty string
// (current[key] on an undefined key), corrupting the very file that
// documents the boundary. Declaring the name via example_tokens must instead
// carry it through byte-for-byte in both directions.
func TestExampleTokenSurvivesMirrorToPrivateRoundTrip(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	setExampleTokens(t, p.private, []string{"KEY"})
	writeFile(t, p.private, "notes.md", "server at 203.0.113.42\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	const doc = "# Rules\n" +
		"- Do not guess or restore the real value of <PUBMIR:KEY>\n" +
		"- Real values render like <PUBMIR:IP>\n"
	writeFile(t, p.mirror, "CONTRIBUTING.md", doc)
	commitAll(t, p.mirror, "AI: add contributing guide")

	if _, err := sync(t, p.mirror, true); err != nil {
		t.Fatalf("mirror->private sync should succeed with KEY declared as an example token: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(p.private, "CONTRIBUTING.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Rules\n" +
		"- Do not guess or restore the real value of <PUBMIR:KEY>\n" +
		"- Real values render like 203.0.113.42\n"
	if string(got) != want {
		t.Fatalf("private content = %q, want %q (the example token must survive verbatim, not become empty)", got, want)
	}

	if findings := checkMirror(t, p); len(findings) != 0 {
		t.Fatalf("check should pass, got %+v", findings)
	}
}

// Without the declaration, the same document must still be rejected rather
// than silently corrupted — this is the safety net example_tokens opts out
// of, not a default.
func TestUndeclaredExampleTokenStillBlocksSync(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "notes.md", "seed\n")
	commitAll(t, p.private, "seed")
	if _, err := sync(t, p.private, false); err != nil {
		t.Fatal(err)
	}

	writeFile(t, p.mirror, "CONTRIBUTING.md", "do not guess <PUBMIR:KEY>\n")
	commitAll(t, p.mirror, "AI: add doc")

	if _, err := sync(t, p.mirror, true); err == nil {
		t.Fatal("an undeclared token must still block the sync")
	}
	if got, err := os.ReadFile(filepath.Join(p.private, "CONTRIBUTING.md")); err == nil {
		t.Fatalf("private must not receive a corrupted file, got %q", got)
	}
}

// A name cannot be both a real secret and a documentation example: pubmir
// cannot tell which list to trust, and guessing wrong means either leaking
// a real value or losing example text.
func TestExampleTokenCollidingWithRealSecretRejected(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	setExampleTokens(t, p.private, []string{"IP"})
	writeFile(t, p.private, "notes.md", "server at 203.0.113.42\n")
	commitAll(t, p.private, "collide")

	_, err := sync(t, p.private, false)
	if err == nil {
		t.Fatal("expected an error when example_tokens collides with a real secret key")
	}
	if !strings.Contains(err.Error(), "IP") {
		t.Fatalf("error should name the colliding key, got: %v", err)
	}
}

// A retired secret's name remains reserved by history even after its entry
// is removed from .pubmir.env, so declaring it as an example token later
// must be rejected the same way.
func TestExampleTokenCollidingWithRetiredSecretRejected(t *testing.T) {
	p := setupRetiredKeyPair(t) // DOMAIN was a real secret, now removed from .pubmir.env but still in history
	setExampleTokens(t, p.private, []string{"DOMAIN"})

	_, err := rebuild(t, p.private, true)
	if err == nil {
		t.Fatal("expected an error when example_tokens collides with a retired secret's history entry")
	}
	if !strings.Contains(err.Error(), "DOMAIN") {
		t.Fatalf("error should name the colliding key, got: %v", err)
	}
}

// A private-authored file that already mentions the token syntax (e.g. this
// very README) must not trip the private->mirror leak-check gate.
func TestExampleTokenInPrivateContentSyncsCleanly(t *testing.T) {
	p := setupPair(t)
	setExampleTokens(t, p.private, []string{"KEY"})
	writeFile(t, p.private, "README.md", "tokens look like <PUBMIR:KEY>\n")
	commitAll(t, p.private, "add readme")

	if _, err := sync(t, p.private, false); err != nil {
		t.Fatalf("private->mirror sync should not be blocked by a declared example token: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(p.mirror, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "tokens look like <PUBMIR:KEY>\n" {
		t.Fatalf("mirror content = %q", got)
	}
	if findings := checkMirror(t, p); len(findings) != 0 {
		t.Fatalf("check should pass, got %+v", findings)
	}
}
