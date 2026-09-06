package tokenize

import "github.com/bmatcuk/doublestar/v4"

// IsExcluded reports whether path (repo-root-relative, "/"-separated)
// matches any of patterns (gitignore-style globs supporting "**").
func IsExcluded(path string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := doublestar.Match(pat, path); ok {
			return true
		}
	}
	return false
}
