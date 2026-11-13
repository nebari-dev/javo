//go:build !local

package servercmd

import (
	"io"
	"strings"
	"testing"
)

// The binary of the image and of every release is built without the tag
// local: it carries none of the files, and says how to get one that does,
// whatever flags it is given.
func TestJavoLocalInABinaryBuiltWithoutItSaysHowToBuildOne(t *testing.T) {
	for _, args := range [][]string{nil, {"--port", "9000"}, {"--dir", t.TempDir()}} {
		cmd := NewLocal()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "make local") || !strings.Contains(err.Error(), "built without") {
			t.Errorf("javo local %v = %v", args, err)
		}
	}
}
