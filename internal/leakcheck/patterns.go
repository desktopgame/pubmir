package leakcheck

import "regexp"

// genericSecretPatterns are common secret shapes that a scanner should catch
// even if the user never registered them as a pubmir secret value. These are
// a heuristic supplement, never a replacement for the value-based scan
// (init.md §9: IPs/hostnames/usernames are secrets here precisely because a
// generic scanner would not flag them).
var genericSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)ghp_[0-9A-Za-z]{36}`),
}
