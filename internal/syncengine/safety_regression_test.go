package syncengine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExcludedFilesSurviveRoundtrip(t *testing.T) {
	p := setupPair(t)
	writeFile(t, p.private, "secrets/local.txt", "private-only")
	writeFile(t, p.private, "doc.txt", "original")
	writeFile(t, p.private, "obsolete.txt", "remove through mirror")
	commitAll(t, p.private, "content")
	if _, err := sync(t, p.private, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.mirror, "doc.txt", "edited")
	runGit(t, p.mirror, "rm", "obsolete.txt")
	commitAll(t, p.mirror, "edit")
	if _, err := sync(t, p.mirror, true); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(p.private, "secrets/local.txt"))
	if err != nil || string(content) != "private-only" {
		t.Fatalf("excluded private file changed: %q, %v", content, err)
	}
	if got := runGit(t, p.private, "show", "HEAD:secrets/local.txt"); got != "private-only" {
		t.Fatalf("excluded file missing from commit: %q", got)
	}
	if got := runGit(t, p.private, "show", "HEAD:doc.txt"); got != "edited" {
		t.Fatalf("mirror edit missing: %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.private, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("ordinary mirror deletion was not applied: %v", err)
	}
}

func TestPrivateToMirrorRejectsUnknownTokenInMessageBody(t *testing.T) {
	p := setupPair(t)
	oldHead := runGit(t, p.mirror, "rev-parse", "HEAD")
	runGit(t, p.private, "commit", "--allow-empty", "-qm", "safe subject\n\n<PUBMIR:UNDEFINED_SECRET>")
	if _, err := sync(t, p.private, true); err == nil || !strings.Contains(err.Error(), "message: token <PUBMIR:UNDEFINED_SECRET>") {
		t.Fatalf("message body was not checked: %v", err)
	}
	if got := runGit(t, p.mirror, "rev-parse", "HEAD"); got != oldHead {
		t.Fatal("mirror HEAD moved despite invalid message")
	}
}

func TestCheckFindsGenericSecretInHistoricalMessage(t *testing.T) {
	p := setupPair(t)
	runGit(t, p.mirror, "commit", "--allow-empty", "-qm", "safe subject\n\n-----BEGIN PRIVATE KEY-----")
	runGit(t, p.mirror, "commit", "--allow-empty", "-qm", "safe latest commit")
	if findings := checkMirror(t, p); !hasFinding(findings, "generic-pattern") {
		t.Fatalf("missed unregistered secret in historical message: %v", findings)
	}
}

func TestCheckFindsHistoricalMessageBodyOnOtherBranch(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"SECRET": "unique-sensitive-review-value"})
	writeFile(t, p.private, "doc.txt", "original")
	commitAll(t, p.private, "content")
	if _, err := sync(t, p.private, true); err != nil {
		t.Fatal(err)
	}
	runGit(t, p.mirror, "checkout", "-qb", "message-leak")
	runGit(t, p.mirror, "commit", "--allow-empty", "-qm", "innocent subject\n\nunique-sensitive-review-value")
	runGit(t, p.mirror, "commit", "--allow-empty", "-qm", "later safe message")
	runGit(t, p.mirror, "checkout", "-q", "main")
	findings := checkMirror(t, p)
	if !hasFinding(findings, "content-leak") {
		t.Fatalf("check missed secret in historical message: %v", findings)
	}
	for _, f := range findings {
		if strings.Contains(f.Detail, "unique-sensitive-review-value") {
			t.Fatal("finding exposed secret")
		}
	}
}

func TestExcludedFilesCannotBeOverwrittenFromMirror(t *testing.T) {
	p := setupPair(t)
	setStubPatterns(t, p.private, []string{"secrets/**"})
	writeFile(t, p.private, "secrets/local.txt", "private-only")
	commitAll(t, p.private, "private content")
	if _, err := sync(t, p.private, true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, p.mirror, "secrets/local.txt", "mirror replacement")
	commitAll(t, p.mirror, "attempt replacement")
	if _, err := sync(t, p.mirror, true); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, p.private, "show", "HEAD:secrets/local.txt"); got != "private-only" {
		t.Fatalf("excluded file overwritten: %q", got)
	}
}
