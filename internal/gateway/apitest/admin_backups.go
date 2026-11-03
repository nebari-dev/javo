package apitest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adminapi "github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

type BackupsJSON struct {
	Database struct {
		Engine string `json:"engine"`
		Status struct {
			Reason string `json:"reason"`
			Title  string `json:"title"`
		} `json:"status"`
		Items []struct {
			Name    string    `json:"name"`
			TakenAt time.Time `json:"takenAt"`
			Size    int64     `json:"size"`
		} `json:"items"`
	} `json:"database"`
}

type RestoreRowsJSON struct {
	Rows []struct {
		Name    string `json:"name"`
		Owner   string `json:"owner"`
		Outcome string `json:"outcome"`
		Message string `json:"message"`
	} `json:"rows"`
}

// BackupOf writes a SQLite backup holding apps into dir and returns its name.
// The live database of the test may be either engine; a backup is always a
// SQLite file.
func BackupOf(t *testing.T, dir string, at time.Time, apps ...*model.App) string {
	t.Helper()
	src := storetest.Open(t, store.Config{Engine: store.EngineSQLite, Path: filepath.Join(t.TempDir(), "src.db")})
	if err := src.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if _, err := src.Apps().Create(context.Background(), gatewaytest.Actor, a); err != nil {
			t.Fatal(err)
		}
	}
	name := store.BackupName(at)
	if err := src.Backup(context.Background(), filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	return name
}

func SqliteBackups(dir string) adminapi.BackupConfig {
	return adminapi.BackupConfig{Engine: store.EngineSQLite, Dir: dir, Schedule: "0 2 * * *", Keep: 7}
}
