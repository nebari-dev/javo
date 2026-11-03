package jobs

import (
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/podspec"
)

// commandData is what a job's command line may name: {{.Home}}. A job has
// no port, URL prefix or chosen file, so those placeholders are refused.
type commandData struct{ Home string }

// CommandArgs turns the command line of a command job into its argv with
// the same rules as an app's typed command (workspace.CommandWords): shell
// quoting, no shell, no variables, {{.Home}} filled in. Errors are
// cmdline's (*cmdline.SyntaxError, cmdline.ErrVariable) or a
// *workspace.BadPlaceholder.
func CommandArgs(line string) ([]string, error) {
	return workspace.CommandWords("", line, commandData{Home: podspec.HomeDir})
}
