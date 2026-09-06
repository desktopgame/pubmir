// Package stub provides the placeholder artifact pubmir writes into mirror
// in place of a private file's real contents.
//
// A stub is deliberately not "an anonymized private file": it is a separate,
// safe artifact that only conveys that the path exists. Nothing derived from
// the private file — its content, size, hash, line count or type — may ever
// appear in it, and it must never be reverse-synced back into private.
package stub

// Content is the fixed body of every stub file. It is intentionally free of
// anything derived from the file it replaces.
var Content = []byte("This file is intentionally stubbed by pubmir.\n" +
	"Its private contents are not available in the sanitized mirror.\n")

// Mode is the git file mode used for a generated stub blob when the source
// entry's own mode cannot be preserved.
const Mode = "100644"

// IsRegularFileMode reports whether mode denotes an ordinary file, the only
// kind of entry that may be stubbed. Symlinks (120000) and submodules
// (160000) are rejected rather than guessed at.
func IsRegularFileMode(mode string) bool {
	return mode == "100644" || mode == "100755"
}
