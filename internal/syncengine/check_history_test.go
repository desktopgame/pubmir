package syncengine_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/desktopgame/pubmir/internal/leakcheck"
	"github.com/desktopgame/pubmir/internal/pairing"
)

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

func TestCheckProgress(t *testing.T) {
	p := setupPair(t)
	setSecrets(t, p.private, map[string]string{"IP": "203.0.113.42"})
	writeFile(t, p.mirror, "notes.txt", "203.0.113.42\n")
	commitAll(t, p.mirror, "leak")
	side, err := pairing.Resolve(p.mirror)
	if err != nil {
		t.Fatal(err)
	}
	want := checkMirror(t, p)
	var out bytes.Buffer
	got, err := leakcheck.Check(side.Self, side.Other, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress changed findings: got %v, want %v", got, want)
	}
	for _, stage := range []string{
		"loading secret rules", "checking working tree", "checked 1 working-tree files",
		"enumerating reachable history objects", "checking history paths", "checked 1 history snapshots",
		"checking excluded files", "checking forbidden secret files", "checking nested Git directories", "checking stubs",
	} {
		if !strings.Contains(out.String(), "Check: "+stage) {
			t.Fatalf("missing progress %q in %s", stage, out.String())
		}
	}
	var total int
	for _, line := range strings.Split(out.String(), "\n") {
		if n, _ := fmt.Sscanf(line, "Check: checking %d history blobs...", &total); n == 1 {
			break
		}
	}
	if total == 0 {
		t.Fatalf("missing history blob total in %s", out.String())
	}
	for i := 1; i <= total; i++ {
		if !strings.Contains(out.String(), fmt.Sprintf("Check: checked %d/%d history blobs\n", i, total)) {
			t.Fatalf("missing blob progress %d/%d in %s", i, total, out.String())
		}
	}
	if strings.Contains(out.String(), "203.0.113.42") {
		t.Fatal("progress exposed a secret value")
	}
}
