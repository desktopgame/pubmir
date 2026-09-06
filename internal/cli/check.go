package cli

import (
	"fmt"
	"os"
	"strings"

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
	historical := false
	for _, f := range findings {
		fmt.Printf("  - %s\n", f)
		if strings.HasPrefix(f.Detail, "blob ") {
			historical = true
		}
	}
	if historical {
		fmt.Print("\nHistorical leak detected: a past commit still carries the value, so\n" +
			"editing the working tree is not enough. After updating pubmir's rules\n" +
			"(.pubmir.env, exclude, stub), `pubmir rebuild` may be required.\n")
	}
	return fmt.Errorf("check failed with %d issue(s)", len(findings))
}
