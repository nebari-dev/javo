//go:build !local

package store

import (
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func openPostgres(cfg Config) (*sql.DB, error) {
	pc, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.ReadOnly {
		pc.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return stdlib.OpenDB(*pc), nil
}

// newPostgresNotifier is the change feed of a Postgres database.
func newPostgresNotifier(dsn string, emit func(Change)) notifier {
	return &pgNotifier{dsn: dsn, emit: emit}
}
