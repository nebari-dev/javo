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

	"github.com/google/jsonschema-go/jsonschema"
	"k8s.io/utils/lru"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/ai/redact"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Backend is the provider the features call and the model they ask for.
// Rev names this pair: a new Rev is a new backend, with its own self-test.
type Backend struct {
	Provider provider.Provider
	Model    string
	Rev      string
}

// Fixed is a backend that never changes.
func Fixed(p provider.Provider, model string) func(context.Context) (Backend, error) {
	b := Backend{Provider: p, Model: model, Rev: "fixed"}
	return func(context.Context) (Backend, error) { return b, nil }
}

// Service runs the AI features. It exists only when AI is on.
type Service struct {
	// Backend is asked at the start of every call, so a change an admin
	// makes applies to the next call; an error answers as an unavailable
	// provider.
	Backend func(ctx context.Context) (Backend, error)
	// Redactor hides secrets in what is sent; nil withholds log lines.
	Redactor      Redactor
	Budget        Budget
	MaxInputRunes int
	// MaxOutputTokens bounds each answer; zero means 16000.
	MaxOutputTokens int
	// SecretsOf says where an owner's stored secret values are in a text,
	// and whether some cannot be looked for yet. nil: none can, so log lines
	// are withheld (see KnownFor).
	SecretsOf func(ctx context.Context, owner string) (find func(text string) []redact.Span, unchecked bool, err error)

	once  sync.Once
	cache *lru.Cache
	self  struct {
		sync.Mutex
		// rev is the backend the result (done) or the running test is for.
		rev           string
		done, running bool
		res           provider.SelfTestResult
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
	// backend is the one that answered, for the feature's audit line.
	backend Backend
}

// call is one model request of a feature.
type call struct {
	feature, person, system, task string
	bundle                        Bundle
	// schema is sent to the model; check, when set, is what an answer is
	// validated against instead (the same shape without the enums whose
	// misses the feature drops one by one).
	schema, check *jsonschema.Schema
	// model and maxOut, when set, replace the service's model and output cap.
	model  string
	maxOut int
}

func (s *Service) maxOut(c call) int {
	switch {
	case c.maxOut > 0:
		return c.maxOut
	case s.MaxOutputTokens > 0:
		return s.MaxOutputTokens
	}
	return 16000
}

func modelOf(c call, b Backend) string {
	if c.model != "" {
		return c.model
	}
	return b.Model
}

// requestOverhead is what a provider adds to a request in tokens: message
// framing and its own instructions for a structured answer.
const requestOverhead = 1024

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
	b, err := s.Backend(ctx)
	if err != nil {
		slog.Warn("ai backend", "feature", c.feature, "err", err)
		s.audit(c, Backend{}, "unavailable", provider.Usage{}, Dropped{})
		return outcome{message: reasons.Msg(reasons.MsgAIUnavailable)}
	}
	req := provider.Request{
		Model: modelOf(c, b), System: c.system, Untrusted: c.bundle.Text, Task: c.task,
		Schema: schemaJSON, SchemaName: c.feature, MaxOutputTokens: s.maxOut(c),
	}
	// The most one attempt can cost: a token per byte sent (a token is at
	// least one byte), the provider's own framing, and the whole output.
	most := len(req.System) + len(req.Untrusted) + len(req.Task) + len(req.Schema) + requestOverhead + req.MaxOutputTokens
	var spent provider.Usage
	for attempt := 0; attempt < 2; attempt++ {
		// Each attempt, the retry included, is reserved before it is sent
		// and settled with what it used, so a call never takes the day past
		// the allowance.
		release, err := s.Budget.Reserve(ctx, c.person, most)
		var used *BudgetUsedError
		switch {
		case errors.As(err, &used):
			s.audit(c, b, "budget", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIBudgetUsed, used.ResetAt.UTC().Format("15:04")+" UTC")}
		case err != nil:
			slog.Error("reserve AI allowance", "err", err)
			return outcome{message: reasons.Msg(reasons.MsgServerError)}
		}
		resp, err := b.Provider.Generate(ctx, req)
		release(resp.Usage.InputTokens + resp.Usage.OutputTokens)
		spent.InputTokens += resp.Usage.InputTokens
		spent.OutputTokens += resp.Usage.OutputTokens
		var rl *provider.RateLimitedError
		switch {
		case errors.As(err, &rl):
			secs := int(math.Ceil(rl.RetryAfter.Seconds()))
			s.audit(c, b, "busy", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIBusy, secs), retryAfter: secs}
		case errors.Is(err, provider.ErrUnavailable):
			s.audit(c, b, "unavailable", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIUnavailable)}
		case errors.Is(err, provider.ErrNoAnswer):
			continue
		case err != nil:
			slog.Warn("ai call failed", "feature", c.feature, "err", err)
			s.audit(c, b, "error", spent, Dropped{})
			return outcome{message: reasons.Msg(reasons.MsgAIUnavailable)}
		}
		var v any
		if json.Unmarshal(resp.JSON, &v) != nil || resolved.Validate(v) != nil {
			continue
		}
		return outcome{raw: resp.JSON, model: resp.Model, usage: spent, backend: b}
	}
	s.audit(c, b, "invalid", spent, Dropped{})
	return outcome{message: reasons.Msg(reasons.MsgAIInvalidAnswer)}
}

// audit writes one line per call: who, which feature, what it cost and
// what was dropped. Never the prompt or the answer.
func (s *Service) audit(c call, b Backend, result string, u provider.Usage, d Dropped) {
	id := ""
	if b.Provider != nil {
		id = b.Provider.ID()
	}
	slog.Info("ai.call", "user", c.person, "feature", c.feature, "provider", id, "model", modelOf(c, b),
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

// SelfTestResult is the remembered self-test of the backend in force, if
// it ran; a backend that cannot be built is a failed one.
func (s *Service) SelfTestResult(ctx context.Context) (provider.SelfTestResult, bool) {
	b, err := s.Backend(ctx)
	if err != nil {
		return provider.SelfTestResult{Err: err.Error()}, true
	}
	s.self.Lock()
	defer s.self.Unlock()
	if !s.self.done || s.self.rev != b.Rev {
		return provider.SelfTestResult{}, false
	}
	return s.self.res, true
}

// SelfTest runs the provider self-test of the backend in force, once per
// backend, and remembers the result. While one runs, a second call returns
// at once with an empty result.
func (s *Service) SelfTest(ctx context.Context) provider.SelfTestResult {
	b, err := s.Backend(ctx)
	if err != nil {
		return provider.SelfTestResult{Err: err.Error()}
	}
	s.self.Lock()
	if s.self.rev == b.Rev && (s.self.done || s.self.running) {
		res := s.self.res
		s.self.Unlock()
		return res
	}
	s.self.rev, s.self.done, s.self.running = b.Rev, false, true
	s.self.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res := provider.SelfTest(ctx, b.Provider, b.Model)
	s.self.Lock()
	defer s.self.Unlock()
	if s.self.rev == b.Rev {
		s.self.res, s.self.done, s.self.running = res, true, false
	}
	return res
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
