package model

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func quantity(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

func TestHomeModeEnum(t *testing.T) {
	s := ClusterStorageSpec{Home: HomeStorageSpec{Mode: "fast"}}
	err := s.Validate()
	if err == nil {
		t.Fatal("mode fast accepted")
	}
	for _, m := range []string{"auto", "perUser", "shared"} {
		if !strings.Contains(err.Error(), m) {
			t.Fatalf("error should list %q, got: %v", m, err)
		}
	}
}

func TestHomeMaxSize(t *testing.T) {
	s := ClusterStorageSpec{Home: HomeStorageSpec{Size: quantity("50Gi"), MaxSize: quantity("20Gi")}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "size is larger than maxSize") {
		t.Fatalf("err = %v", err)
	}
	s.Home.Size = quantity("20Gi")
	if err := s.Validate(); err != nil {
		t.Fatalf("size equal to maxSize refused: %v", err)
	}
}

func TestNegativeDurationsRefused(t *testing.T) {
	s := ClusterStorageSpec{
		Probe:     ProbeSpec{Timeout: &metav1.Duration{Duration: -time.Second}},
		Migration: MigrationSpec{KeepOldFor: &metav1.Duration{Duration: -time.Hour}},
	}
	err := s.Validate()
	for _, want := range []string{"storage.probe.timeout", "storage.migration.keepOldFor"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %s", err, want)
		}
	}
}

func TestClusterStorageDefaults(t *testing.T) {
	var s ClusterStorageSpec
	s.ApplyDefaults()
	if s.Home.Mode != HomeModeAuto || s.Home.Size.String() != "10Gi" || s.Scratch.SizeLimit.String() != "20Gi" ||
		s.SharedFolders.DefaultSize.String() != "20Gi" || s.Migration.KeepOldFor.Duration != 168*time.Hour ||
		s.Probe.Timeout.Duration != 5*time.Minute {
		t.Fatalf("defaults = %+v", s)
	}
}

// The admin's run-time requests replace the probe and the schedule, but the
// time old claims are kept stays the configured one, and a probe without a
// timeout takes the configured timeout.
func TestWithRequests(t *testing.T) {
	cfg := ClusterStorageSpec{Migration: MigrationSpec{KeepOldFor: &metav1.Duration{Duration: time.Hour}}}
	cfg.ApplyDefaults()
	at := metav1.NewTime(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	got := cfg.WithRequests(StorageRequests{
		Probe:     ProbeSpec{StorageClassName: "nfs", Nonce: "n1"},
		Migration: MigrationSpec{StartAt: &at, To: HomeModeShared, Attempt: 2, KeepOldFor: &metav1.Duration{Duration: time.Minute}},
	})
	if got.Probe.Nonce != "n1" || got.Probe.Timeout.Duration != 5*time.Minute {
		t.Errorf("probe = %+v", got.Probe)
	}
	if !got.Migration.StartAt.Equal(&at) || got.Migration.To != HomeModeShared || got.Migration.Attempt != 2 ||
		got.Migration.KeepOldFor.Duration != time.Hour {
		t.Errorf("migration = %+v", got.Migration)
	}
}

func TestHomeOwnerMustBeAName(t *testing.T) {
	if err := (&Home{Spec: HomeSpec{Owner: "Alice!"}}).Validate(); err == nil {
		t.Fatal("owner Alice! accepted")
	}
	if err := (&Home{Spec: HomeSpec{Owner: "alice-1234abcd"}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedFolderValidation(t *testing.T) {
	ok := SharedFolder{Name: "data-science", Spec: SharedFolderSpec{Group: "/org/data-science"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	long := ok
	long.Name = strings.Repeat("a", MaxFolderName+1)
	if err := long.Validate(); err == nil || !strings.Contains(err.Error(), "at most 56") {
		t.Errorf("long name: %v", err)
	}
	noGroup := ok
	noGroup.Spec.Group = ""
	if err := noGroup.Validate(); err == nil || !strings.Contains(err.Error(), "group") {
		t.Errorf("no group: %v", err)
	}
}

// The disk limit covers the scratch volume and /tmp: a lower one would evict
// a pod before either of them is full.
func TestDiskLimitMustHoldScratchAndTmp(t *testing.T) {
	s := ClusterStorageSpec{Scratch: ScratchSpec{SizeLimit: quantity("20Gi"), TmpSizeLimit: quantity("2Gi"), DiskLimit: quantity("21Gi")}}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "storage.scratch.diskLimit") {
		t.Fatalf("err = %v", err)
	}
	s.Scratch.DiskLimit = quantity("22Gi")
	if err := s.Validate(); err != nil {
		t.Fatalf("limit equal to scratch plus tmp refused: %v", err)
	}
	var d ClusterStorageSpec
	d.ApplyDefaults()
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
}
