//go:build local

package store

import (
	"database/sql"

	"github.com/nebari-dev/javo/internal/buildinfo"
)

// errNoPostgres is what opening a Postgres database says in a binary
// built with the tag local: `javo local` keeps its rows in a SQLite
// file, and the Postgres driver would add about 3.5 MB to it.
var errNoPostgres = buildinfo.LeftOut("a Postgres database")

func openPostgres(Config) (*sql.DB, error) { return nil, errNoPostgres }

// newPostgresNotifier is the change feed of a database that is never
// opened here (openPostgres): none.
func newPostgresNotifier(string, func(Change)) notifier { return nil }
