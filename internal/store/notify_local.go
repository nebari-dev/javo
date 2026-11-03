package store

import (
	"context"
	"database/sql"
	"log/slog"
)

// notifier is how committed writes reach the change feed. Open picks one for
// the engine; nothing else branches on it.
type notifier interface {
	// inTx runs inside the writing transaction, before commit.
	inTx(ctx context.Context, tx *sql.Tx, changes []Change) error
	// afterCommit runs once the transaction has committed.
	afterCommit(changes []Change)
	// start begins delivery on Changes until ctx ends.
	start(ctx context.Context) error
}

// localNotifier is the SQLite case: the writer is this process, so it
// announces itself after the commit and there is nothing to start.
type localNotifier struct{ emit func(Change) }

func (localNotifier) inTx(context.Context, *sql.Tx, []Change) error { return nil }
func (localNotifier) start(context.Context) error                   { return nil }

func (n localNotifier) afterCommit(changes []Change) {
	for _, c := range changes {
		n.emit(c)
	}
}

// emit never blocks: a full buffer drops the announcement and the
// controller's periodic resync covers it.
func (s *Store) emit(c Change) {
	select {
	case s.changes <- c:
	default:
		slog.Warn("change feed full, relying on resync", "table", c.Table, "id", c.ID)
	}
}
