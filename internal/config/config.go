// Package config reads and writes pubmir's two configuration files:
// .pubmir.yml (git-tracked, portable: role + exclude) and .pubmir/local.yml
// (gitignored, machine-local: the pair path). Keeping these separate means
// cloning a repo to a new machine never silently carries over a stale local
// filesystem path.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/desktopgame/pubmir/internal/skillasset"
)

type Role string

const (
	RolePrivate Role = "private"
	RoleMirror  Role = "mirror"
)

const (
	FileName       = ".pubmir.yml"
	PubmirDirName  = ".pubmir"
	LocalFileName  = "local.yml"
	StateFileName  = "state.json"
	HistoryFile    = "secrets-history.json"
	EnvFileName    = ".pubmir.env"
	AltSecretsFile = ".pubmir.secrets"
)

const GitignoreFileName = ".gitignore"

// ForcedExcludes are patterns excluded from ordinary content sync regardless
// of user configuration: git internals, every secret-bearing local file
// pubmir itself manages, and each side's own pubmir bookkeeping files
// (.pubmir.yml/.gitignore are per-repository — private's role/exclude config
// must never overwrite mirror's, or vice versa; syncengine carries each
// side's own copies of these two forward across every synced commit
// instead).
// The secrets-file entries deliberately use a "**/" prefix: a bare literal
// pattern only matches at the repository root, so a stray file named
// .pubmir.env in any subdirectory would otherwise be copied into mirror.
var ForcedExcludes = []string{
	".git/**",
	"**/" + EnvFileName,
	"**/" + AltSecretsFile,
	PubmirDirName + "/**",
	FileName,
	GitignoreFileName,
}

// BookkeepingPaths are pubmir's own per-repository files that never flow
// through the private<->mirror tokenize/detokenize pipeline: each side
// keeps and carries forward its own copy independently (see
// syncengine.carryForwardEntries), and none of them are user data, so they
// must also be exempt from leak/token-validity scanning — the bundled
// skill's documentation text legitimately contains example tokens like
// <PUBMIR:KEY> that do not correspond to any real configured secret.
var BookkeepingPaths = []string{FileName, GitignoreFileName, skillasset.PubmirMirrorSkillRelPath}

// Config is the git-tracked half of a repository's pubmir configuration.
//
// Exclude and Stub describe what leaves the private repository, so only the
// private side's values are ever consulted. A mirror's file therefore holds
// its role and nothing else: presenting knobs there that pubmir silently
// ignores would invite someone to "protect" a path in the copy that has no
// say in the matter. Both fields are omitted when empty for that reason.
type Config struct {
	Role Role `yaml:"role"`
	// Exclude paths are kept out of mirror entirely. Private side only.
	Exclude []string `yaml:"exclude,omitempty"`
	// Stub paths keep their name and location in mirror but never their
	// content: mirror gets a fixed placeholder instead. Private side only,
	// and absent in older config files, where it means "no stubs".
	Stub []string `yaml:"stub,omitempty"`
	// ExampleTokens are <PUBMIR:KEY> names that a human has declared as
	// documentation examples rather than real secrets (e.g. a mirror-side
	// AI writing "don't guess the value of <PUBMIR:KEY>"). They are passed
	// through unchanged in both directions and never required to resolve —
	// without this, such a token blocks sync as an "unknown token" and, in
	// mirror->private, would otherwise be silently replaced with an empty
	// string (current[key] on an undefined key). Private side only, and
	// absent in older config files, where it means "no example tokens".
	ExampleTokens []string `yaml:"example_tokens,omitempty"`
}

type Local struct {
	Pair string `yaml:"pair"`
}

func DefaultExclude() []string {
	return []string{"secrets/**", "**/*.key", "**/*.pem"}
}

// Load reads .pubmir.yml from repoRoot.
func Load(repoRoot string) (*Config, error) {
	path := filepath.Join(repoRoot, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not found: run `pubmir init` first", path)
		}
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.Role != RolePrivate && c.Role != RoleMirror {
		return nil, fmt.Errorf("%s: role must be %q or %q, got %q", path, RolePrivate, RoleMirror, c.Role)
	}
	return &c, nil
}

// Exists reports whether .pubmir.yml already exists at repoRoot.
func Exists(repoRoot string) bool {
	_, err := os.Stat(filepath.Join(repoRoot, FileName))
	return err == nil
}

// Save writes .pubmir.yml, refusing to overwrite an existing file.
func (c *Config) Save(repoRoot string) error {
	path := filepath.Join(repoRoot, FileName)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func pubmirDir(repoRoot string) string {
	return filepath.Join(repoRoot, PubmirDirName)
}

// LoadLocal reads .pubmir/local.yml from repoRoot.
func LoadLocal(repoRoot string) (*Local, error) {
	path := filepath.Join(pubmirDir(repoRoot), LocalFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not found: pair repository is not configured, run `pubmir init --pair <path>`", path)
		}
		return nil, err
	}
	var l Local
	if err := yaml.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if l.Pair == "" {
		return nil, fmt.Errorf("%s: pair is empty", path)
	}
	return &l, nil
}

// LocalExists reports whether .pubmir/local.yml already exists at repoRoot.
func LocalExists(repoRoot string) bool {
	_, err := os.Stat(filepath.Join(pubmirDir(repoRoot), LocalFileName))
	return err == nil
}

// Save writes .pubmir/local.yml, refusing to overwrite an existing file.
func (l *Local) Save(repoRoot string) error {
	dir := pubmirDir(repoRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, LocalFileName)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	data, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ResolvePairRoot resolves the pair path (relative to repoRoot) to an
// absolute path.
func (l *Local) ResolvePairRoot(repoRoot string) (string, error) {
	p := l.Pair
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, nil
}
