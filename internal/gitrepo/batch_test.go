package gitrepo

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadBatchBlobs(t *testing.T) {
	var got []string
	err := readBatchBlobs(bufio.NewReader(strings.NewReader("one blob 4\na\x00\nb\ntwo blob 0\n\n")), []string{"one", "two"}, func(sha string, content []byte) error {
		got = append(got, sha+":"+string(content))
		return nil
	})
	if err != nil || !reflect.DeepEqual(got, []string{"one:a\x00\nb", "two:"}) {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, input := range []string{"", "one missing\n", "one tree 0\n\n", "other blob 0\n\n", "one blob -1\n", "one blob 2\nx", "one blob 1\nxy"} {
		if err := readBatchBlobs(bufio.NewReader(strings.NewReader(input)), []string{"one"}, func(string, []byte) error { return nil }); err == nil {
			t.Fatalf("accepted invalid batch output %q", input)
		}
	}
	want := errors.New("stop")
	if err := readBatchBlobs(bufio.NewReader(strings.NewReader("one blob 0\n\n")), []string{"one"}, func(string, []byte) error { return want }); !errors.Is(err, want) {
		t.Fatalf("callback error lost: %v", err)
	}
}

func TestBatchHistory(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
			return ""
		} else {
			return strings.TrimSpace(string(out))
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	git("config", "core.autocrlf", "false")
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("historical\x00content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "old")
	git("branch", "old")
	git("mv", "old.txt", "new.txt")
	git("commit", "-qm", "rename")
	git("commit", "--allow-empty", "-qm", "same tree")
	repo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	if err := repo.VisitHistoryTrees(func(entries []TreeEntry) error {
		for _, e := range entries {
			paths = append(paths, e.Path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"new.txt", "old.txt"}) {
		t.Fatalf("expected newest first, unique trees and historical paths: %q", paths)
	}
	blobs, err := repo.RevListAllBlobs()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := repo.VisitBlobs(blobs, func(_ string, content []byte) error {
		count++
		if string(content) != "historical\x00content\n" {
			t.Fatalf("blob changed: %q", content)
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatalf("expected one unique blob: count=%d, err=%v", count, err)
	}
	if err := repo.VisitBlobs([]string{strings.Repeat("0", 40)}, func(string, []byte) error { return nil }); err == nil {
		t.Fatal("missing object accepted")
	}
}
