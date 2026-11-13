//go:build linux || darwin

package process

import (
	"errors"
	"io/fs"
	"path"
	"strings"
)

// toolsDir, in the state directory, holds the tools the step that
// installs an environment needs and a machine may lack. It goes first on
// that step's PATH (Executor.Host) and on no other unit's, so nothing
// else runs them in place of the machine's own.
const toolsDir = "bin"

// javoTools are the tools Javo itself stands in for (internal/cli/podcmd).
var javoTools = []string{"sha256sum", "flock"}

// tools fills toolsDir: pixi is a link to the binary Javo was given, and
// each of javoTools a script that runs this binary's command of that
// name. They are written anew each time, since Javo may have moved.
func (e *engine) tools() error {
	if err := e.root.MkdirAll(toolsDir, 0o700); err != nil {
		return err
	}
	// The path is Javo's own, and is still quoted: a quote in it ends
	// nothing.
	self := "'" + strings.ReplaceAll(e.self, "'", `'\''`) + "'"
	for _, name := range javoTools {
		script := "#!/bin/sh\nexec " + self + " " + name + ` "$@"` + "\n"
		file := path.Join(toolsDir, name)
		if err := e.root.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := e.root.WriteFile(file, []byte(script), 0o700); err != nil {
			return err
		}
	}
	link := path.Join(toolsDir, "pixi")
	if err := e.root.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if e.pixi == "" {
		return nil
	}
	return e.root.Symlink(e.pixi, link)
}
