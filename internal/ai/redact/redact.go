// Package redact hides secrets in text before it is sent to a model: first
// the values the platform knows are secret (Known), then whatever matches
// the rules of gitleaks (its detect package, used as a library). It does not
// look for personal data.
package redact

import (
	"slices"
	"strings"
	"sync"

	"github.com/zricethezav/gitleaks/v8/detect"
)

// Hidden replaces each secret found.
const Hidden = "[hidden]"

// Scanner finds secrets with gitleaks' default rules.
type Scanner struct {
	mu sync.Mutex
	d  *detect.Detector
}

// NewScanner loads gitleaks' default rules. Call it once at start-up: the
// rules are parsed through viper's global instance.
func NewScanner() (*Scanner, error) {
	d, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return nil, err
	}
	return &Scanner{d: d}, nil
}

// Redact returns text with every secret replaced by Hidden, and how many
// secrets it found.
func (s *Scanner) Redact(text string) (string, int) {
	s.mu.Lock()
	findings := s.d.DetectString(text)
	s.mu.Unlock()
	var secrets []string
	for _, f := range findings {
		v := f.Secret
		if v == "" {
			v = f.Match
		}
		if v != "" && !slices.Contains(secrets, v) {
			secrets = append(secrets, v)
		}
	}
	// Longest first, so a secret that contains another is hidden whole.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, v := range secrets {
		text = strings.ReplaceAll(text, v, Hidden)
	}
	return text, len(secrets)
}
