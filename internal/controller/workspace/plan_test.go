package workspace

import (
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

func homeSpec() workload.Spec {
	s := workload.Spec{Main: MainUnit("alice", "example.com/app:1")}
	AddLayout(&s, testLayout(FixedUID))
	return s
}

func TestScratchHomeKeepsTheOwnersHomeAwayFromTheApp(t *testing.T) {
	s := homeSpec()
	scratchHome(&s)

	for _, v := range s.Volumes {
		if v.Source != "" {
			t.Fatalf("volume %s is still the owner's storage", v.Name)
		}
	}
	at, ok := s.Main.MountAt(homeDir)
	if !ok || at.Volume != podspec.HomeScratchVolume {
		t.Fatalf("the app has %+v at the home path", at)
	}
	if !slices.ContainsFunc(s.Volumes, func(v workload.Volume) bool { return v.Name == at.Volume }) {
		t.Fatalf("volume %s does not exist", at.Volume)
	}
}

func TestScratchHomeKeepsTheHomeForAStepThatReadsIt(t *testing.T) {
	s := homeSpec()
	s.Steps = []workload.Unit{{Name: "site-copy", Mounts: []workload.Mount{{Volume: homeVolume, Path: homeDir, ReadOnly: true}}}}
	scratchHome(&s)

	if s.Steps[0].Mounts[0].Volume != homeVolume {
		t.Fatal("the step lost its home mount")
	}
	owned := 0
	for _, v := range s.Volumes {
		if v.Source != "" {
			owned++
		}
	}
	if owned != 1 {
		t.Fatalf("%d volumes of the owner's storage, want the one the step reads", owned)
	}
	if slices.ContainsFunc(s.Main.Mounts, func(m workload.Mount) bool { return m.Volume == homeVolume }) {
		t.Fatal("the app still mounts the home")
	}
}

// Images set no HOME a workload can rely on (some run as a passwd user with
// another home): "~" must be the mounted volume. pod-user.sh names an
// allocated uid's passwd entry after JAVO_USER, so the shell prompt, whoami
// and git show the person, not "I have no name!".
func TestTheMainUnitNamesTheHomeAndTheOwner(t *testing.T) {
	u := MainUnit("alice-1a2b3c4d", "example.com/lab:1")
	if u.Name != workload.MainUnit || u.Image != "example.com/lab:1" || u.Command != nil || u.Args != nil {
		t.Fatalf("unit = %+v", u)
	}
	if got := u.EnvValue("HOME"); got != homeDir {
		t.Errorf("HOME = %q, want %q", got, homeDir)
	}
	if got := u.EnvValue("JAVO_USER"); got != "alice" {
		t.Errorf("JAVO_USER = %q, want alice", got)
	}
}
