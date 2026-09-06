package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/syncengine"
)

func RunRebuild(args []string) error {
	fs := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the confirmation prompt (safety checks still run)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	report, err := syncengine.Rebuild(cwd, syncengine.Options{Yes: *yes})
	if err != nil {
		return err
	}
	if report.Cancelled {
		fmt.Println("cancelled: the mirror repository was not modified")
		return nil
	}

	fmt.Printf("Mirror history rebuilt successfully (branch %s): %d commit(s)\n\n", report.Branch, len(report.Rebuilt))
	for _, bc := range report.Rebuilt {
		fmt.Printf("  %s -> %s  %s\n", gitrepo.ShortSha(bc.SourceSha), gitrepo.ShortSha(bc.TargetSha), bc.Message)
	}

	if report.HasRemote {
		fmt.Print("\nExisting remote history may still contain the old sanitized history.\n" +
			"If appropriate, update the remote manually with:\n\n" +
			"  git push --force-with-lease\n")
	}

	fmt.Print("\nImportant:\n" +
		"Rebuilding removes the value from the current mirror history,\n" +
		"but cannot guarantee removal from external copies.\n" +
		"If the leaked value was a credential, key, token, password,\n" +
		"or other revocable secret, rotate or revoke it.\n")
	return nil
}
