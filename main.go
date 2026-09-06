// Command pubmir safely transports git commit history between a private
// repository containing secrets and a sanitized mirror repository. See
// init.md for the full specification.
package main

import (
	"fmt"
	"os"

	"github.com/desktopgame/pubmir/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = cli.RunInit(os.Args[2:])
	case "sync":
		err = cli.RunSync(os.Args[2:])
	case "status":
		err = cli.RunStatus(os.Args[2:])
	case "check":
		err = cli.RunCheck(os.Args[2:])
	case "rebuild":
		err = cli.RunRebuild(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "pubmir: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`pubmir - safely transport git history between a private repository and a sanitized mirror

Usage:
  pubmir init --role <private|mirror> --pair <path>
  pubmir sync [--yes]
  pubmir status
  pubmir check
  pubmir rebuild [--yes]   (private side only; regenerates the whole mirror history)`)
}
