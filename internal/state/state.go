// Package state persists, locally and per-repo, which commits have already
// been synced and how private/mirror commits map to each other. It is never
// committed to git: mirror's history must stay free of pubmir's own
// bookkeeping (see init.md §15/§17), and it is meaningless outside the
// paired local checkout anyway (sync always needs filesystem access to both
// repos at once).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"

	"pubmir/internal/config"
)

// Mapping records that a private commit and a mirror commit are the
// sanitized/desanitized form of each other.
type Mapping struct {
	Private string `json:"private"`
	Mirror  string `json:"mirror"`
}

// BranchState is the sync bookkeeping for one branch name.
type BranchState struct {
	LastPrivateSha string    `json:"last_private_sha"`
	LastMirrorSha  string    `json:"last_mirror_sha"`
	Mapping        []Mapping `json:"mapping"`
}

// MirrorFor returns the mirror sha mapped to privateSha, if any.
func (bs *BranchState) MirrorFor(privateSha string) (string, bool) {
	for _, m := range bs.Mapping {
		if m.Private == privateSha {
			return m.Mirror, true
		}
	}
	return "", false
}

// PrivateFor returns the private sha mapped to mirrorSha, if any.
func (bs *BranchState) PrivateFor(mirrorSha string) (string, bool) {
	for _, m := range bs.Mapping {
		if m.Mirror == mirrorSha {
			return m.Private, true
		}
	}
	return "", false
}

// Record adds a new private<->mirror commit mapping and advances the
// per-branch last-synced pointers.
func (bs *BranchState) Record(privateSha, mirrorSha string) {
	bs.Mapping = append(bs.Mapping, Mapping{Private: privateSha, Mirror: mirrorSha})
	bs.LastPrivateSha = privateSha
	bs.LastMirrorSha = mirrorSha
}

// State is the full sync bookkeeping document, keyed by branch name.
type State struct {
	Branches map[string]*BranchState `json:"branches"`
}

func statePath(repoRoot string) string {
	return filepath.Join(repoRoot, config.PubmirDirName, config.StateFileName)
}

// Load reads .pubmir/state.json, returning an empty State if it does not
// exist yet.
func Load(repoRoot string) (*State, error) {
	data, err := os.ReadFile(statePath(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Branches: map[string]*BranchState{}}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Branches == nil {
		s.Branches = map[string]*BranchState{}
	}
	return &s, nil
}

// Save writes .pubmir/state.json.
func (s *State) Save(repoRoot string) error {
	dir := filepath.Join(repoRoot, config.PubmirDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(repoRoot), data, 0o644)
}

// Branch returns the BranchState for name, creating an empty one if absent.
func (s *State) Branch(name string) *BranchState {
	bs, ok := s.Branches[name]
	if !ok {
		bs = &BranchState{}
		s.Branches[name] = bs
	}
	return bs
}

// SaveBoth writes the same branch state to both the private and mirror
// repositories' local state files, keeping them consistent.
func SaveBoth(privateRoot, mirrorRoot, branch string, bs *BranchState) error {
	for _, root := range []string{privateRoot, mirrorRoot} {
		s, err := Load(root)
		if err != nil {
			return err
		}
		s.Branches[branch] = bs
		if err := s.Save(root); err != nil {
			return err
		}
	}
	return nil
}
