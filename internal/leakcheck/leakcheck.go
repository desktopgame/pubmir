// Package leakcheck implements the checks behind `pubmir check` and the
// pre-ref-update safety gate that private->mirror sync runs before a single
// mirror ref is moved.
package leakcheck

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/pairing"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/tokenize"
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
	return scanBytes(content, mapper, currentKeys, nil)
}

// scanBytes is ScanBytesForLeaks with the option to stay quiet about tokens
// whose key is already reported through a more specific finding, so the
// output points at a cause instead of repeating its symptoms.
func scanBytes(content []byte, mapper *tokenize.Mapper, currentKeys map[string]string, explainedKeys map[string]bool) []Finding {
	var findings []Finding
	if key, ok := mapper.FindLeakingKey(string(content)); ok {
		findings = append(findings, Finding{Category: "content-leak", Detail: fmt.Sprintf("content still contains a value that should have become %s", key)})
	}
	for _, k := range tokenize.UnknownTokens(content, currentKeys) {
		if explainedKeys[k] {
			continue
		}
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

	// A key dropped from .pubmir.env while its values linger in the local
	// history produces tokens nothing can resolve. Report that cause once,
	// and suppress the per-occurrence unknown-token findings it explains.
	retired := secrets.RetiredKeys(current, history)
	explained := make(map[string]bool, len(retired))
	for _, k := range retired {
		explained[k] = true
		findings = append(findings, Finding{
			Category: "retired-secret",
			Detail: fmt.Sprintf("%s was removed from %s but its past values remain in %s, so <PUBMIR:%s> can no longer be resolved; "+
				"delete that entry from the history file if it is no longer secret, or restore it to %s",
				k, config.EnvFileName, config.HistoryFile, k, config.EnvFileName),
		})
	}

	// 2/3/8: working tree content, path, and generic-pattern scan.
	wtFindings, err := scanWorkingTree(mirror.Root, mapper, current, explained)
	if err != nil {
		return nil, err
	}
	findings = append(findings, wtFindings...)

	// 2/3: full history content and path scan.
	histFindings, err := scanHistory(mirror.Repo, mapper, current, explained)
	if err != nil {
		return nil, err
	}
	findings = append(findings, histFindings...)

	// 4: private's exclude patterns must not match anything present in mirror.
	excFindings, err := checkNoExcludedFiles(mirror.Root, private.Config.Exclude)
	if err != nil {
		return nil, err
	}
	findings = append(findings, excFindings...)

	// 5: secrets files must never exist in mirror, at any depth.
	secFindings, err := checkNoSecretsFiles(mirror.Root)
	if err != nil {
		return nil, err
	}
	findings = append(findings, secFindings...)

	// 6: no nested/foreign ".git" path other than mirror's own top-level one.
	gitFindings, err := checkNoForeignGitDir(mirror.Root)
	if err != nil {
		return nil, err
	}
	findings = append(findings, gitFindings...)

	// 7: every stubbed path must still hold exactly the placeholder, and
	// private's real blob for it must never have reached mirror's odb.
	stubFindings, err := checkStubs(mirror, private)
	if err != nil {
		return nil, err
	}
	findings = append(findings, stubFindings...)

	return findings, nil
}

// checkStubs verifies that each file private stubs is present in mirror
// holding exactly the generated placeholder, and that the private blob it
// stands in for never entered mirror's object database. Identical content
// hashes to an identical object id, so looking for private's own blob sha
// in mirror is a precise test for "the real file was copied across".
func checkStubs(mirror, private *pairing.Side) ([]Finding, error) {
	if len(private.Config.Stub) == 0 {
		return nil, nil
	}
	privateHead, hasHead, err := private.Repo.RevParseVerify("HEAD")
	if err != nil || !hasHead {
		return nil, err
	}
	privateEntries, err := private.Repo.LsTreeRecursive(privateHead + "^{tree}")
	if err != nil {
		return nil, err
	}

	var findings []Finding
	for _, e := range privateEntries {
		if !tokenize.MatchesAny(e.Path, private.Config.Stub) {
			continue
		}

		present, err := mirror.Repo.HasObject(e.Sha)
		if err != nil {
			return nil, err
		}
		if present {
			findings = append(findings, Finding{
				Category: "stub-content-leak",
				Detail:   fmt.Sprintf("%s: private's real blob for this stubbed file exists in mirror's object database", e.Path),
			})
		}

		content, err := os.ReadFile(filepath.Join(mirror.Root, filepath.FromSlash(e.Path)))
		if err != nil {
			if os.IsNotExist(err) {
				findings = append(findings, Finding{
					Category: "stub-missing",
					Detail:   fmt.Sprintf("%s is stubbed in private but missing from mirror", e.Path),
				})
				continue
			}
			return nil, err
		}
		if !bytes.Equal(content, stub.Content) {
			findings = append(findings, Finding{
				Category: "stub-modified",
				Detail:   fmt.Sprintf("%s does not hold the expected stub placeholder in mirror", e.Path),
			})
		}
	}
	return findings, nil
}

func scanWorkingTree(root string, mapper *tokenize.Mapper, currentKeys map[string]string, explainedKeys map[string]bool) ([]Finding, error) {
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
		// Bookkeeping files (.pubmir.yml, .gitignore, the bundled skill) are
		// pubmir's own trusted content, not data that passed through
		// tokenize/detokenize — the skill's documentation legitimately
		// contains example tokens like <PUBMIR:KEY> that are not real.
		if slices.Contains(config.BookkeepingPaths, rel) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		findings = append(findings, scanBytes(content, mapper, currentKeys, explainedKeys)...)
		findings = append(findings, scanGenericPatterns(content, rel)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

func scanHistory(repo *gitrepo.Repo, mapper *tokenize.Mapper, currentKeys map[string]string, explainedKeys map[string]bool) ([]Finding, error) {
	var findings []Finding

	bookkeepingBlobs, err := bookkeepingBlobShas(repo)
	if err != nil {
		return nil, err
	}

	paths, err := repo.AllTrackedPathsAllCommits()
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		if slices.Contains(config.BookkeepingPaths, p) {
			continue
		}
		if f := ScanPathForLeak(p, mapper); f != nil {
			findings = append(findings, *f)
		}
	}

	blobs, err := repo.RevListAllBlobs()
	if err != nil {
		return nil, err
	}
	for _, sha := range blobs {
		if bookkeepingBlobs[sha] {
			continue
		}
		content, err := repo.CatFileBlob(sha)
		if err != nil {
			return nil, err
		}
		for _, f := range scanBytes(content, mapper, currentKeys, explainedKeys) {
			findings = append(findings, Finding{Category: f.Category, Detail: fmt.Sprintf("blob %s: %s", sha, f.Detail)})
		}
	}
	return findings, nil
}

// bookkeepingBlobShas collects every blob sha that config.BookkeepingPaths
// has ever resolved to, across every commit reachable from any ref, so
// scanHistory can exempt pubmir's own bookkeeping content (which is not
// data that passed through tokenize/detokenize) from the content scan.
func bookkeepingBlobShas(repo *gitrepo.Repo) (map[string]bool, error) {
	commits, err := repo.RevList("--all")
	if err != nil {
		return nil, err
	}
	shas := map[string]bool{}
	for _, c := range commits {
		for _, p := range config.BookkeepingPaths {
			e, ok, err := repo.LookupPath(c, p)
			if err != nil {
				return nil, err
			}
			if ok {
				shas[e.Sha] = true
			}
		}
	}
	return shas, nil
}

// checkNoExcludedFiles flags files matching private's user-defined exclude
// patterns (e.g. secrets/**, *.key) that are nonetheless present in mirror.
// It deliberately does not use config.ForcedExcludes: those either denote
// pubmir's own local operating files (.pubmir/, which legitimately exists
// on disk in mirror too — it just never gets pushed since it is gitignored)
// or are covered by their own dedicated checks (secrets file presence,
// foreign .git dirs) elsewhere in this package.
// Every walk below propagates its errors rather than skipping the affected
// subtree: `pubmir check` reporting success is a statement that the whole
// mirror was inspected, so an unreadable directory must fail the check
// loudly instead of silently narrowing what was scanned.
func checkNoExcludedFiles(root string, exclude []string) ([]Finding, error) {
	var findings []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
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
	if err != nil {
		return nil, fmt.Errorf("scanning mirror for excluded files: %w", err)
	}
	return findings, nil
}

// checkNoSecretsFiles walks the whole tree rather than stat-ing the root:
// a secrets file is just as dangerous in a subdirectory, and the forced
// exclude patterns match at any depth too.
func checkNoSecretsFiles(root string) ([]Finding, error) {
	secretNames := []string{config.EnvFileName, config.AltSecretsFile}
	var findings []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" || rel == config.PubmirDirName {
				return filepath.SkipDir
			}
			return nil
		}
		if slices.Contains(secretNames, d.Name()) {
			findings = append(findings, Finding{Category: "secrets-file-present", Detail: fmt.Sprintf("%s must not exist in mirror", rel)})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning mirror for secrets files: %w", err)
	}
	return findings, nil
}

func checkNoForeignGitDir(root string) ([]Finding, error) {
	var findings []Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
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
	if err != nil {
		return nil, fmt.Errorf("scanning mirror for foreign .git paths: %w", err)
	}
	return findings, nil
}
