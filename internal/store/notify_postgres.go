//go:build !local

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// notifyChannel is the Postgres channel every javo component listens on.
const notifyChannel = "javo_changes"

const (
	reconnectMin = 100 * time.Millisecond
	reconnectMax = 10 * time.Second
)

// pgNotifier announces with pg_notify inside the writing transaction and
// delivers through a LISTEN connection. Nothing is published after the commit:
// a second, local copy would reach the controller twice.
type pgNotifier struct {
	dsn  string
	emit func(Change)
}

func (p *pgNotifier) afterCommit([]Change) {}

// start connects and LISTENs before returning, so a write made after start
// returns is seen, then keeps the connection alive in the background.
func (p *pgNotifier) start(ctx context.Context) error {
	conn, err := p.listen(ctx)
	if err != nil {
		return fmt.Errorf("store: listen: %w", err)
	}
	go p.feed(ctx, conn)
	return nil
}

// inTx announces changes with pg_notify inside the writing transaction:
// Postgres delivers them at commit and drops them on rollback.
func (p *pgNotifier) inTx(ctx context.Context, tx *sql.Tx, changes []Change) error {
	for _, c := range changes {
		payload, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "SELECT pg_notify($1, $2)", notifyChannel, string(payload)); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
	}
	return nil
}

// listen opens the dedicated LISTEN connection.
func (p *pgNotifier) listen(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		_ = conn.Close(context.Background())
		return nil, err
	}
	return conn, nil
}

// feed forwards notifications to the feed until ctx ends. When the
// connection drops it reconnects with backoff and emits Resync, because
// announcements sent while it was down are gone.
func (p *pgNotifier) feed(ctx context.Context, conn *pgx.Conn) {
	for {
		p.forward(ctx, conn)
		_ = conn.Close(context.Background())
		var err error
		for wait := reconnectMin; ; wait = min(wait*2, reconnectMax) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if conn, err = p.listen(ctx); err == nil {
				break
			}
			slog.Warn("change feed reconnect failed", "error", err)
		}
		p.emit(Resync)
	}
}

// forward reads notifications until the connection fails or ctx ends.
func (p *pgNotifier) forward(ctx context.Context, conn *pgx.Conn) {
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("change feed connection lost", "error", err)
			}
			return
		}
		var c Change
		if err := json.Unmarshal([]byte(n.Payload), &c); err != nil {
			slog.Warn("change feed: bad payload", "payload", n.Payload)
			p.emit(Resync)
			continue
		}
		p.emit(c)
	}
}
