// Package homepath checks file paths that point into a user's home folder.
// The gateway checks a path before storing it on a Workspace and the
// controller checks it again before passing it to an app's command line.
package homepath

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrUnsafe is returned for a path that is not valid UTF-8 or that contains
// a control character (C0 including NUL and newline, DEL, C1) or a Unicode
// line or paragraph separator.
var ErrUnsafe = errors.New("file path is not valid UTF-8 or contains control characters")

// Check returns ErrUnsafe unless p is safe to pass to an app.
func Check(p string) error {
	if !utf8.ValidString(p) || strings.ContainsFunc(p, unsafeRune) {
		return ErrUnsafe
	}
	return nil
}

func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || r == ' ' || r == ' '
}
