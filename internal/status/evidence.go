package status

import (
	"strings"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maxEvidenceLines = 20
	maxEvidenceChars = 300
)

// CapMessage joins at most maxLines of lines with newlines, each cut to
// maxChars bytes on a character boundary and ended with "..." when cut.
func CapMessage(lines []string, maxLines, maxChars int) string {
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) > maxChars {
			cut := maxChars - 3
			// Back up to a rune start so a multi-byte character is not split.
			for cut > 0 && !utf8.RuneStart(l[cut]) {
				cut--
			}
			l = l[:cut] + "..."
		}
		out[i] = l
	}
	return strings.Join(out, "\n")
}

// WithEvidence stores the evidence lines in the failing (False) condition's
// message, newline-separated, so they outlive the pod. Other conditions keep
// their short message.
func WithEvidence(conds []metav1.Condition, evidence []string) []metav1.Condition {
	if len(evidence) == 0 {
		return conds
	}
	msg := CapMessage(evidence, maxEvidenceLines, maxEvidenceChars)
	out := append([]metav1.Condition(nil), conds...)
	for i := range out {
		if out[i].Status == metav1.ConditionFalse {
			out[i].Message = msg
			break
		}
	}
	return out
}
