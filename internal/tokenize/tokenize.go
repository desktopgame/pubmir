// Package tokenize implements the stable, reversible byte-level substitution
// between real secret values and <PUBMIR:KEY> tokens. Substitution operates
// on raw bytes rather than decoded text so it applies uniformly to text and
// binary content alike.
package tokenize

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
)

var TokenPattern = regexp.MustCompile(`<PUBMIR:([A-Za-z0-9_]+)>`)

func Token(key string) string {
	return "<PUBMIR:" + key + ">"
}

// pair is one (value, token) substitution.
type pair struct {
	value []byte
	token []byte
}

// Mapper performs private-value -> token substitution (private -> mirror
// direction). It is built from the current secret values plus every
// historical value for each key, so an unsynced old commit that predates a
// secret rotation still gets correctly tokenized.
type Mapper struct {
	pairs []pair
}

// NewMapper builds a Mapper from current values and, for each key, every
// historical value ever observed (current included). All values for a key
// map to the same token. Values are tried longest-first so that one value
// being a substring of another never causes a partial/incorrect rewrite.
func NewMapper(current map[string]string, historyValues map[string][]string) *Mapper {
	m := &Mapper{}
	seen := map[string]bool{}
	add := func(key, value string) {
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		m.pairs = append(m.pairs, pair{value: []byte(value), token: []byte(Token(key))})
	}
	for key, v := range current {
		add(key, v)
	}
	for key, vs := range historyValues {
		for _, v := range vs {
			add(key, v)
		}
	}
	sort.SliceStable(m.pairs, func(i, j int) bool {
		return len(m.pairs[i].value) > len(m.pairs[j].value)
	})
	return m
}

// Tokenize replaces every occurrence of every known secret value in data
// with its token, longest values first.
func (m *Mapper) Tokenize(data []byte) []byte {
	for _, p := range m.pairs {
		data = bytes.ReplaceAll(data, p.value, p.token)
	}
	return data
}

// FindLeakingKey reports the key of the first known secret value that
// occurs literally within s (used to scan file paths, which are never
// tokenized but must not be allowed to leak a secret value either).
func (m *Mapper) FindLeakingKey(s string) (token string, ok bool) {
	b := []byte(s)
	for _, p := range m.pairs {
		if len(p.value) > 0 && bytes.Contains(b, p.value) {
			return string(p.token), true
		}
	}
	return "", false
}

// Detokenize replaces every <PUBMIR:KEY> token in data with the current
// value for KEY. It returns an error naming every KEY that has no current
// value and is not in reserved, without partially applying any substitution
// — the whole content is rejected so no unresolved token or partial rewrite
// can ever reach private.
//
// A token whose key is in reserved (see ReservedSet) is left exactly as
// written instead of being resolved or reported as unknown: it names a
// value declared in .pubmir.yml's example_tokens as a documentation
// example, not a real secret, so there is nothing to restore it to.
func Detokenize(data []byte, current map[string]string, reserved map[string]bool) ([]byte, error) {
	unknown := UnknownTokens(data, current, reserved)
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown pubmir token(s), not defined in .pubmir.env: %v", unknown)
	}
	return TokenPattern.ReplaceAllFunc(data, func(m []byte) []byte {
		key := string(TokenPattern.FindSubmatch(m)[1])
		if reserved[key] {
			return m
		}
		return []byte(current[key])
	}), nil
}

// UnknownTokens returns the sorted, de-duplicated list of KEYs referenced by
// <PUBMIR:KEY> tokens in data that have no entry in current and are not
// declared in reserved (see ReservedSet).
func UnknownTokens(data []byte, current map[string]string, reserved map[string]bool) []string {
	seen := map[string]bool{}
	var unknown []string
	for _, m := range TokenPattern.FindAllSubmatch(data, -1) {
		key := string(m[1])
		if reserved[key] {
			continue
		}
		if _, ok := current[key]; !ok && !seen[key] {
			seen[key] = true
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// ReservedSet builds a lookup set from a list of token names (typically
// .pubmir.yml's example_tokens), for passing to UnknownTokens/Detokenize. A
// nil/empty names reliably yields a nil map, which every lookup treats as
// "nothing reserved".
func ReservedSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}
