package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/desktopgame/pubmir/internal/syncengine"
)

func RunRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	to := fs.String("to", "", "new private repository path (must not exist)")
	env := fs.String("secrets", "", "file containing new KEY=VALUE secret values")
	cfg := fs.String("config", "", "optional private .pubmir.yml with exclude, stub and example_tokens rules")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *to == "" || *env == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: pubmir restore --to <new-private-path> --secrets <env-file> [--config <private-yml>]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	report, err := syncengine.Restore(cwd, *to, *env, *cfg, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Printf("Restored private repository: %s\nBranch: %s\nRestored %d commit(s). Pair is ready for pubmir sync.\n", report.Root, report.Branch, report.Commits)
	if len(report.Stubs) > 0 {
		fmt.Println("Stub contents cannot be recovered. Detected placeholder paths (including history):")
		for _, path := range report.Stubs {
			fmt.Printf("  %s\n", path)
		}
	}
	fmt.Println("Excluded files cannot be recovered from the mirror; supply them separately.")
	return nil
}
