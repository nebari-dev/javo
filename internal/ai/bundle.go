// Package ai turns what the platform knows into a model request and the
// model's answer back into something safe to show: the bundle sent (built as
// the person asking, redacted, capped), the provider call (budgeted), and a
// validator that keeps only claims backed by the bundle.
package ai

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Redactor hides secrets in text and says how many values it hid. It finds
// what it knows (Known) and what looks like a credential (gitleaks' rules);
// it does not look for personal data such as names or email addresses.
// Without one, log lines are not sent at all: they are the text an app
// controls, so a password it prints would otherwise reach the model.
type Redactor interface {
	Redact(s string) (out string, hidden int)
}

// Bundle is exactly what is sent; the UI's "What will be sent" shows Text.
type Bundle struct {
	Text string `json:"text"`
	// Refs are the evidence refs in Text: the only ones an answer may cite.
	Refs []string `json:"refs"`
	// Hidden counts values the redactor replaced.
	Hidden int `json:"hidden"`
	// Withheld counts log lines left out because they could not be checked
	// for secrets: no scanner is set, or some secret values cannot be looked for.
	Withheld int   `json:"withheld"`
	Trimmed  *Trim `json:"trimmed,omitempty"`
	// Notes say in words what was hidden, withheld or trimmed.
	Notes []string `json:"notes"`
}

// Trim says how many log lines fit: Used of Total.
type Trim struct {
	Used  int `json:"used"`
	Total int `json:"total"`
}

// BuildBundle wraps each piece of evidence in a labeled delimiter, with its
// text escaped so it cannot close the delimiter, redacts it, and keeps the
// result within maxRunes by dropping the oldest log lines first (conditions
// and events always stay). Log lines are withheld when r is nil or
// withholdLogs is set.
func BuildBundle(evs []diagnose.Evidence, r Redactor, withholdLogs bool, maxRunes int) Bundle {
	var b Bundle
	type piece struct {
		ref, text string
		log       bool
	}
	var pieces []piece
	logs := 0
	for _, e := range evs {
		isLog := e.Kind == diagnose.KindLog
		if isLog && (r == nil || withholdLogs) {
			b.Withheld++
			continue
		}
		text := e.Excerpt
		if r != nil {
			var n int
			text, n = r.Redact(text)
			b.Hidden += n
		}
		pieces = append(pieces, piece{ref: e.Ref, log: isLog,
			text: fmt.Sprintf("<untrusted ref=%q kind=%q>%s</untrusted>\n", e.Ref, e.Kind, html.EscapeString(text))})
		if isLog {
			logs++
		}
	}
	size := 0
	for _, p := range pieces {
		size += utf8.RuneCountInString(p.text)
	}
	// Log lines come oldest first (the previous run, then the current one).
	dropped := 0
	for i := 0; i < len(pieces) && size > maxRunes; i++ {
		if pieces[i].log {
			size -= utf8.RuneCountInString(pieces[i].text)
			pieces[i].text = ""
			dropped++
		}
	}
	if dropped > 0 {
		b.Trimmed = &Trim{Used: logs - dropped, Total: logs}
	}
	var sb strings.Builder
	b.Refs = []string{}
	for _, p := range pieces {
		if p.text == "" {
			continue
		}
		sb.WriteString(p.text)
		b.Refs = append(b.Refs, p.ref)
	}
	b.Text = sb.String()
	b.Notes = []string{}
	if b.Hidden > 0 {
		b.Notes = append(b.Notes, reasons.Msg(reasons.MsgAIRedacted, b.Hidden))
	}
	if b.Withheld > 0 {
		b.Notes = append(b.Notes, reasons.Msg(reasons.MsgAIWithheld, b.Withheld))
	}
	if t := b.Trimmed; t != nil {
		b.Notes = append(b.Notes, reasons.Msg(reasons.MsgAITrimmed, t.Used, t.Total))
	}
	return b
}
