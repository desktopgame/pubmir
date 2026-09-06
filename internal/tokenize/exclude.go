package tokenize

import "github.com/bmatcuk/doublestar/v4"

// MatchesAny reports whether path (repo-root-relative, "/"-separated)
// matches any of patterns (gitignore-style globs supporting "**").
func MatchesAny(path string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := doublestar.Match(pat, path); ok {
			return true
		}
	}
	return false
}

// IsExcluded reports whether path matches any exclude pattern. Stub
// patterns use the same matching rules via MatchesAny.
func IsExcluded(path string, patterns []string) bool {
	return MatchesAny(path, patterns)
}
