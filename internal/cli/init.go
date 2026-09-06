package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/secrets"
	"github.com/desktopgame/pubmir/internal/skillasset"
	"github.com/desktopgame/pubmir/internal/state"
)

func RunInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	role := fs.String("role", "", "private or mirror")
	pair := fs.String("pair", "", "path to the paired repository")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *role != string(config.RolePrivate) && *role != string(config.RoleMirror) {
		return fmt.Errorf("--role must be %q or %q", config.RolePrivate, config.RoleMirror)
	}
	if *pair == "" {
		return fmt.Errorf("--pair <path> is required")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	repo, err := gitrepo.Open(cwd)
	if err != nil {
		return err
	}

	if config.Exists(repo.Root) {
		return fmt.Errorf("%s already exists; this repository is already pubmir-initialized", filepath.Join(repo.Root, config.FileName))
	}
	if config.LocalExists(repo.Root) {
		return fmt.Errorf("%s already exists", filepath.Join(repo.Root, config.PubmirDirName, config.LocalFileName))
	}

	roleVal := config.Role(*role)
	cfg := &config.Config{Role: roleVal, Exclude: config.DefaultExclude(), Stub: []string{}}
	if err := cfg.Save(repo.Root); err != nil {
		return err
	}

	local := &config.Local{Pair: *pair}
	if err := local.Save(repo.Root); err != nil {
		return err
	}

	if err := ensureGitignore(repo.Root, roleVal); err != nil {
		return err
	}

	addPaths := []string{config.FileName, config.GitignoreFileName}
	skillInstalled := false
	if roleVal == config.RoleMirror {
		skillInstalled, err = installMirrorSkill(repo.Root)
		if err != nil {
			return err
		}
		if skillInstalled {
			addPaths = append(addPaths, skillasset.PubmirMirrorSkillRelPath)
		}
	}

	// pubmir's own tracked scaffolding (.pubmir.yml, .gitignore, and on the
	// mirror side the bundled Claude Code skill) must be committed here so
	// the working tree starts clean — sync refuses to run against a dirty
	// mirror/private working tree, and would otherwise reject the very
	// first sync right after init.
	if err := repo.Add(addPaths...); err != nil {
		return err
	}
	staged, err := repo.HasStagedChanges()
	if err != nil {
		return err
	}
	if staged {
		if err := repo.Commit(fmt.Sprintf("pubmir: initialize as %s", roleVal)); err != nil {
			return err
		}
	}

	if roleVal == config.RolePrivate {
		if err := secrets.WriteTemplate(repo.Root); err != nil {
			return err
		}
	}

	st := &state.State{Branches: map[string]*state.BranchState{}}
	if err := st.Save(repo.Root); err != nil {
		return err
	}

	fmt.Printf("Initialized pubmir (%s) in %s\n", roleVal, repo.Root)
	fmt.Printf("Pair: %s\n", *pair)
	if roleVal == config.RolePrivate {
		fmt.Printf("Edit %s to add secret values, then run `pubmir sync`.\n", filepath.Join(repo.Root, config.EnvFileName))
	}
	if skillInstalled {
		fmt.Printf("Installed Claude Code skill: %s\n", skillasset.PubmirMirrorSkillRelPath)
	}
	return nil
}

// installMirrorSkill writes pubmir's bundled Claude Code skill into the
// mirror repository so it ships to collaborators (and AI agents) on
// `git clone`, per Claude Code's project-skill convention. It never
// overwrites an existing file — if one is already there (e.g. the human
// customized it), it is left untouched.
func installMirrorSkill(root string) (installed bool, err error) {
	path := filepath.Join(root, filepath.FromSlash(skillasset.PubmirMirrorSkillRelPath))
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, skillasset.PubmirMirrorSkillMD, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureGitignore appends any missing pubmir-managed entries to .gitignore
// without disturbing existing content.
func ensureGitignore(root string, role config.Role) error {
	lines := []string{config.PubmirDirName + "/"}
	if role == config.RolePrivate {
		lines = append([]string{config.EnvFileName, config.AltSecretsFile}, lines...)
	}

	path := filepath.Join(root, config.GitignoreFileName)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	existing := map[string]bool{}
	for l := range strings.SplitSeq(string(data), "\n") {
		existing[strings.TrimSpace(l)] = true
	}

	var toAdd []string
	for _, l := range lines {
		if !existing[l] {
			toAdd = append(toAdd, l)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	var buf bytes.Buffer
	buf.Write(data)
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.WriteString("\n# pubmir\n")
	for _, l := range toAdd {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
