package naming

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of PromptTitle: words that count (articles do not) and runes.
const (
	titleWords = 6
	titleRunes = 48
)

var (
	// titleLead are phrases a request starts with that name nothing,
	// longest first; each is taken off as often as one leads.
	titleLead = [][]string{
		{"i", "want", "you", "to"}, {"i", "would", "like"}, {"i", "want"}, {"i", "need"},
		{"can", "you"}, {"could", "you"}, {"help", "me"}, {"set", "up"}, {"please"}, {"let's"},
		{"create"}, {"build"}, {"make"}, {"write"}, {"generate"}, {"develop"}, {"deploy"},
		{"design"}, {"implement"}, {"code"}, {"add"}, {"me"}, {"a"}, {"an"}, {"the"},
	}
	// titleTo are clauses that read better as "to".
	titleTo = [][]string{
		{"that", "allows", "users", "to"}, {"which", "allows", "users", "to"}, {"that", "allows", "me", "to"},
		{"which", "allows", "me", "to"}, {"that", "lets", "users"}, {"which", "lets", "users"},
		{"that", "lets", "me"}, {"which", "lets", "me"}, {"that", "lets", "you"}, {"so", "i", "can"},
		{"where", "i", "can"}, {"for", "me", "to"}, {"lets", "me"}, {"let", "me"},
	}
	titleArticles = []string{"a", "an", "the"}
	// titleWeak never end a title.
	titleWeak = []string{
		"a", "an", "the", "and", "or", "but", "with", "for", "of", "in", "on", "at", "to", "from",
		"by", "into", "that", "which", "who", "where", "so", "me", "my", "it", "its", "i", "you",
		"lets", "let", "allows", "please", "using", "use", "is", "are", "can", "will",
	}
)

// PromptTitle is a short sentence-case title for an app made from a
// request to a coding agent: the first sentence, without a leading verb
// or filler ("Create a", "Please build me a"), at most titleWords words
// besides articles and titleRunes runes, ending on a word that carries
// meaning. A request with nothing left after that keeps its first words.
func PromptTitle(request string) string {
	first := request
	if i := strings.IndexAny(first, "\n;:!?"); i >= 0 {
		first = first[:i]
	}
	if i := strings.Index(first, ". "); i >= 0 {
		first = first[:i]
	}
	words := strings.Fields(strings.TrimRight(strings.TrimSpace(first), "."))
	low := func(w []string) []string {
		out := make([]string, len(w))
		for i, s := range w {
			out[i] = strings.ToLower(strings.Trim(s, ",."))
		}
		return out
	}
	for cut := true; cut; {
		cut = false
		for _, p := range titleLead {
			if len(words) > len(p) && slices.Equal(low(words[:len(p)]), p) {
				words, cut = words[len(p):], true
				break
			}
		}
	}
	var out []string
	counted, runes := 0, 0
	for i := 0; i < len(words); i++ {
		for _, p := range titleTo {
			if i+len(p) <= len(words) && slices.Equal(low(words[i:i+len(p)]), p) {
				words = slices.Replace(slices.Clone(words), i, i+len(p), "to")
				break
			}
		}
		w := strings.TrimRight(words[i], ",")
		article := slices.Contains(titleArticles, strings.ToLower(w))
		if !article && counted == titleWords || runes+utf8.RuneCountInString(w) > titleRunes {
			break
		}
		if !article {
			counted++
		}
		out = append(out, w)
		runes += utf8.RuneCountInString(w) + 1
	}
	for len(out) > 0 && slices.Contains(titleWeak, strings.ToLower(out[len(out)-1])) {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		out = strings.Fields(request)
		if len(out) > titleWords {
			out = out[:titleWords]
		}
	}
	return sentenceCase(strings.Join(out, " "))
}

// sentenceCase is s with its first letter upper case.
func sentenceCase(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
