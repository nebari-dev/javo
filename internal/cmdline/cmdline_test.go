package cmdline

import (
	"errors"
	"slices"
	"testing"
)

func TestSplitQuotesLikeAShell(t *testing.T) {
	for in, want := range map[string][]string{
		`python app.py --port={{.Port}} --base-url={{.BaseURL}}`: {"python", "app.py", "--port={{.Port}}", "--base-url={{.BaseURL}}"},
		`echo "a b" 'c d' e\ f`:                                  {"echo", "a b", "c d", "e f"},
		`  serve   --flag  `:                                     {"serve", "--flag"},
		`echo '$HOME' '~'`:                                       {"echo", "$HOME", "~"},
	} {
		got, err := Split(in)
		if err != nil {
			t.Fatalf("Split(%q): %v", in, err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("Split(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitRefusesWhatOnlyAShellCouldRun(t *testing.T) {
	for _, in := range []string{`echo "open`, `a | b`, `a; b`, `a > out.txt`, `echo $(date)`, "echo `date`"} {
		_, err := Split(in)
		var bad *SyntaxError
		if !errors.As(err, &bad) || bad.Detail == "" {
			t.Fatalf("Split(%q) err = %v, want a SyntaxError with detail", in, err)
		}
	}
}

// No variable is expanded: there is no shell, so $NAME and ~ would reach the
// program as text it does not expect. They are refused instead.
func TestSplitRefusesVariables(t *testing.T) {
	for _, in := range []string{`echo $HOME`, `echo ${PORT}`, `run "$X"`, `cat ~/notes.txt`} {
		_, err := Split(in)
		if !errors.Is(err, ErrVariable) {
			t.Fatalf("Split(%q) err = %v, want ErrVariable", in, err)
		}
	}
}

func TestSplitEmptyIsNoWords(t *testing.T) {
	got, err := Split("   ")
	if err != nil || len(got) != 0 {
		t.Fatalf("Split(blank) = %q, %v", got, err)
	}
}

// Join writes words as a line Split reads back as the same words, quoting
// only where a word needs it.
func TestJoinRoundTripsThroughSplit(t *testing.T) {
	words := []string{"bash", "/opt/javo/samples/jobs/print-time.sh", "a b", "it's", "$HOME", "~", "{{.Home}}", `back\slash`, ""}
	line, err := Join(words)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Split(line)
	if err != nil {
		t.Fatalf("Split(%q): %v", line, err)
	}
	if !slices.Equal(got, words) {
		t.Fatalf("Split(Join(%q)) = %q via %q", words, got, line)
	}
	if line, _ := Join([]string{"python", "etl.py", "--fast"}); line != "python etl.py --fast" {
		t.Errorf("plain words quoted: %q", line)
	}
}
