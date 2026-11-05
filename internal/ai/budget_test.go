package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func TestBudgetIsPerPersonPerUTCDay(t *testing.T) {
	db := storetest.New(t)
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, time.FixedZone("CET", 3600)) // 22:30 UTC
	b := &DBBudget{Usage: db.AIUsage(), Daily: 1000, Now: func() time.Time { return now }}
	ctx := context.Background()

	release, err := b.Reserve(ctx, "alice-a", 600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Reserve(ctx, "alice-a", 600)
	var used *BudgetUsedError
	if !errors.As(err, &used) || !used.ResetAt.Equal(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("second reserve: %v", err)
	}
	// The call used 250 of the 600 it reserved.
	release(250)
	if _, err := b.Reserve(ctx, "alice-a", 750); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if _, err := b.Reserve(ctx, "bob-b", 1000); err != nil {
		t.Fatalf("bob has his own allowance: %v", err)
	}
	now = now.Add(2 * time.Hour) // 00:30 UTC on the 2nd
	if _, err := b.Reserve(ctx, "alice-a", 1000); err != nil {
		t.Fatalf("a new UTC day: %v", err)
	}
}

// A call that uses more than it reserved (a provider that counts more than
// expected) is charged in full: the overage counts against the day.
func TestBudgetChargesWhatACallUsedAboveItsReservation(t *testing.T) {
	db := storetest.New(t)
	b := &DBBudget{Usage: db.AIUsage(), Daily: 1000}
	ctx := context.Background()
	release, err := b.Reserve(ctx, "alice-a", 300)
	if err != nil {
		t.Fatal(err)
	}
	release(900)
	_, err = b.Reserve(ctx, "alice-a", 300)
	var used *BudgetUsedError
	if !errors.As(err, &used) {
		t.Fatalf("900 of 1000 used, a 300 reservation: %v", err)
	}
	if _, err := b.Reserve(ctx, "alice-a", 100); err != nil {
		t.Fatalf("900 of 1000 used, a 100 reservation: %v", err)
	}
}

// costly answers every request with the most it can cost: a token per byte
// of what it is sent (code and CJK text run near that) and the whole output
// allowance. Its first answer does not match the schema, so the call
// retries.
type costly struct{ calls int }

func (*costly) ID() string { return "costly" }

func (c *costly) Generate(_ context.Context, req provider.Request) (provider.Response, error) {
	c.calls++
	u := provider.Usage{InputTokens: len(req.System) + len(req.Untrusted) + len(req.Task) + len(req.Schema), OutputTokens: req.MaxOutputTokens}
	if c.calls == 1 {
		return provider.Response{JSON: json.RawMessage(`{"wrong":true}`), Usage: u}, nil
	}
	return provider.Response{JSON: json.RawMessage(injected), Usage: u}, nil
}

// A call never takes the day past the allowance: each attempt, the retry
// included, is reserved at the most it can cost before it is sent, and one
// that does not fit is not sent.
func TestAnAICallNeverOvershootsTheAllowance(t *testing.T) {
	ctx := context.Background()
	// What one attempt reserves: a call that answers well the first time.
	b := &allowAll{}
	probe := service(&costly{calls: 1})
	probe.Budget = b
	if r, _ := probe.Explain(ctx, "bob-b", crash(), salesScope, Known{}, false); r.Explanation == nil {
		t.Fatal(r.Message)
	}
	attempt := b.reserved
	db := storetest.New(t)
	p := &costly{}
	s := service(p)
	// Room for one attempt, not two.
	daily := int64(attempt + attempt/2)
	s.Budget = &DBBudget{Usage: db.AIUsage(), Daily: daily}
	r, _ := s.Explain(ctx, "alice-a", crash(), salesScope, Known{}, false)
	used, err := db.AIUsage().Used(ctx, "alice-a", time.Now().UTC().Format(time.DateOnly))
	if err != nil {
		t.Fatal(err)
	}
	if used > daily {
		t.Fatalf("used %d of a %d allowance (%d calls)", used, daily, p.calls)
	}
	if p.calls != 1 || !strings.HasPrefix(r.Message, strings.SplitN(reasons.Msg(reasons.MsgAIBudgetUsed, "x"), "x", 2)[0]) {
		t.Fatalf("%d calls, message %q: the retry should be refused for the allowance", p.calls, r.Message)
	}
}
