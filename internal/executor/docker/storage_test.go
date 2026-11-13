package docker

import (
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

// Storage is named after its namespace and its own name, and can never be
// taken for a volume of a workload, whatever the workload is called.
func TestStorageNameIsNoWorkloadsVolume(t *testing.T) {
	got, err := StorageName("javo-apps", "home-alice-aaaaaaaa")
	if err != nil || got != "javo-javo-apps.home-alice-aaaaaaaa" {
		t.Fatalf("StorageName = %q, %v", got, err)
	}
	own, err := VolumeName(workload.Ref{Namespace: "javo-apps", Name: "home-alice-aaaaaaaa"}, "tmp")
	if err != nil || own == got {
		t.Fatalf("a workload's volume is %q, %v", own, err)
	}
	for _, bad := range [][2]string{{"javo-apps", "home.alice"}, {"javo.apps", "home-alice"}, {"javo-apps", "../etc"}, {"", "home-alice"}, {"javo-apps", "/var/lib"}} {
		if name, err := StorageName(bad[0], bad[1]); err == nil {
			t.Errorf("StorageName(%q, %q) = %q, want an error", bad[0], bad[1], name)
		}
	}
}
