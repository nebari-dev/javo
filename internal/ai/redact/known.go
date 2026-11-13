// Package redact hides secrets in text before it is sent to a model: first
// the values the platform knows are secret (Known), then whatever a
// Redactor finds by pattern (redact/scanner, the rules of gitleaks). It
// does not look for personal data.
package redact

import (
	"slices"
	"strings"
)

// Span is a stretch of text (bytes Start to End) holding the value of the
// secret or variable Name.
type Span struct {
	Start, End int
	Name       string
}

// Value is a value to hide wherever it appears, as Name.
type Value struct{ Name, Value string }

// Redactor is what Known runs after hiding what it knows: the patterns of
// scanner.Scanner.
type Redactor interface {
	Redact(text string) (string, int)
}

// Known hides what the platform knows is secret before any pattern runs:
// the places Spans finds (the owner's stored secret values, found by their
// fingerprints) and every occurrence of Values (typed values of variables
// named like credentials). Each becomes [redacted:<name>], or [redacted]
// when Anonymous is set (for someone other than the owner, who must not learn
// which secret a text held). Next, if set, then runs on the result.
type Known struct {
	Spans     func(text string) []Span
	Values    []Value
	Next      Redactor
	Anonymous bool
}

// Redact returns text with what k knows hidden, and how many values it hid.
func (k Known) Redact(text string) (string, int) {
	var spans []Span
	if k.Spans != nil {
		spans = k.Spans(text)
	}
	for _, v := range k.Values {
		if len(v.Value) < minValue {
			continue
		}
		for i := 0; ; {
			j := strings.Index(text[i:], v.Value)
			if j < 0 {
				break
			}
			spans = append(spans, Span{Start: i + j, End: i + j + len(v.Value), Name: v.Name})
			i += j + len(v.Value)
		}
	}
	if k.Anonymous {
		for i := range spans {
			spans[i].Name = ""
		}
	}
	text, n := replace(text, spans)
	if k.Next != nil {
		var m int
		text, m = k.Next.Redact(text)
		n += m
	}
	return text, n
}

// minValue is the shortest typed value hidden: shorter ones are ordinary
// words and numbers.
const minValue = 4

// replace hides each span; overlapping spans become one, named by the one
// that starts first (the longest of those).
func replace(text string, spans []Span) (string, int) {
	if len(spans) == 0 {
		return text, 0
	}
	slices.SortFunc(spans, func(a, b Span) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return b.End - a.End
	})
	var sb strings.Builder
	at, n := 0, 0
	for _, s := range spans {
		if s.Start < at {
			at = max(at, s.End)
			continue
		}
		sb.WriteString(text[at:s.Start])
		mark := "[redacted]"
		if s.Name != "" {
			mark = "[redacted:" + s.Name + "]"
		}
		sb.WriteString(Marker(mark, text[s.Start:s.End]))
		at, n = s.End, n+1
	}
	sb.WriteString(text[at:])
	return sb.String(), n
}

// SensitiveEnv is the values of env whose names look like credentials
// (SensitiveName), to hide as Values.
func SensitiveEnv(env map[string]string) []Value {
	var out []Value
	for name, v := range env {
		if SensitiveName(name) {
			out = append(out, Value{Name: name, Value: v})
		}
	}
	slices.SortFunc(out, func(a, b Value) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// sensitiveWords make a name sensitive wherever they appear in it
// (PGPASSWORD, GITHUB_TOKEN); sensitiveParts only as a whole part between
// underscores (API_KEY, but not MONKEY_COUNT).
var (
	sensitiveWords = []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "CREDENTIAL", "APIKEY", "PRIVATE", "DATABASE_URL", "CONNECTION_STRING"}
	sensitiveParts = []string{"KEY", "PWD", "PASS", "DSN", "AUTH", "COOKIE", "SALT"}
)

// SensitiveName says whether a variable called name likely holds a
// credential.
func SensitiveName(name string) bool {
	up := strings.ToUpper(name)
	if slices.ContainsFunc(sensitiveWords, func(w string) bool { return strings.Contains(up, w) }) {
		return true
	}
	return slices.ContainsFunc(strings.FieldsFunc(up, func(r rune) bool { return r == '_' || r == '-' || r == '.' }),
		func(p string) bool { return slices.Contains(sensitiveParts, p) })
}

// Marker is what replaces secret: mark, once per line secret spans, so the
// text keeps its line breaks.
func Marker(mark, secret string) string {
	return mark + strings.Repeat("\n"+mark, strings.Count(secret, "\n"))
}
