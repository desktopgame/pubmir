// Package leakcheck implements the checks behind `pubmir check` and the
// pre-ref-update safety gate that private->mirror sync runs before a single
// mirror ref is moved.
package leakcheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"pubmir/internal/config"
	"pubmir/internal/gitrepo"
	"pubmir/internal/pairing"
	"pubmir/internal/secrets"
	"pubmir/internal/tokenize"
)

// Finding is one leak-check violation. Detail must never include the raw
// secret value itself, only identifying information (key name, path).
type Finding struct {
	Category string
	Detail   string
}

func (f Finding) String() string {
	return fmt.Sprintf("[%s] %s", f.Category, f.Detail)
}

// ScanBytesForLeaks checks already-sanitized content for (a) a known secret
// value that should have been tokenized but was not, and (b) a
// <PUBMIR:KEY> token whose KEY is not a currently-known secret key.
func ScanBytesForLeaks(content []byte, mapper *tokenize.Mapper, currentKeys map[string]string) []Finding {
	var findings []Finding
	if key, ok := mapper.FindLeakingKey(string(content)); ok {
		findings = append(findings, Finding{Category: "content-leak", Detail: fmt.Sprintf("content still contains a value that should have become %s", key)})
	}
	for _, k := range tokenize.UnknownTokens(content, currentKeys) {
		findings = append(findings, Finding{Category: "unknown-token", Detail: fmt.Sprintf("token <PUBMIR:%s> has no corresponding secret", k)})
	}
	return findings
}

// ScanPathForLeak checks a single path string for literal secret-value
// leakage (paths are never tokenized, so any occurrence here is a leak,
// not a missed substitution).
func ScanPathForLeak(path string, mapper *tokenize.Mapper) *Finding {
	if key, ok := mapper.FindLeakingKey(path); ok {
		return &Finding{Category: "path-leak", Detail: fmt.Sprintf("path %q contains a value mapped to %s", path, key)}
	}
	return nil
}

// scanGenericPatterns applies the heuristic generic-secret regexes to
// content, reporting the pattern matched (never the matched text itself).
func scanGenericPatterns(content []byte, path string) []Finding {
	var findings []Finding
	for _, re := range genericSecretPatterns {
		if re.Match(content) {
			findings = append(findings, Finding{Category: "generic-pattern", Detail: fmt.Sprintf("%s matches a common secret pattern (%s)", path, re.String())})
		}
	}
	return findings
}

// Check runs the full `pubmir check` suite against mirror, using private's
// secret values (current + history) as the ground truth for what must never
// appear in mirror.
func Check(mirror, private *pairing.Side) ([]Finding, error) {
	current, history, err := secrets.LoadCurrentAndRecord(private.Root)
	if err != nil {
		return nil, fmt.Errorf("loading private secrets: %w", err)
	}
	mapper := tokenize.NewMapper(current, history.Values)

	var findings []Finding

	// 2/3/8: working tree content, path, and generic-pattern scan.
	wtFindings, err := scanWorkingTree(mirror.Root, mapper, current)
	if err != nil {
		return nil, err
	}
	findings = append(findings, wtFindings...)

	// 2/3: full history content and path scan.
	histFindings, err := scanHistory(mirror.Repo, mapper, current)
	if err != nil {
		return nil, err
	}
	findings = append(findings, histFindings...)

	// 4: private's exclude patterns must not match anything present in mirror.
	findings = append(findings, checkNoExcludedFiles(mirror.Root, private.Config.Exclude)...)

	// 5: secrets files must never exist in mirror.
	findings = append(findings, checkNoSecretsFiles(mirror.Root)...)

	// 6: no nested/foreign ".git" path other than mirror's own top-level one.
	findings = append(findings, checkNoForeignGitDir(mirror.Root)...)

	return findings, nil
}

func scanWorkingTree(root string, mapper *tokenize.Mapper, currentKeys map[string]string) ([]Finding, error) {
	var findings []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if f := ScanPathForLeak(rel, mapper); f != nil {
			findings = append(findings, *f)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		findings = append(findings, ScanBytesForLeaks(content, mapper, currentKeys)...)
		findings = append(findings, scanGenericPatterns(content, rel)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

func scanHistory(repo *gitrepo.Repo, mapper *tokenize.Mapper, currentKeys map[string]string) ([]Finding, error) {
	var findings []Finding

	paths, err := repo.AllTrackedPathsAllCommits()
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if f := ScanPathForLeak(p, mapper); f != nil {
			findings = append(findings, *f)
		}
	}

	blobs, err := repo.RevListAllBlobs()
	if err != nil {
		return nil, err
	}
	for _, sha := range blobs {
		content, err := repo.CatFileBlob(sha)
		if err != nil {
			return nil, err
		}
		for _, f := range ScanBytesForLeaks(content, mapper, currentKeys) {
			findings = append(findings, Finding{Category: f.Category, Detail: fmt.Sprintf("blob %s: %s", sha, f.Detail)})
		}
	}
	return findings, nil
}

// checkNoExcludedFiles flags files matching private's user-defined exclude
// patterns (e.g. secrets/**, *.key) that are nonetheless present in mirror.
// It deliberately does not use config.ForcedExcludes: those either denote
// pubmir's own local operating files (.pubmir/, which legitimately exists
// on disk in mirror too — it just never gets pushed since it is gitignored)
// or are covered by their own dedicated checks (secrets file presence,
// foreign .git dirs) elsewhere in this package.
func checkNoExcludedFiles(root string, exclude []string) []Finding {
	var findings []Finding
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == config.PubmirDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if tokenize.IsExcluded(rel, exclude) {
			findings = append(findings, Finding{Category: "excluded-file-present", Detail: fmt.Sprintf("%s matches an exclude pattern but is present in mirror", rel)})
		}
		return nil
	})
	return findings
}

func checkNoSecretsFiles(root string) []Finding {
	var findings []Finding
	for _, name := range []string{config.EnvFileName, config.AltSecretsFile} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			findings = append(findings, Finding{Category: "secrets-file-present", Detail: fmt.Sprintf("%s must not exist in mirror", name)})
		}
	}
	return findings
}

func checkNoForeignGitDir(root string) []Finding {
	var findings []Finding
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".git" {
			findings = append(findings, Finding{Category: "nested-git", Detail: fmt.Sprintf("%s: a path named .git exists outside mirror's own top-level .git", rel)})
		}
		return nil
	})
	return findings
}
