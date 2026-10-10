package gitrepo

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// VisitBlobs reads blobs with one Git process, retaining only one blob's
// contents at a time. Missing objects, wrong types and truncated output fail.
func (r *Repo) VisitBlobs(shas []string, visit func(string, []byte) error) error {
	if len(shas) == 0 {
		return nil
	}
	cmd := exec.Command("git", "--git-dir="+r.GitDir, "--work-tree="+r.Root, "cat-file", "--batch")
	cmd.Dir = r.Root
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	reader := bufio.NewReader(stdout)
	readErr := readBatchBlobs(reader, shas, visit)
	if readErr != nil {
		// The child may still be writing; stop it before waiting on failure.
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		return fmt.Errorf("git cat-file --batch: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func readBatchBlobs(reader *bufio.Reader, shas []string, visit func(string, []byte) error) error {
	for _, sha := range shas {
		header, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading blob %s header: %w", sha, err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != sha || fields[1] != "blob" {
			return fmt.Errorf("unexpected git cat-file header for blob %s", sha)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return fmt.Errorf("invalid size for blob %s", sha)
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			return fmt.Errorf("reading blob %s: %w", sha, err)
		}
		if end, err := reader.ReadByte(); err != nil || end != '\n' {
			return fmt.Errorf("missing terminator for blob %s", sha)
		}
		if err := visit(sha, content); err != nil {
			return err
		}
	}
	return nil
}

// VisitHistoryTrees visits distinct root trees, starting at the newest tips
// and walking toward their parents, across all refs. Unchanged trees need
// only one read, even when many commits have the same snapshot.
func (r *Repo) VisitHistoryTrees(visit func([]TreeEntry) error) error {
	out, err := r.run(nil, "log", "--all", "--topo-order", "--format=%T")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, tree := range splitLines(out) {
		if seen[tree] {
			continue
		}
		seen[tree] = true
		entries, err := r.LsTreeRecursive(tree)
		if err != nil {
			return err
		}
		if err := visit(entries); err != nil {
			return err
		}
	}
	return nil
}
