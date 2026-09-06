package cli

import (
	"fmt"
	"os"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/leakcheck"
	"github.com/desktopgame/pubmir/internal/pairing"
)

func RunCheck(args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	pair, err := pairing.Resolve(cwd)
	if err != nil {
		return err
	}

	var privateSide, mirrorSide *pairing.Side
	if pair.Self.Config.Role == config.RolePrivate {
		privateSide, mirrorSide = pair.Self, pair.Other
	} else {
		privateSide, mirrorSide = pair.Other, pair.Self
	}

	findings, err := leakcheck.Check(mirrorSide, privateSide)
	if err != nil {
		return err
	}

	if len(findings) == 0 {
		fmt.Println("OK: no leaks detected in mirror repository")
		return nil
	}

	fmt.Printf("FAIL: %d issue(s) found in mirror repository:\n\n", len(findings))
	for _, f := range findings {
		fmt.Printf("  - %s\n", f)
	}
	return fmt.Errorf("check failed with %d issue(s)", len(findings))
}
