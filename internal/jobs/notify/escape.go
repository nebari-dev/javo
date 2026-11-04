package notify

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"

	"github.com/argoproj/notifications-engine/pkg/api"
	"github.com/argoproj/notifications-engine/pkg/services"
)

// maxText is the longest a text from the job (its failure message, its
// display name) may be in a notice, in characters.
const maxText = 500

// serviceTypes maps each service name in the operator's ConfigMap
// (service.<type>[.<name>]) to its type, so a notice is escaped for where it goes.
func serviceTypes(cm *corev1.ConfigMap) map[string]string {
	out := map[string]string{}
	if cm == nil {
		return out
	}
	for k := range cm.Data {
		parts := strings.Split(k, ".")
		switch {
		case parts[0] != "service":
		case len(parts) == 2:
			out[parts[1]] = parts[1]
		case len(parts) == 3:
			out[parts[2]] = parts[1]
		}
	}
	return out
}

// varsFor is TemplateVars with every text escaped for the destination's
// service type: a job's own output reaches operator channels, so it must not
// break the notice's format, mention people (@channel), or carry a live link.
func varsFor(types map[string]string) api.GetVars {
	return func(obj map[string]any, dest services.Destination) map[string]any {
		vars := TemplateVars(obj, dest)
		if dest.Service == "" {
			// A trigger only evaluates conditions; nothing is rendered.
			return vars
		}
		esc := escaper(types[dest.Service])
		return walk(vars, esc).(map[string]any)
	}
}

// walk returns v with esc applied to every string in it.
func walk(v any, esc func(string) string) any {
	switch t := v.(type) {
	case string:
		return esc(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = walk(x, esc)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = walk(x, esc)
		}
		return out
	}
	return v
}

// escaper is how a text is made safe for a service type. Every type gets
// plain text: no mentions, no live links, the HTML and chat markup
// characters (&, <, >) as entities. A webhook body is usually JSON the
// operator writes around the value, quoted by hand or with toJson, so there
// the text also loses what a JSON string would need escaped (quotes become
// apostrophes, backslashes slashes, line breaks spaces): it is then safe
// between quotes and reads the same under toJson.
func escaper(serviceType string) func(string) string {
	plain := func(s string) string { return markup.Replace(defang(noMentions(clean(s)))) }
	if serviceType == "webhook" {
		return func(s string) string { return jsonSafe.Replace(plain(s)) }
	}
	return plain
}

// markup writes the characters chat and HTML markup is made of as entities
// (Slack and Mattermost read &amp;, &lt; and &gt; the same way).
var markup = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

var jsonSafe = strings.NewReplacer(`"`, "'", `\`, "/", "\n", " ", "\t", " ")

// clean drops control characters but line breaks and tabs, and cuts s to maxText.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, strings.ToValidUTF8(s, ""))
	return capped(s)
}

// capped is s cut to maxText characters, marked when cut.
func capped(s string) string {
	if utf8.RuneCountInString(s) <= maxText {
		return s
	}
	return string([]rune(s)[:maxText]) + "..."
}

// mention is an at sign that starts a word: @channel, @here, @someone.
var mention = regexp.MustCompile(`@(\w)`)

// noMentions puts a zero-width space after each such at sign, so no chat
// reads it as a mention.
func noMentions(s string) string { return mention.ReplaceAllString(s, "@​$1") }

// link is what a chat or mail reader turns into a link: a URL with a scheme,
// a www. host, or a host name followed by a path.
var link = regexp.MustCompile(`(?i)\b(?:[a-z][a-z0-9+.-]*://\S*|www\.\S+|(?:[a-z0-9-]+\.)+[a-z]{2,}(?::\d+)?/\S*)`)

// defang writes each link so it is not one: evil.example/x becomes
// evil[.]example/x and https:// becomes https[:]//.
func defang(s string) string {
	return link.ReplaceAllStringFunc(s, func(l string) string {
		return strings.ReplaceAll(strings.ReplaceAll(l, "://", "[:]//"), ".", "[.]")
	})
}
