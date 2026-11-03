package storetest_test

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// Each New is a migrated database of its own: a table made in one is not in
// the next, however New prepares them.
func TestNewGivesEachCallItsOwnMigratedDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := storetest.New(t)
	if err := first.Ready(ctx); err != nil {
		t.Fatalf("first store is not migrated: %v", err)
	}
	storetest.Exec(t, first, "CREATE TABLE probe (x integer)")
	storetest.Exec(t, first, "INSERT INTO probe (x) VALUES (1)")

	second := storetest.New(t)
	if err := second.Ready(ctx); err != nil {
		t.Fatalf("second store is not migrated: %v", err)
	}
	db, err := store.OpenDB(storetest.ConfigOf(t, second))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "SELECT x FROM probe"); err == nil {
		t.Fatal("the second store sees the table made in the first")
	}
}
