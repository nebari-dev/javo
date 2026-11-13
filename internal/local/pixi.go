//go:build linux || darwin

package local

import (
	"os"
	"os/exec"
	"path/filepath"
)

// FindPixi is the pixi binary that installs the apps' environments, as an
// absolute path, or empty when there is none: the one beside javo's own
// binary (self), which is where a bundle that ships both puts it, and
// else the one on PATH. Javo carries no pixi and downloads none. The one
// beside the binary is taken only from a folder, and as a file, that the
// group and others cannot write: javo may have been started from a
// folder anyone can put a program in.
func FindPixi(self string) string {
	dir := filepath.Dir(self)
	beside := filepath.Join(dir, "pixi")
	if file, err := os.Stat(beside); err == nil && file.Mode().IsRegular() && file.Mode()&0o111 != 0 && file.Mode()&0o022 == 0 {
		if folder, err := os.Stat(dir); err == nil && folder.Mode()&0o022 == 0 {
			return beside
		}
	}
	found, err := exec.LookPath("pixi")
	if err != nil {
		return ""
	}
	if found, err = filepath.Abs(found); err != nil {
		return ""
	}
	return found
}

// noPixi stands in for pixi where there is none: the step that installs
// an environment runs it, fails, and its message is what the person reads
// as the reason their app or job did not start (the step's termination
// message, and its log). The UI and apps that need no environment work
// without pixi.
const noPixi = `#!/bin/sh
msg='pixi, which installs the environment this runs in, is not on this machine. javo local looks for it beside its own binary and on PATH: install pixi (https://pixi.sh), then start javo local again.'
[ -z "${JAVO_TERMINATION_LOG:-}" ] || printf '%s' "$msg" > "$JAVO_TERMINATION_LOG"
echo "$msg" >&2
exit 1
`

// WithoutPixi writes the stand-in for pixi into the folder root, which
// only the person can write, and returns its path.
func WithoutPixi(root string) (string, error) {
	file := filepath.Join(root, "no-pixi")
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return file, os.WriteFile(file, []byte(noPixi), 0o700)
}
