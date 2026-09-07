// Package secrets loads the private side's .pubmir.env file and maintains a
// local, gitignored history of every value ever observed per key so that
// rotating a secret does not cause an unsynced old commit (still containing
// the pre-rotation value) to leak into the mirror.
package secrets

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/desktopgame/pubmir/internal/config"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// EnvPath returns the path to the private secrets file.
func EnvPath(repoRoot string) string {
	return filepath.Join(repoRoot, config.EnvFileName)
}

// LoadEnv parses .pubmir.env as KEY=VALUE lines. Blank lines and lines
// starting with # are ignored. It is not an error for the file to be
// missing (returns an empty map) so a freshly-initialized repo still works.
func LoadEnv(repoRoot string) (map[string]string, error) {
	path := EnvPath(repoRoot)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rawKey, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, lineNo, line)
		}
		key := strings.TrimSpace(rawKey)
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("%s:%d: invalid key %q (must match %s)", path, lineNo, key, keyPattern.String())
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// WriteTemplate creates a starter .pubmir.env if one does not already exist.
func WriteTemplate(repoRoot string) error {
	path := EnvPath(repoRoot)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	const template = "# pubmir secret values (KEY=VALUE). Never commit this file.\n" +
		"# Each KEY becomes the mirror token <PUBMIR:KEY>.\n" +
		"# VPS_PUBLIC_IP=203.0.113.42\n" +
		"# VPS_USER=actual-user\n"
	return os.WriteFile(path, []byte(template), 0o600)
}

// History is the set of every value pubmir has ever observed for each
// secret key. It is a best-effort log limited to what pubmir has actually
// seen: values changed or removed without pubmir ever loading .pubmir.env
// in between are not recoverable from it, and losing the history file loses
// that record entirely. It is never a substitute for a complete audit
// trail of secret values.
type History struct {
	Values map[string][]string `json:"values"`
}

func historyPath(repoRoot string) string {
	return filepath.Join(repoRoot, config.PubmirDirName, config.HistoryFile)
}

// LoadHistory reads .pubmir/secrets-history.json, returning an empty History
// if it does not exist yet.
func LoadHistory(repoRoot string) (*History, error) {
	data, err := os.ReadFile(historyPath(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return &History{Values: map[string][]string{}}, nil
		}
		return nil, err
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, err
	}
	if h.Values == nil {
		h.Values = map[string][]string{}
	}
	return &h, nil
}

// Save writes the history back to .pubmir/secrets-history.json.
func (h *History) Save(repoRoot string) error {
	dir := filepath.Join(repoRoot, config.PubmirDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(historyPath(repoRoot), data, 0o600)
}

// Record appends any new-or-changed values from current into the history,
// keyed by KEY. Existing entries are never removed. Returns whether the
// history changed (and so needs saving).
func (h *History) Record(current map[string]string) bool {
	changed := false
	keys := make([]string, 0, len(current))
	for k := range current {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := current[k]
		existing := h.Values[k]
		if !slices.Contains(existing, v) {
			h.Values[k] = append(existing, v)
			changed = true
		}
	}
	return changed
}

// AllValues returns every value ever recorded, across all keys, flattened.
func (h *History) AllValues() []string {
	var all []string
	for _, vs := range h.Values {
		all = append(all, vs...)
	}
	return all
}

// ValuesForKey returns every historical value for key, including one not
// present if key is unknown.
func (h *History) ValuesForKey(key string) []string {
	return h.Values[key]
}

// RetiredKeys returns, sorted, the keys the local history still remembers
// that are no longer defined in .pubmir.env.
//
// pubmir cannot tell on its own whether such a key was retired on purpose
// ("this value is not secret after all") or deleted by accident, and the two
// call for opposite handling — emitting the real value versus continuing to
// hide it. Callers therefore stop and ask rather than guessing, because
// guessing wrong means publishing a secret.
func RetiredKeys(current map[string]string, h *History) []string {
	var keys []string
	for k := range h.Values {
		if _, ok := current[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// RetiredKeysError explains the situation RetiredKeys detects and spells out
// both ways forward. It never prints the values themselves.
func RetiredKeysError(repoRoot string, keys []string) error {
	return fmt.Errorf("secret key(s) %s were removed from %s but their past values are still recorded in %s.\n\n"+
		"pubmir would keep replacing those values with <PUBMIR:KEY> tokens that nothing can resolve any more, "+
		"which blocks sync, rebuild and check.\n\n"+
		"If they are genuinely no longer secret: delete those entries from %s, then run `pubmir rebuild` to "+
		"regenerate the mirror without the tokens.\n"+
		"If they were removed by mistake: restore them to %s.",
		strings.Join(keys, ", "), config.EnvFileName, historyPath(repoRoot), config.HistoryFile, config.EnvFileName)
}

// ValidateExampleTokens rejects any name in exampleTokens (from
// .pubmir.yml's example_tokens) that is also a real secret key — currently
// defined, or only remembered in history from before a rotation. Letting
// such a name through would be ambiguous in exactly the way that matters:
// whether a given <PUBMIR:NAME> is real secret data or a documentation
// example depends on which list you trust.
func ValidateExampleTokens(current map[string]string, h *History, exampleTokens []string) error {
	for _, name := range exampleTokens {
		if _, ok := current[name]; ok {
			return fmt.Errorf("%s is declared as an example_token in .pubmir.yml but is also a real key in %s; rename one of them", name, config.EnvFileName)
		}
		if _, ok := h.Values[name]; ok {
			return fmt.Errorf("%s is declared as an example_token in .pubmir.yml but also appears in %s (a past secret value); rename one of them", name, config.HistoryFile)
		}
	}
	return nil
}

// LoadCurrentAndRecord loads .pubmir.env and records its values into the
// local secrets history, saving the history if it changed. This is the
// single place .pubmir.env should be read from so history-tracking happens
// automatically regardless of which command triggered the read.
func LoadCurrentAndRecord(repoRoot string) (current map[string]string, history *History, err error) {
	current, err = LoadEnv(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	history, err = LoadHistory(repoRoot)
	if err != nil {
		return nil, nil, err
	}
	if history.Record(current) {
		if err := history.Save(repoRoot); err != nil {
			return nil, nil, err
		}
	}
	return current, history, nil
}
