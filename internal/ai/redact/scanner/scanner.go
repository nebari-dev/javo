// Package scanner hides whatever matches the rules of gitleaks (its detect
// package, used as a library) in text before it is sent to a model, after
// redact.Known hid the values the platform knows are secret. It does not
// look for personal data. It is a package of its own because gitleaks is
// large: only what builds an AI service imports it.
package scanner

import (
	"slices"
	"strings"
	"sync"

	"github.com/zricethezav/gitleaks/v8/detect"

	"github.com/nebari-dev/javo/internal/ai/redact"
	"github.com/nebari-dev/javo/internal/auth/token"
)

// Hidden replaces each secret found.
const Hidden = "[hidden]"

// Scanner finds secrets with gitleaks' default rules and the platform's own
// API tokens (token.Pattern).
type Scanner struct {
	mu sync.Mutex
	d  *detect.Detector
}

// New loads gitleaks' default rules. Call it once at start-up: the
// rules are parsed through viper's global instance.
func New() (*Scanner, error) {
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
	// The platform's own API tokens, which no gitleaks rule knows.
	for _, v := range token.Pattern.FindAllString(text, -1) {
		if !slices.Contains(secrets, v) {
			secrets = append(secrets, v)
		}
	}
	// Longest first, so a secret that contains another is hidden whole.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, v := range secrets {
		text = strings.ReplaceAll(text, v, redact.Marker(Hidden, v))
	}
	return text, len(secrets)
}
