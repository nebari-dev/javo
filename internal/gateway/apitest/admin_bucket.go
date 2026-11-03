package apitest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	adminapi "github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/reasons"
)

type BackupStepJSON struct {
	Kind, Name, State string
	Status            struct{ Reason string }
}

type ProgressJSON struct {
	Run    string
	Steps  []BackupStepJSON
	Done   bool
	Failed int
}

type PageJSON struct {
	Bucket *struct {
		Status struct {
			Reason  string
			Problem bool
		}
		Jobs struct {
			Backup  *struct{ Name, Owner string }
			Restore *struct{ Name string }
		}
		LastRun *struct {
			Run, Outcome                                                       string
			Homes, HomesFailed, Volumes, VolumesFailed, Folders, FoldersFailed int
			Database                                                           string
			Status                                                             struct{ Reason string }
		}
		Runs  []struct{ Run string }
		Homes []struct {
			Owner  string
			Person string
			Status struct {
				Reason  string
				Problem bool
			}
		}
	}
}

// Copies records the database copies the gateway asks for, as the copier
// would.
type Copies struct {
	Mu    sync.Mutex
	Files []string
	Runs  []string
}

func BucketWith(t *testing.T, f **Fixture, c *Copies, snaps map[string][]restic.Snapshot, dbCopies []restic.Snapshot) *adminapi.Bucket {
	return &adminapi.Bucket{
		Endpoint: "https://s3.example.com", Name: "backups", Prefix: "javo", Keep: restic.Keep{Daily: 7},
		CopyDatabase: func(ctx context.Context, file, run string) error {
			c.Mu.Lock()
			c.Files, c.Runs = append(c.Files, file), append(c.Runs, run)
			c.Mu.Unlock()
			return backup.RecordDatabaseCopy(ctx, (*f).Db.Facts(), backup.DatabaseCopy{Run: run, At: time.Now(), File: filepath.Base(file), Snapshot: "s"}, time.Now())
		},
		DatabaseCopies: func(context.Context) ([]restic.Snapshot, error) { return dbCopies, nil },
		FetchDatabase: func(context.Context, restic.Snapshot, string) error {
			t.Fatal("fetched a copy that is on disk")
			return nil
		},
		HomeSnapshots: func(_ context.Context, _, owner string) ([]restic.Snapshot, error) {
			if s, ok := snaps[owner]; ok {
				return s, nil
			}
			return nil, &restic.Error{Code: reasons.BackupRepoMissing}
		},
	}
}
