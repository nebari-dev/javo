package storage

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockertest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/dockerhome"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const dockerVolume = "javo-javo-apps.home-" + hostOwner

// uidOf is a table of people's uids; someone missing has none yet.
type uidOf map[string]int64

func (u uidOf) UID(_ context.Context, owner string) (int64, int64, error) {
	uid, ok := u[owner]
	if !ok {
		return 0, 0, ids.ErrNoUID
	}
	return uid, 100, nil
}

// dockerHomes is a Docker home reconciler over a new database and a fake
// daemon with no volumes.
func dockerHomes(t *testing.T, uids uidOf) (*DockerHomeReconciler, *store.Store, *dockertest.Daemon) {
	t.Helper()
	db := storetest.New(t)
	d, c := dockertest.New(t)
	r := &DockerHomeReconciler{Rows: Rows{Store: db}, Volumes: dockerhome.Volumes{Client: c, Image: "example.com/javo/runner@sha256:0000"}, UIDs: uids}
	return r, db, d
}

// askedHome is a home asked for, as an app's start asks for one, and
// reconciled once.
func askedHome(t *testing.T, r *DockerHomeReconciler, db *store.Store) (model.Home, reconcile.Result) {
	t.Helper()
	asked, err := db.Homes().Ensure(ctx, "javo-apps", hostOwner, nil)
	must(t, err)
	res, err := r.Reconcile(ctx, requestFor(asked))
	must(t, err)
	home, err := db.Homes().Get(ctx, asked.ID)
	must(t, err)
	return home, res
}

func reasonOf(h model.Home) string {
	if c := meta.FindStatusCondition(h.Status.Conditions, model.ConditionReady); c != nil {
		return c.Reason
	}
	return ""
}

// A home asked for becomes a named volume that belongs to the person's
// uid, recorded where a claim's name is on Kubernetes, and ready for
// everything that waits on a home.
func TestDockerHomeBecomesAVolumeRecordedOnItsRow(t *testing.T) {
	r, db, d := dockerHomes(t, uidOf{hostOwner: 1234})
	home, res := askedHome(t, r, db)
	if res != (reconcile.Result{}) {
		t.Fatalf("Reconcile = %+v", res)
	}
	st := home.Status
	if st.ClaimName != dockerVolume || !st.Prepared || st.SizeEnforced || !meta.IsStatusConditionTrue(st.Conditions, model.ConditionReady) {
		t.Fatalf("status = %+v, want the volume %s, prepared and ready, its size not enforced", st, dockerVolume)
	}
	vol, ok := d.Volume(dockerVolume)
	if !ok || vol.UID != 1234 || vol.GID != 100 || vol.Handed != 1 {
		t.Fatalf("volume = %+v, %v; want one owned by 1234:100", vol, ok)
	}
	if vol.Labels[docker.LabelOwner] != hostOwner || vol.Labels[docker.LabelManaged] != "true" {
		t.Errorf("labels = %v", vol.Labels)
	}

	// An app planned for Docker mounts it: the home volume's source is
	// the named volume.
	layout := podstorage.Build(hostOwner, podstorage.Input{Home: &home})
	if got := layout.Volumes[0]; got.Name != podstorage.HomeVolume || got.Source != dockerVolume {
		t.Errorf("home volume = %+v, want source %s", got, dockerVolume)
	}

	// A pass over a ready home writes nothing and hands nothing over
	// again: what the person made of their home's root stays.
	if _, err := r.Reconcile(ctx, requestFor(home)); err != nil {
		t.Fatal(err)
	}
	if again, err := db.Homes().Get(ctx, home.ID); err != nil || again.Version != home.Version {
		t.Errorf("version %d after a second pass, was %d (%v)", again.Version, home.Version, err)
	}
	if vol, _ := d.Volume(dockerVolume); vol.Handed != 1 {
		t.Errorf("the volume was handed over %d times", vol.Handed)
	}
}

// A volume that has the home's name and is not labelled as this person's
// is left alone, and the home says whose fault that is, as a claim of
// another account does on Kubernetes.
func TestDockerHomeRefusesAVolumeOfSomeoneElse(t *testing.T) {
	r, db, d := dockerHomes(t, uidOf{hostOwner: 1234})
	d.Put(dockerVolume, map[string]string{docker.LabelManaged: "true", docker.LabelNamespace: "javo-apps", docker.LabelOwner: "bob-bbbbbbbb", dockerhome.LabelStorage: dockerhome.Home})
	home, _ := askedHome(t, r, db)
	if home.Status.ClaimName != "" || home.Status.Prepared || reasonOf(home) != string(reasons.HomeClaimForeign) {
		t.Fatalf("status = %+v, want no volume, not prepared and HomeClaimForeign", home.Status)
	}
	if vol, _ := d.Volume(dockerVolume); vol.Handed != 0 || vol.Labels[docker.LabelOwner] != "bob-bbbbbbbb" {
		t.Errorf("the other person's volume was touched: %+v", vol)
	}
}

