package backup

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EventLog writes one line per event: the time, the context fields, the
// action and its fields, as key=value pairs. A value with a space or a quote
// is quoted. It is what a backup's log shows; nil writes nothing.
type EventLog struct {
	W   io.Writer
	Now func() time.Time
	// Fields come first on every line, as key, value, key, value.
	Fields []string

	mu sync.Mutex
}

// Event writes the action with kv (key, value, key, value).
func (l *EventLog) Event(action string, kv ...any) {
	if l == nil || l.W == nil {
		return
	}
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	var b strings.Builder
	b.WriteString(now().Format("15:04:05"))
	pair := func(k string, v any) {
		b.WriteByte(' ')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quote(fmt.Sprint(v)))
	}
	for i := 0; i+1 < len(l.Fields); i += 2 {
		pair(l.Fields[i], l.Fields[i+1])
	}
	pair("action", action)
	for i := 0; i+1 < len(kv); i += 2 {
		pair(fmt.Sprint(kv[i]), kv[i+1])
	}
	b.WriteByte('\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.W, b.String())
}

func quote(v string) string {
	if v == "" || strings.ContainsAny(v, " \"=\t") {
		return strconv.Quote(v)
	}
	return v
}

// Size is n bytes in the largest binary unit that keeps a number of at
// least 1, as 1.2MiB.
func Size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Took is a duration in seconds as 1.2s.
func Took(seconds float64) string { return strconv.FormatFloat(seconds, 'f', 1, 64) + "s" }
