package templates

import (
	"testing"
)

// JSON decoding replaces invalid UTF-8, so these are checked on CleanFile.
func TestCleanFileRejectsUnsafeCharacters(t *testing.T) {
	for _, f := range []string{"a\u0085b.py", "a\u009fb.py", "a\xffb.py", "a\u2028b.py", "a\x00b.py", "a\nb.py"} {
		if got, ok := CleanFile(f); ok {
			t.Errorf("%q accepted as %q", f, got)
		}
	}
}
