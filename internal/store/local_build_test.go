//go:build local

package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
)

// A javo built with the tag local keeps its rows in a SQLite file and
// carries no Postgres driver: a database on Postgres is refused in words
// that name the build, before anything is dialled.
func TestALocalBuildRefusesPostgres(t *testing.T) {
	cfg := store.Config{Engine: store.EnginePostgres, DSN: "postgres://db.example.com/javo"}
	_, err := store.Open(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "left out of a javo built with the tag local") {
		t.Errorf("Open: %v", err)
	}
	if _, err := store.OpenDB(cfg); err == nil || !strings.Contains(err.Error(), "left out of a javo built with the tag local") {
		t.Errorf("OpenDB: %v", err)
	}
}
