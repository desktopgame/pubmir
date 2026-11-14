package syncengine

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/stub"
	"github.com/desktopgame/pubmir/internal/tokenize"
)

type RestoreReport struct {
	Root, Branch string
	Commits      int
	Stubs        []string
}

// Restore replays the current mirror branch into a new, independent private
// repository. Nothing containing restored values is written to the mirror.
// It requires an unpaired clone, and never replaces an existing destination.
func Restore(cwd, destination, envPath, configPath string, progress ...io.Writer) (*RestoreReport, error) {
	var out io.Writer = io.Discard
	if len(progress) > 0 && progress[0] != nil {
		out = progress[0]
	}
	mirror, err := gitrepo.Open(cwd)
	if err != nil {
		return nil, err
	}
	mc, err := config.Load(mirror.Root)
	if err != nil {
		return nil, err
	}
	if mc.Role != config.RoleMirror {
		return nil, fmt.Errorf("restore must be run from a mirror repository")
	}
	if _, err := os.Lstat(filepath.Join(mirror.Root, config.PubmirDirName)); !os.IsNotExist(err) {
		return nil, fmt.Errorf("mirror already has .pubmir metadata; use a fresh clone for restore")
	}
	clean, err := mirror.IsClean()
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, fmt.Errorf("mirror working tree must be clean before restore")
	}
	branch, err := mirror.CurrentBranch()
	if err != nil {
		return nil, err
	}
	head, exists, err := mirror.RevParseVerify("HEAD")
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("mirror has no commits to restore")
	}
	shas, err := mirror.RevList(head)
	if err != nil {
		return nil, err
	}
	if err := checkNoMergeCommits(mirror, shas); err != nil {
		return nil, err
	}

	destination, err = filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return nil, fmt.Errorf("restore destination must not exist: %s", destination)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return nil, fmt.Errorf("destination parent must exist: %w", err)
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	mirrorRoot, err := filepath.EvalSymlinks(mirror.Root)
	if err != nil {
		return nil, err
	}
	if destination == mirrorRoot || strings.HasPrefix(destination, mirrorRoot+string(filepath.Separator)) {
		return nil, fmt.Errorf("private destination must be outside the mirror")
	}
	envPath, err = filepath.EvalSymlinks(envPath)
	if err != nil {
		return nil, err
	}
	envPath, err = filepath.Abs(envPath)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(envPath, mirrorRoot+string(filepath.Separator)) {
		return nil, fmt.Errorf("--secrets file must be outside the mirror")
	}
	// A newly created local.yml must remain untracked without changing any
	// tracked mirror file. Standard pubmir mirrors already ignore .pubmir/.
	cmd := exec.Command("git", "check-ignore", "--no-index", "-q", "--", ".pubmir/local.yml")
	cmd.Dir = mirror.Root
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("mirror must gitignore .pubmir/ before restore")
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
	}
	current, err := secrets.LoadEnvFile(envPath)
	if err != nil {
		return nil, err
	}
	seenValues := map[string]string{}
	for key, value := range current {
		if value == "" {
			return nil, fmt.Errorf("secret %s has an empty value", key)
		}
		if other, ok := seenValues[value]; ok {
			return nil, fmt.Errorf("secrets %s and %s have the same value; use distinct values for reversible sync", key, other)
		}
		seenValues[value] = key
	}
	cfg := &config.Config{Role: config.RolePrivate}
	if configPath != "" {
		b, err := os.ReadFile(configPath)
		if err != nil {
			return nil, err
		}
		decoder := yaml.NewDecoder(bytes.NewReader(b))
		decoder.KnownFields(true)
		if err := decoder.Decode(cfg); err != nil {
			return nil, fmt.Errorf("invalid private config: %w", err)
		}
		if cfg.Role != config.RolePrivate {
			return nil, fmt.Errorf("--config must describe role: private")
		}
	}
	if err := secrets.ValidateExampleTokens(current, &secrets.History{}, cfg.ExampleTokens); err != nil {
		return nil, err
	}
	reserved := tokenize.ReservedSet(cfg.ExampleTokens)
	stubPaths := map[string]bool{}
	// Inspect all history before creating private. Old tokens also need a
	// new value. Placeholder paths become protected stubs in the new policy.
	fmt.Fprintf(out, "Restore: validating %d commits...\n", len(shas))
	for i, sha := range shas {
		commit, err := mirror.CommitInfo(sha)
		if err != nil {
			return nil, err
		}
		if _, err := tokenize.Detokenize([]byte(commit.Message), current, reserved); err != nil {
			return nil, fmt.Errorf("commit %s message: %w", sha, err)
		}
		entries, err := mirror.LsTreeRecursive(commit.Tree)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if slices.Contains(config.BookkeepingPaths, e.Path) {
				continue
			}
			if tokenize.IsExcluded(e.Path, config.ForcedExcludes) {
				return nil, fmt.Errorf("mirror contains forbidden private path %q", e.Path)
			}
			if !stub.IsRegularFileMode(e.Mode) {
				return nil, fmt.Errorf("restore does not support symlinks or submodules: %q", e.Path)
			}
			content, err := mirror.CatFileBlob(e.Sha)
			if err != nil {
				return nil, err
			}
			if bytes.Equal(content, stub.Content) {
				stubPaths[e.Path] = true
			}
			if _, err := tokenize.Detokenize(content, current, reserved); err != nil {
				return nil, fmt.Errorf("commit %s, path %q: %w", sha, e.Path, err)
			}
		}
		fmt.Fprintf(out, "Restore: validated %d/%d commits\n", i+1, len(shas))
	}
	var stubs []string
	for path := range stubPaths {
		stubs = append(stubs, path)
	}
	sort.Strings(stubs)
	for _, path := range stubs {
		// doublestar supports backslash escapes for glob metacharacters.
		literal := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[").Replace(path)
		if !tokenize.MatchesAny(path, cfg.Stub) {
			cfg.Stub = append(cfg.Stub, literal)
		}
	}

	staging, err := os.MkdirTemp(parent, ".pubmir-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	cmd = exec.Command("git", "init", "-q", "-b", branch, staging)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git init: %w: %s", err, out)
	}
	private, err := gitrepo.Open(staging)
	if err != nil {
		return nil, err
	}
	if err := cfg.Save(staging); err != nil {
		return nil, err
	}
	// Retain ordinary ignore rules from mirror, adding private-only entries.
	ignore, ok, err := mirror.LookupPath(head, config.GitignoreFileName)
	if err != nil {
		return nil, err
	}
	var ignoreContent []byte
	if ok {
		if !stub.IsRegularFileMode(ignore.Mode) {
			return nil, fmt.Errorf("mirror .gitignore must be a regular file")
		}
		ignoreContent, err = mirror.CatFileBlob(ignore.Sha)
		if err != nil {
			return nil, err
		}
	}
	ignoreContent = append(ignoreContent, []byte("\n# pubmir private\n.pubmir/\n.pubmir.env\n.pubmir.secrets\n")...)
	if err := os.WriteFile(filepath.Join(staging, config.GitignoreFileName), ignoreContent, 0600); err != nil {
		return nil, err
	}
	var carry []gitrepo.TreeEntry
	for _, path := range []string{config.FileName, config.GitignoreFileName} {
		b, err := os.ReadFile(filepath.Join(staging, path))
		if err != nil {
			return nil, err
		}
		id, err := private.HashObjectWrite(b)
		if err != nil {
			return nil, err
		}
		carry = append(carry, gitrepo.TreeEntry{Mode: "100644", Type: "blob", Sha: id, Path: path})
	}
	bs := &state.BranchState{}
	excludes := append(append([]string{}, config.ForcedExcludes...), cfg.Exclude...)
	// Copy placeholders into private as placeholders. The inferred policy
	// protects them afterward, until the user supplies the missing contents.
	fmt.Fprintf(out, "Restore: building %d private commits...\n", len(shas))
	built, err := buildMirrorToPrivate(mirror, private, shas, current, bs, carry, nil, nil, reserved, excludes, func(done, total int) {
		fmt.Fprintf(out, "Restore: built %d/%d private commits\n", done, total)
	})
	if err != nil {
		return nil, err
	}
	for _, bc := range built {
		bs.Record(bc.TargetSha, bc.SourceSha)
	}
	if err := private.UpdateRef("refs/heads/"+branch, bs.LastPrivateSha, gitrepo.ZeroSha); err != nil {
		return nil, err
	}
	if err := private.ResetHard(bs.LastPrivateSha); err != nil {
		return nil, err
	}
	if err := os.WriteFile(secrets.EnvPath(staging), data, 0600); err != nil {
		return nil, err
	}
	if _, _, err := secrets.LoadCurrentAndRecord(staging); err != nil {
		return nil, err
	}
	if err := (&config.Local{Pair: mirrorRoot}).Save(staging); err != nil {
		return nil, err
	}
	if err := (&state.State{Branches: map[string]*state.BranchState{branch: bs}}).Save(staging); err != nil {
		return nil, err
	}
	// Publish only after the complete history and local metadata are ready.
	latest, _, err := mirror.RevParseVerify("HEAD")
	if err != nil {
		return nil, err
	}
	clean, err = mirror.IsClean()
	if err != nil {
		return nil, err
	}
	if latest != head || !clean {
		return nil, fmt.Errorf("mirror changed during restore; retry")
	}
	metadata := filepath.Join(mirror.Root, config.PubmirDirName)
	if err := os.Mkdir(metadata, 0700); err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(metadata)
		}
	}()
	if err := (&config.Local{Pair: destination}).Save(mirror.Root); err != nil {
		return nil, err
	}
	if err := (&state.State{Branches: map[string]*state.BranchState{branch: bs}}).Save(mirror.Root); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return nil, fmt.Errorf("destination appeared during restore; refusing to overwrite it")
	}
	if err := os.Rename(staging, destination); err != nil {
		return nil, err
	}
	published = true
	return &RestoreReport{Root: destination, Branch: branch, Commits: len(built), Stubs: stubs}, nil
}
