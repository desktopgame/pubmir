package cli

import (
	"flag"
	"fmt"
	"os"

	"pubmir/internal/syncengine"
)

func RunSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "skip the mirror -> private confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	report, err := syncengine.Run(cwd, syncengine.Options{Yes: *yes})
	if err != nil {
		return err
	}

	if report.NoOp {
		fmt.Println(report.Message)
		return nil
	}

	fmt.Printf("Synced %s (branch %s): %d commit(s) applied\n\n", report.Direction, report.Branch, len(report.Applied))
	for _, bc := range report.Applied {
		fmt.Printf("  %s -> %s  %s\n", shortSha(bc.SourceSha), shortSha(bc.TargetSha), bc.Message)
	}
	return nil
}
