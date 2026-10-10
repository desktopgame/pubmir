package syncengine_test

import "testing"

// Starting at the newest tree must still inspect deleted paths and blobs,
// including history reachable only through another branch.
func TestCheckFindsLeaksOnOtherBranch(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.mirror, "safe.txt", "safe\n")
	commitAll(t, p.mirror, "safe main")
	runGit(t, p.mirror, "checkout", "-qb", "old-leak")
	writeFile(t, p.mirror, "203.0.113.42.txt", "203.0.113.42\n")
	commitAll(t, p.mirror, "leak")
	runGit(t, p.mirror, "rm", "203.0.113.42.txt")
	commitAll(t, p.mirror, "remove leak from latest snapshot")
	runGit(t, p.mirror, "checkout", "-q", "main")
	findings := checkMirror(t, p)
	for _, category := range []string{"path-leak", "content-leak"} {
		if !hasFinding(findings, category) {
			t.Fatalf("missing historical %s finding: %v", category, findings)
		}
	}
}
