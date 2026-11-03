package jobs

import (
	"errors"
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/controller/workspace"
)

// A job's command line follows an app's: shell quoting, no shell, the same
// placeholder syntax. Only {{.Home}} means something for a job.
func TestCommandArgsSplitsAndFillsInTheHome(t *testing.T) {
	t.Parallel()
	got, err := CommandArgs(`bash "{{.Home}}/my scripts/run.sh" --fast`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bash", "/home/user/my scripts/run.sh", "--fast"}; !slices.Equal(got, want) {
		t.Errorf("CommandArgs = %q, want %q", got, want)
	}
}

func TestCommandArgsRefusesWhatAJobCannotFillIn(t *testing.T) {
	t.Parallel()
	var bad *workspace.BadPlaceholder
	if _, err := CommandArgs(`serve --port={{.Port}}`); !errors.As(err, &bad) {
		t.Errorf("{{.Port}}: err = %v, want a BadPlaceholder", err)
	}
	if _, err := CommandArgs(`echo $JAVO_PARAM_REGION`); !errors.Is(err, cmdline.ErrVariable) {
		t.Errorf("$JAVO_PARAM_REGION: err = %v, want ErrVariable", err)
	}
	var syn *cmdline.SyntaxError
	if _, err := CommandArgs(`date | tee log`); !errors.As(err, &syn) {
		t.Errorf("pipe: err = %v, want a SyntaxError", err)
	}
}
