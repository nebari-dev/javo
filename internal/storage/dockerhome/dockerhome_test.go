package dockerhome_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockertest"
	"github.com/nebari-dev/javo/internal/storage/dockerhome"
)

const (
	ns    = "javo-apps"
	alice = "alice-aaaaaaaa"
	bob   = "bob-bbbbbbbb"
)

var ctx = context.Background()

func volumes(t *testing.T) (dockerhome.Volumes, *dockertest.Daemon) {
	t.Helper()
	d, c := dockertest.New(t)
	return dockerhome.Volumes{Client: c, Image: "example.com/javo/runner@sha256:0000"}, d
}

// A person's home is one named volume, called after the namespace and the
// person, that says on its labels it is Javo's and whose home it is.
func TestEnsureMakesAVolumeLabelledAsThePersonsHome(t *testing.T) {
	v, d := volumes(t)
	name, fresh, err := v.Ensure(ctx, ns, alice)
	if err != nil || !fresh || name != "javo-javo-apps.home-alice-aaaaaaaa" {
		t.Fatalf("Ensure = %q, %v, %v", name, fresh, err)
	}
	got, ok := d.Volume(name)
	if !ok {
		t.Fatal("no volume was made")
	}
	want := map[string]string{docker.LabelManaged: "true", docker.LabelNamespace: ns, docker.LabelOwner: alice, dockerhome.LabelStorage: dockerhome.Home}
	for k, w := range want {
		if got.Labels[k] != w {
			t.Errorf("label %s = %q, want %q", k, got.Labels[k], w)
		}
	}
	// It is no workload's: what ends a workload never takes it along.
	if got.Labels[docker.LabelEnds] != "" || got.Labels[docker.LabelName] != "" {
		t.Errorf("labels %v name a workload", got.Labels)
	}

	// Asked again, it is the same volume and not a new one.
	again, fresh, err := v.Ensure(ctx, ns, alice)
	if err != nil || fresh || again != name || len(d.Names()) != 1 {
		t.Fatalf("second Ensure = %q, %v, %v with volumes %v", again, fresh, err, d.Names())
	}
}

// Two people, and one person in two namespaces, never share a volume.
func TestEveryPersonAndNamespaceHasAVolumeOfItsOwn(t *testing.T) {
	v, _ := volumes(t)
	seen := map[string]string{}
	for _, who := range [][2]string{{ns, alice}, {ns, bob}, {"javo-other", alice}, {"javo", "apps-" + alice}} {
		name, _, err := v.Ensure(ctx, who[0], who[1])
		if err != nil {
			t.Fatal(err)
		}
		if other, taken := seen[name]; taken {
			t.Errorf("%v and %s share the volume %s", who, other, name)
		}
		seen[name] = who[0] + "/" + who[1]
	}
}

// A volume that has the home's name and is not labelled as that person's
// home is never used, written to or relabelled.
func TestEnsureRefusesAVolumeThatIsNotThePersons(t *testing.T) {
	name, err := dockerhome.Name(ns, alice)
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]string{docker.LabelManaged: "true", docker.LabelNamespace: ns, docker.LabelOwner: alice, dockerhome.LabelStorage: dockerhome.Home}
	without := func(k string) map[string]string {
		out := map[string]string{}
		for key, v := range mine {
			if key != k {
				out[key] = v
			}
		}
		return out
	}
	with := func(k, v string) map[string]string { out := without(k); out[k] = v; return out }
	for what, labels := range map[string]map[string]string{
		"someone's own volume":       nil,
		"another person's":           with(docker.LabelOwner, bob),
		"another namespace's":        with(docker.LabelNamespace, "javo-other"),
		"not Javo's":                 without(docker.LabelManaged),
		"a workload's, not a home":   without(dockerhome.LabelStorage),
		"a group folder's, no home":  with(dockerhome.LabelStorage, "folder"),
		"a workload's ending volume": with(docker.LabelEnds, "true"),
	} {
		v, d := volumes(t)
		d.Put(name, labels)
		if _, _, err := v.Ensure(ctx, ns, alice); !errors.Is(err, dockerhome.ErrForeign) {
			t.Errorf("%s: Ensure = %v, want ErrForeign", what, err)
		}
		if err := v.Own(ctx, ns, alice, 1000, 100); !errors.Is(err, dockerhome.ErrForeign) {
			t.Errorf("%s: Own = %v, want ErrForeign", what, err)
		}
		if found, theirs, err := v.Owned(ctx, ns, alice, name); err != nil || !found || theirs {
			t.Errorf("%s: Owned = %v, %v, %v; want found and not theirs", what, found, theirs, err)
		}
		if got, _ := d.Volume(name); got.Handed != 0 {
			t.Errorf("%s: the volume was handed over", what)
		}
	}
}

// The volume's root is handed to the person's uid and gid, closed to
// everyone else, without anything running for it.
func TestOwnGivesTheVolumeToThePerson(t *testing.T) {
	v, d := volumes(t)
	name, _, err := v.Ensure(ctx, ns, alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Own(ctx, ns, alice, 1234, 100); err != nil {
		t.Fatal(err)
	}
	got, _ := d.Volume(name)
	if got.UID != 1234 || got.GID != 100 || got.Mode != 0o700 {
		t.Errorf("the root belongs to %d:%d with mode %o, want 1234:100 and 700", got.UID, got.GID, got.Mode)
	}
	// Root never owns a home.
	if err := v.Own(ctx, ns, alice, 0, 0); err == nil {
		t.Error("a home was handed to root")
	}
}

// Owned tells the gateway whether the volume a home's row names is that
// person's home.
func TestOwnedKnowsWhoseHomeAVolumeIs(t *testing.T) {
	v, _ := volumes(t)
	name, _, err := v.Ensure(ctx, ns, alice)
	if err != nil {
		t.Fatal(err)
	}
	if found, theirs, err := v.Owned(ctx, ns, alice, name); err != nil || !found || !theirs {
		t.Errorf("alice's own = %v, %v, %v", found, theirs, err)
	}
	if found, theirs, err := v.Owned(ctx, ns, bob, name); err != nil || !found || theirs {
		t.Errorf("bob asking for alice's = %v, %v, %v", found, theirs, err)
	}
	if found, _, err := v.Owned(ctx, ns, alice, "javo-javo-apps.home-nobody"); err != nil || found {
		t.Errorf("a volume that is not there = %v, %v", found, err)
	}
}

// A name that could leave the place volumes are named in makes nothing.
func TestNamesThatAreNotNamesMakeNothing(t *testing.T) {
	v, d := volumes(t)
	for _, bad := range [][2]string{{ns, "../etc"}, {ns, "a.b"}, {ns, ""}, {"a/b", alice}, {"", alice}, {ns, "/var/lib/docker"}} {
		if name, _, err := v.Ensure(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("Ensure(%q, %q) = %q, want an error", bad[0], bad[1], name)
		}
	}
	if len(d.Names()) != 0 {
		t.Errorf("volumes were made: %v", d.Names())
	}
}
