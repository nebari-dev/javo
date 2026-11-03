package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"k8s.io/utils/lru"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Service runs the AI features. It exists only when AI is on.
type Service struct {
	Provider provider.Provider
	Model    string
	// Redactor hides secrets in what is sent; nil withholds log lines.
	Redactor      Redactor
	Budget        Budget
	MaxInputRunes int
	// MaxOutputTokens bounds each answer; zero means 16000.
	MaxOutputTokens int

	once  sync.Once
	cache *lru.Cache
	self  struct {
		sync.Mutex
		done bool
		res  provider.SelfTestResult
	}
}

// Dropped counts claims the validator removed from an answer.
type Dropped struct {
	Refs    int `json:"refs"`
	Actions int `json:"actions"`
}

// Scope is what an answer may propose: actions from the catalog, on targets
// the person may change and that are in view.
type Scope struct{ Targets, Actions []string }

// keep turns the model's actions into proposals, keeping those in scope
// and counting the rest.
func (s Scope) keep(ms []ModelAction) ([]actions.Proposal, int) {
	out := []actions.Proposal{}
	dropped := 0
	for _, m := range ms {
		p := m.proposal()
		if slices.Contains(s.Actions, p.Action) && slices.Contains(s.Targets, p.Target) {
			out = append(out, p)
		} else {
			dropped++
		}
	}
	return out, dropped
}

// outcome is a call's result before the feature's own checks: the answer,
// or the catalog text to show instead.
type outcome struct {
	raw        json.RawMessage
	model      string
	message    string
	retryAfter int
	usage      provider.Usage
}

// call is one model request of a feature.
type call struct {
	feature, person, system, task string
	bundle                        Bundle
	// schema is sent to the model; check, when set, is what an answer is
	// validated against instead (the same shape without the enums whose
	// misses the feature drops one by one).
	schema, check *jsonschema.Schema
}

func (s *Service) maxOut() int {
	if s.MaxOutputTokens > 0 {
		return s.MaxOutputTokens
	}
	return 16000
}

// run sends c within the person's allowance and returns the first answer that
// matches the schema; one bad answer is retried silently.
func (s *Service) run(ctx context.Context, c call) outcome {
	schemaJSON, err := json.Marshal(c.schema)
	if err != nil {
		return outcome{message: reasons.Msg(reasons.MsgServerError)}
	}
	check := c.check
	if check == nil {
		check = c.schema
	}
	resolved, err := check.Resolve(nil)
	if err != nil {
		return outcome{message: reasons.Msg(reasons.MsgServerError)}
	}
	estimate := (utf8.RuneCountInString(c.system)+utf8.RuneCountInString(c.task)+utf8.RuneCountInString(c.bundle.Text))/3 + s.maxOut()
	release, err := s.Budget.Reserve(ctx, c.person, estimate)
	var used *BudgetUsedError
	switch {
	case errors.As(err, &used):
		s.audit(c, "budget", provider.Usage{}, Dropped{})
		return outcome{message: reasons.Msg(reasons.MsgAIBudgetUsed, used.ResetAt.UTC().Format("15:04")+" UTC")}
	case err != nil:
		slog.Error("reserve AI allowance", "err", err)
		return outcome{message: reasons.Msg(reasons.MsgServerError)}
	}
	var spent provider.Usage
	defer func() { release(spent.InputTokens + spent.OutputTokens) }()
	req := provider.Request{
		Model: s.Model, System: c.system, Untrusted: c.bundle.Text, Task: c.task,
		Schema: schemaJSON, SchemaName: c.feature, MaxOutputTokens: s.maxOut(),
	}
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := s.Provider.Generate(ctx, req)
		spent.InputTokens += resp.Usage.InputTokens
		spent.OutputTokens += resp.Usage.OutputTokens
		var rl *provider.RateLimitedError
		switch {
		case errors.As(err, &rl):
			secs := int(math.Ceil(rl.RetryAfter.Seconds()))
			s.audit(c, "busy", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIBusy, secs), retryAfter: secs}
		case errors.Is(err, provider.ErrUnavailable):
			s.audit(c, "unavailable", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIUnavailable)}
		case errors.Is(err, provider.ErrNoAnswer):
			continue
		case err != nil:
			slog.Warn("ai call failed", "feature", c.feature, "err", err)
			s.audit(c, "error", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIUnavailable)}
		}
		var v any
		if json.Unmarshal(resp.JSON, &v) != nil || resolved.Validate(v) != nil {
			continue
		}
		return outcome{raw: resp.JSON, model: resp.Model, usage: spent}
	}
	s.audit(c, "invalid", spent, Dropped{})
	return outcome{message: reasons.Msg(reasons.MsgAIInvalidAnswer)}
}

// audit writes one line per call: who, which feature, what it cost and
// what was dropped. Never the prompt or the answer.
func (s *Service) audit(c call, result string, u provider.Usage, d Dropped) {
	slog.Info("ai.call", "user", c.person, "feature", c.feature, "provider", s.Provider.ID(), "model", s.Model,
		"tokens_in", u.InputTokens, "tokens_out", u.OutputTokens, "hidden", c.bundle.Hidden, "withheld", c.bundle.Withheld,
		"dropped_refs", d.Refs, "dropped_actions", d.Actions, "outcome", result)
}

// cached returns a stored answer for key, or runs fn and stores what it
// returns when ok. Keys include the person, so no one is served another
// person's answer.
func (s *Service) cached(key string, fn func() (any, bool)) any {
	s.once.Do(func() { s.cache = lru.New(256) })
	if v, ok := s.cache.Get(key); ok {
		return v
	}
	v, ok := fn()
	if ok {
		s.cache.Add(key, v)
	}
	return v
}

func cacheKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SelfTestResult is the remembered self-test, if it ran.
func (s *Service) SelfTestResult() (provider.SelfTestResult, bool) {
	s.self.Lock()
	defer s.self.Unlock()
	return s.self.res, s.self.done
}

// SelfTest runs the provider self-test once and remembers the result.
func (s *Service) SelfTest(ctx context.Context) provider.SelfTestResult {
	s.self.Lock()
	defer s.self.Unlock()
	if !s.self.done {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		s.self.res, s.self.done = provider.SelfTest(ctx, s.Provider, s.Model), true
	}
	return s.self.res
}

// keepRefs keeps the refs that are in the bundle and counts the rest.
func keepRefs(refs []string, b Bundle) ([]string, int) {
	out := []string{}
	dropped := 0
	for _, r := range refs {
		if slices.Contains(b.Refs, r) && !slices.Contains(out, r) {
			out = append(out, r)
		} else {
			dropped++
		}
	}
	return out, dropped
}
