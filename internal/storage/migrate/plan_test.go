package migrate

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/model"
)

func home(mode model.HomeMode, size string, mig *model.HomeMigration) model.Home {
	h := model.Home{Status: model.HomeStatus{Mode: mode, Migration: mig}}
	if size != "" {
		q := resource.MustParse(size)
		h.Spec.Size = &q
	}
	return h
}

func TestPlanCountsHomesOnAnotherModeAndSumsTheirSizes(t *testing.T) {
	homes := []model.Home{
		home(model.HomeModePerUser, "10Gi", nil),
		home(model.HomeModePerUser, "5Gi", nil),
		home(model.HomeModeShared, "100Gi", nil),
	}
	got := Plan(homes, model.StorageChoice{Mode: model.HomeModeShared})
	want := model.MigrationStatus{From: model.HomeModePerUser, To: model.HomeModeShared, Users: 2, RequestedBytes: 15 << 30}
	if got != want {
		t.Fatalf("Plan = %+v, want %+v", got, want)
	}
}

func TestPlanIgnoresHomesWithoutAModeYet(t *testing.T) {
	got := Plan([]model.Home{home("", "10Gi", nil)}, model.StorageChoice{Mode: model.HomeModeShared})
	if got.Users != 0 || got.From != "" {
		t.Fatalf("a home with no claim yet is not a migration: %+v", got)
	}
}

func TestPlanReportsProgress(t *testing.T) {
	switched := &model.HomeMigration{Phase: model.MigrationSwitched}
	failed := &model.HomeMigration{Phase: model.MigrationFailed}
	homes := []model.Home{
		home(model.HomeModeShared, "10Gi", switched), // already moved: counts as done
		home(model.HomeModePerUser, "10Gi", failed),
		home(model.HomeModePerUser, "10Gi", nil),
	}
	got := Plan(homes, model.StorageChoice{Mode: model.HomeModeShared})
	if got.Users != 3 || got.Done != 1 || got.Failed != 1 {
		t.Fatalf("Users/Done/Failed = %d/%d/%d, want 3/1/1", got.Users, got.Done, got.Failed)
	}
}

// A switched home that is on another mode than the choice has not finished
// the round that is now asked for.
func TestPlanDoesNotCountASwitchedHomeOnAnotherModeAsDone(t *testing.T) {
	switched := &model.HomeMigration{Phase: model.MigrationSwitched}
	got := Plan([]model.Home{home(model.HomeModeShared, "10Gi", switched)}, model.StorageChoice{Mode: model.HomeModePerUser})
	if got.Users != 1 || got.Done != 0 || got.From != model.HomeModeShared || got.RequestedBytes != 10<<30 {
		t.Fatalf("Plan = %+v, want one user still to move, none done", got)
	}
}

func TestStopMinutesGrowsWithTheHomeAndRoundsUpToFive(t *testing.T) {
	const gi = int64(1) << 30
	cases := []struct {
		bytes int64
		want  int
	}{
		{0, 5},         // size unknown: the shortest estimate
		{10 * gi, 5},   // well under five minutes at 100 MiB/s
		{50 * gi, 10},  // 512 s
		{200 * gi, 35}, // 2048 s
	}
	for _, tc := range cases {
		if got := StopMinutes(tc.bytes); got != tc.want {
			t.Errorf("StopMinutes(%d GiB) = %d, want %d", tc.bytes/gi, got, tc.want)
		}
	}
}
