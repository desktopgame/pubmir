package cli

import (
	"fmt"
	"os"

	"github.com/desktopgame/pubmir/internal/config"
	"github.com/desktopgame/pubmir/internal/gitrepo"
	"github.com/desktopgame/pubmir/internal/pairing"
	"github.com/desktopgame/pubmir/internal/state"
	"github.com/desktopgame/pubmir/internal/tokenize"
)

func RunStatus(args []string) error {
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

	fmt.Printf("Role: %s\nPair: %s\n\n", pair.Self.Config.Role, pair.Other.Root)

	pb, pbErr := privateSide.Repo.CurrentBranch()
	mb, mbErr := mirrorSide.Repo.CurrentBranch()
	if pbErr != nil || mbErr != nil {
		return fmt.Errorf("reading branches: private err=%v mirror err=%v", pbErr, mbErr)
	}
	if pb != mb {
		fmt.Printf("Warning: branch mismatch (private=%q, mirror=%q); sync will refuse to run\n\n", pb, mb)
	}

	st, err := state.Load(privateSide.Root)
	if err != nil {
		return err
	}
	bs := st.Branch(pb)

	privateCount, err := unsyncedCount(privateSide.Repo, bs.LastPrivateSha)
	if err != nil {
		return err
	}
	mirrorCount, err := unsyncedCount(mirrorSide.Repo, bs.LastMirrorSha)
	if err != nil {
		return err
	}

	fmt.Println("Private:")
	printSyncLine(privateCount, "not mirrored")
	fmt.Println("\nMirror:")
	printSyncLine(mirrorCount, "not applied to private")

	if stubbed, err := stubbedFileCount(privateSide); err != nil {
		return err
	} else if stubbed > 0 {
		fmt.Printf("\nStubbed files: %d\n", stubbed)
	}

	if len(bs.Mapping) > 0 {
		fmt.Println("\nCommit mapping:")
		for _, m := range bs.Mapping {
			fmt.Printf("  private %s <-> mirror %s\n", gitrepo.ShortSha(m.Private), gitrepo.ShortSha(m.Mirror))
		}
	}
	return nil
}

// stubbedFileCount counts the files in private's current tree whose content
// is replaced by a placeholder in mirror.
func stubbedFileCount(privateSide *pairing.Side) (int, error) {
	if len(privateSide.Config.Stub) == 0 {
		return 0, nil
	}
	head, hasHead, err := privateSide.Repo.RevParseVerify("HEAD")
	if err != nil || !hasHead {
		return 0, err
	}
	entries, err := privateSide.Repo.LsTreeRecursive(head + "^{tree}")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if tokenize.MatchesAny(e.Path, privateSide.Config.Stub) {
			count++
		}
	}
	return count, nil
}

func printSyncLine(count int, suffix string) {
	if count == 0 {
		fmt.Println("  up to date")
		return
	}
	fmt.Printf("  %d commit(s) %s\n", count, suffix)
}

func unsyncedCount(repo *gitrepo.Repo, lastSha string) (int, error) {
	head, hasHead, err := repo.RevParseVerify("HEAD")
	if err != nil {
		return 0, err
	}
	if !hasHead {
		return 0, nil
	}
	rangeSpec := head
	if lastSha != "" {
		rangeSpec = lastSha + ".." + head
	}
	return repo.RevListCount(rangeSpec)
}
