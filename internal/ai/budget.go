package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nebari-dev/javo/internal/store"
)

// Budget limits the model tokens each person may use per day.
type Budget interface {
	// Reserve sets aside tokens for one call. release, called with what the
	// call really used, gives back the rest. A used-up allowance is a
	// *BudgetUsedError.
	Reserve(ctx context.Context, person string, tokens int) (release func(actual int), err error)
}

// BudgetUsedError is a used-up daily allowance.
type BudgetUsedError struct{ ResetAt time.Time }

func (e *BudgetUsedError) Error() string {
	return fmt.Sprintf("daily AI allowance used; resets at %s", e.ResetAt.Format(time.RFC3339))
}

// DBBudget keeps the counts in the database, one row per person and UTC day,
// so the limit holds across gateway replicas.
type DBBudget struct {
	Usage *store.AIUsage
	Daily int64
	Now   func() time.Time
}

func (b *DBBudget) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *DBBudget) Reserve(ctx context.Context, person string, tokens int) (func(int), error) {
	now := b.now()
	day := now.Format(time.DateOnly)
	err := b.Usage.Reserve(ctx, person, day, int64(tokens), b.Daily)
	if errors.Is(err, store.ErrLimit) {
		y, m, d := now.Date()
		return nil, &BudgetUsedError{ResetAt: time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)}
	}
	if err != nil {
		return nil, err
	}
	return func(actual int) {
		if unused := tokens - actual; unused > 0 {
			if err := b.Usage.Release(context.WithoutCancel(ctx), person, day, int64(unused)); err != nil {
				slog.Error("release AI allowance", "err", err)
			}
		}
	}, nil
}
