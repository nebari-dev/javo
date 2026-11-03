package storageadmin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/storageadmin"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	bg    = context.Background()
	admin = model.Actor{Person: "admin"}
)

// cluster is a database whose storage was inspected and chose mode (none:
// not inspected yet).
func cluster(t *testing.T, mode model.HomeMode) *store.Storage {
	t.Helper()
	st := storetest.New(t).Storage()
	if mode != "" {
		if err := st.SetClusterStatus(bg, model.ClusterStorageStatus{Choice: &model.StorageChoice{Mode: mode}}); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func get(t *testing.T, st *store.Storage) model.StorageRequests {
	t.Helper()
	r, err := st.Requests(bg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRequestProbeSetsAFreshNonceForTheClass(t *testing.T) {
	st := cluster(t, model.HomeModePerUser)
	n1, err := storageadmin.RequestProbe(bg, st, admin, "longhorn", 4*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p := get(t, st).Probe
	if p.StorageClassName != "longhorn" || p.Nonce != n1 || n1 == "" || p.Timeout.Duration != 4*time.Minute {
		t.Fatalf("probe = %+v (nonce %q)", p, n1)
	}
	n2, err := storageadmin.RequestProbe(bg, st, admin, "longhorn", time.Minute)
	if err != nil || n2 == n1 {
		t.Fatalf("a second request needs its own nonce: %q then %q, %v", n1, n2, err)
	}
}

func TestRequestProbeWithoutATimeoutLeavesTheConfiguredOne(t *testing.T) {
	st := cluster(t, model.HomeModePerUser)
	if _, err := storageadmin.RequestProbe(bg, st, admin, "longhorn", 0); err != nil {
		t.Fatal(err)
	}
	if p := get(t, st).Probe; p.Timeout != nil {
		t.Fatalf("timeout = %v, want unset", p.Timeout.Duration)
	}
}

func TestScheduleNeedsAChosenMode(t *testing.T) {
	st := cluster(t, "")
	err := storageadmin.ScheduleMove(bg, st, admin, time.Now())
	if !errors.Is(err, storageadmin.ErrNotInspected) {
		t.Fatalf("err = %v, want ErrNotInspected", err)
	}
	if get(t, st).Migration.StartAt != nil {
		t.Error("a start time was stored")
	}
}

func TestScheduleTargetsTheChosenMode(t *testing.T) {
	st := cluster(t, model.HomeModeShared)
	at := time.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	if err := storageadmin.ScheduleMove(bg, st, admin, at); err != nil {
		t.Fatal(err)
	}
	m := get(t, st).Migration
	if m.StartAt == nil || !m.StartAt.Time.Equal(at) || m.To != model.HomeModeShared {
		t.Fatalf("migration = %+v", m)
	}
}

func TestRetryRaisesTheAttemptEachTime(t *testing.T) {
	st := cluster(t, model.HomeModeShared)
	for want := int32(1); want <= 2; want++ {
		if err := storageadmin.RetryMove(bg, st, admin); err != nil {
			t.Fatal(err)
		}
		if got := get(t, st).Migration.Attempt; got != want {
			t.Fatalf("attempt = %d, want %d", got, want)
		}
	}
}