// Until the person has a uid the volume is made and handed to no one, so
// nothing mounts it.
func TestDockerHomeWaitsForTheOwnersUID(t *testing.T) {
	uids := uidOf{}
	r, db, d := dockerHomes(t, uids)
	home, res := askedHome(t, r, db)
	if home.Status.Prepared || reasonOf(home) != string(reasons.IdentityPending) || res.RequeueAfter <= 0 {
		t.Fatalf("status = %+v, result %+v; want IdentityPending and another look", home.Status, res)
	}
	if vol, _ := d.Volume(dockerVolume); vol.Handed != 0 {
		t.Error("the volume was handed over without a uid")
	}
	uids[hostOwner] = 4321
	home, _ = askedHome(t, r, db)
	if vol, _ := d.Volume(dockerVolume); !home.Status.Prepared || vol.UID != 4321 {
		t.Fatalf("status = %+v, volume %+v; want prepared and owned by 4321", home.Status, vol)
	}
}

// A daemon that does not answer leaves the home not ready with what it
// said, and is asked again.
func TestDockerHomeSaysWhyWhenTheDaemonFails(t *testing.T) {
	r, db, d := dockerHomes(t, uidOf{hostOwner: 1234})
	d.Down(true)
	home, res := askedHome(t, r, db)
	if home.Status.Prepared || home.Status.ClaimName != "" || reasonOf(home) != string(reasons.HomePending) || res.RequeueAfter <= 0 {
		t.Fatalf("status = %+v, result %+v; want HomePending and another look", home.Status, res)
	}
	d.Down(false)
	if home, _ = askedHome(t, r, db); !home.Status.Prepared || reasonOf(home) != reasonReady {
		t.Fatalf("status = %+v, want ready once the daemon answers", home.Status)
	}
}

// A volume removed behind Javo's back is made again and handed to the
// person again: a fresh volume belongs to root.
func TestDockerHomeIsHandedOverAgainWhenItsVolumeWasRemoved(t *testing.T) {
	r, db, d := dockerHomes(t, uidOf{hostOwner: 1234})
	askedHome(t, r, db)
	d.Remove(dockerVolume)
	home, _ := askedHome(t, r, db)
	if vol, ok := d.Volume(dockerVolume); !ok || vol.UID != 1234 || !home.Status.Prepared {
		t.Fatalf("volume = %+v, %v, status %+v; want it back and owned by 1234", vol, ok, home.Status)
	}
}

// Removing a person's home row removes no volume: their files stay, and
// are theirs again when they come back.
func TestDockerHomeVolumeOutlivesItsRow(t *testing.T) {
	r, db, d := dockerHomes(t, uidOf{hostOwner: 1234})
	home, _ := askedHome(t, r, db)
	must(t, db.Homes().Delete(ctx, model.Actor{System: "test"}, home.ID))
	if res, err := r.Reconcile(ctx, requestFor(home)); err != nil || res != (reconcile.Result{}) {
		t.Fatalf("Reconcile of a deleted home = %+v, %v", res, err)
	}
	if _, ok := d.Volume(dockerVolume); !ok {
		t.Fatal("the volume went with the row")
	}
	back, _ := askedHome(t, r, db)
	if vol, _ := d.Volume(dockerVolume); back.Status.ClaimName != dockerVolume || !back.Status.Prepared || len(d.Names()) != 1 {
		t.Fatalf("status = %+v, volume %+v of %v; want the same volume adopted", back.Status, vol, d.Names())
	}
}

// Where group folders are not offered, a folder asked for says so and why
// on its status, is never mounted, and no volume is made for it.
func TestAFolderWhereNoneIsOfferedSaysSo(t *testing.T) {
	db := storetest.New(t)
	d, _ := dockertest.New(t)
	r := &NoFolderReconciler{Rows: Rows{Store: db}, Why: "apps run as Docker containers here"}
	f, err := db.SharedFolders().Create(ctx, model.Actor{Person: "admin"}, model.SharedFolder{
		Name: "team", Namespace: "javo-apps", Spec: model.SharedFolderSpec{Group: "team"},
	})
	must(t, err)
	req := reconcile.Request{}
	req.Name = f.ID
	if res, err := r.Reconcile(ctx, req); err != nil || res != (reconcile.Result{}) {
		t.Fatalf("Reconcile = %+v, %v", res, err)
	}
	got, err := db.SharedFolders().Get(ctx, f.ID)
	must(t, err)
	ready := meta.FindStatusCondition(got.Status.Conditions, model.ConditionReady)
	if ready == nil || ready.Reason != string(reasons.SharedFoldersUnavailable) || !strings.Contains(ready.Message, "Docker") {
		t.Fatalf("Ready = %+v, want SharedFoldersUnavailable naming Docker", ready)
	}
	if got.Status.ClaimName != "" || got.Status.Prepared || podstorage.FolderReady(got) || len(d.Names()) != 0 {
		t.Fatalf("status = %+v with volumes %v; want nothing made and nothing to mount", got.Status, d.Names())
	}
	// A second pass writes nothing.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if again, err := db.SharedFolders().Get(ctx, f.ID); err != nil || again.Version != got.Version {
		t.Errorf("version %d after a second pass, was %d (%v)", again.Version, got.Version, err)
	}
}
