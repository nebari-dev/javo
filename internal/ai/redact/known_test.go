package redact_test

import (
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/redact"
	"github.com/nebari-dev/javo/internal/ai/redact/scanner"
)

// A fake credential in a shape the scanner knows; it is not real.
const fakeAWSKey = "AKIAZ7QW3RTY5UXO6PLK"

func patterns(t *testing.T) redact.Redactor {
	t.Helper()
	s, err := scanner.New()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The value of a person's secret printed in free text matches no pattern;
// the platform knows the value (here by its place in the text), so it is
// hidden by name before the patterns run.
func TestKnownSecretValuesInFreeTextAreHiddenByName(t *testing.T) {
	const pw = "Vx9-kQ2pLr7mWz4s"
	line := "connecting to postgres as app with password " + pw + ", retrying with " + pw
	k := redact.Known{
		Spans: func(text string) []redact.Span {
			var out []redact.Span
			for i := 0; ; {
				j := strings.Index(text[i:], pw)
				if j < 0 {
					return out
				}
				out = append(out, redact.Span{Start: i + j, End: i + j + len(pw), Name: "DB_PASSWORD"})
				i += j + len(pw)
			}
		},
		Next: patterns(t),
	}
	out, n := k.Redact(line)
	if strings.Contains(out, pw) || n != 2 {
		t.Fatalf("%q became %q (%d hidden)", line, out, n)
	}
	if want := "connecting to postgres as app with password [redacted:DB_PASSWORD], retrying with [redacted:DB_PASSWORD]"; out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

// Typed values of variables named like credentials are hidden too, and the
// patterns still run on what is left.
func TestValuesOfSensitiveVariablesAreHiddenAndPatternsStillRun(t *testing.T) {
	k := redact.Known{Values: redact.SensitiveEnv(map[string]string{
		"API_TOKEN":   "tok-31415926535",
		"db_password": "hunter2-but-longer",
		"LOG_LEVEL":   "debug",
		"PORT":        "8080",
	}), Next: patterns(t)}
	out, n := k.Redact("token tok-31415926535 pw hunter2-but-longer level debug port 8080 key " + fakeAWSKey)
	for _, leaked := range []string{"tok-31415926535", "hunter2-but-longer", fakeAWSKey} {
		if strings.Contains(out, leaked) {
			t.Errorf("%q leaked in %q", leaked, out)
		}
	}
	if !strings.Contains(out, "[redacted:API_TOKEN]") || !strings.Contains(out, "[redacted:db_password]") {
		t.Errorf("no names in %q", out)
	}
	if !strings.Contains(out, "level debug port 8080") || n < 3 {
		t.Errorf("got %q (%d hidden)", out, n)
	}
}

func TestSensitiveNames(t *testing.T) {
	for name, want := range map[string]bool{
		"PASSWORD": true, "DB_PASSWORD": true, "PGPASSWORD": true, "GITHUB_TOKEN": true, "AWS_SECRET_ACCESS_KEY": true,
		"API_KEY": true, "APIKEY": true, "OPENAI_API_KEY": true, "CLIENT_SECRET": true, "DATABASE_URL": true, "SENTRY_DSN": true,
		"private_key": true, "AUTH_HEADER": true, "CREDENTIALS": true,
		"PORT": false, "LOG_LEVEL": false, "MONKEY_COUNT": false, "KEYBOARD": false, "PATH": false, "HOME": false,
	} {
		if got := redact.SensitiveName(name); got != want {
			t.Errorf("redact.SensitiveName(%q) = %v, want %v", name, got, want)
		}
	}
}
