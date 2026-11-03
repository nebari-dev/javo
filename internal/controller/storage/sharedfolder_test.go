package storage

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
)

// gids is a fixed group to gid table; a missing group has no gid yet.
type gids map[string]int64

func (g gids) GID(_ context.Context, group string) (int64, bool, error) {
	v, ok := g[group]
	return v, ok, nil
}

func folderReconciler(t *testing.T, c client.Client, g gids) *SharedFolderReconciler {
	return &SharedFolderReconciler{
		Client: c, Reader: c, Rows: testRows(t), Image: testImage, GIDs: g, Backends: testBackends(),
	}
}

var folderSeq atomic.Int32

// createFolder stores f as an admin would.
func createFolder(t *testing.T, f *model.SharedFolder) *model.SharedFolder {
	t.Helper()
	got, err := env(t).db.SharedFolders().Create(ctx, model.Actor{Person: "admin"}, *f)
	must(t, err)
	return &got
}

// newFolder makes a folder with a name of its own: claims made in envtest
// are never really removed.
func newFolder(t *testing.T, group string, mutate func(*model.SharedFolder)) *model.SharedFolder {
	t.Helper()
	f := &model.SharedFolder{
		Name: fmt.Sprintf("%s-%d", naming.GroupDir(group), folderSeq.Add(1)), Namespace: homeNS,
		Spec: model.SharedFolderSpec{Group: group},
	}
	if mutate != nil {
		mutate(f)
	}
	return createFolder(t, f)
}

func reconcileFolder(t *testing.T, r *SharedFolderReconciler, f *model.SharedFolder) {
	t.Helper()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: f.ID}}); err != nil {
		t.Fatal(err)
	}
}

func getFolder(t *testing.T, f *model.SharedFolder) *model.SharedFolder {
	t.Helper()
	out, err := env(t).db.SharedFolders().Get(ctx, f.ID)
	must(t, err)
	return &out
}

func folderReady(t *testing.T, f *model.SharedFolder) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(getFolder(t, f).Status.Conditions, model.ConditionReady)
	if c == nil {
		t.Fatal("no Ready condition on the folder")
	}
	return c
}

func folderJobs(t *testing.T, f *model.SharedFolder) []batchv1.Job {
	t.Helper()
	var l batchv1.JobList
	must(t, env(t).k8s.List(ctx, &l, client.InNamespace(homeNS), client.MatchingLabels{
		model.LabelRole: model.RoleFolderPrepare, model.LabelGroupFolder: f.Name}))
	return l.Items
}

// folderStorage is homeStorage plus the class chosen for shared folders.
func folderStorage(t *testing.T, mode model.HomeMode, nodes int32, sharedClass string, mutate func(*model.ClusterStorage)) {
	t.Helper()
	homeStorage(t, storageOpts{mode: mode, class: "fast", nodes: nodes})
	cs := storageNow(t)
	cs.Status.Choice.SharedClass = sharedClass
	setStatus(t, cs.Status)
	if mutate != nil {
		mutate(cs)
		env(t).cfg = cs.Spec
	}
}

func TestAFolderGetsASharedClaimAndOnePrepareJobThatMountsOnlyIt(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/data-science", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/data-science": 2001})
	reconcileFolder(t, r, f)
	reconcileFolder(t, r, f) // a second pass must not make a second Job

	got := getFolder(t, f)
	if got.Status.GID != 2001 || got.Status.ClaimName != naming.FolderClaimName(f.Name) {
		t.Fatalf("status = %+v", got.Status)
	}
	claim := getClaim(t, got.Status.ClaimName)
	if len(claim.Spec.AccessModes) != 1 || claim.Spec.AccessModes[0] != corev1.ReadWriteMany || *claim.Spec.StorageClassName != "nfs" {
		t.Fatalf("claim = %+v", claim.Spec)
	}
	if size := claim.Spec.Resources.Requests[corev1.ResourceStorage]; size.Cmp(resource.MustParse("20Gi")) != 0 {
		t.Fatalf("size = %s, want the 20Gi default", size.String())
	}
	if c := folderReady(t, f); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.FolderPreparing) {
		t.Fatalf("Ready = %+v", c)
	}

	jobs := folderJobs(t, f)
	if len(jobs) != 1 {
		t.Fatalf("prepare jobs = %d, want 1", len(jobs))
	}
	spec := jobs[0].Spec.Template.Spec
	if len(spec.Volumes) != 1 || spec.Volumes[0].PersistentVolumeClaim.ClaimName != got.Status.ClaimName {
		t.Fatalf("volumes = %+v, want only the folder claim", spec.Volumes)
	}
	sc := spec.SecurityContext
	if sc.FSGroup == nil || *sc.FSGroup != 2001 || *sc.RunAsUser != 65532 || *sc.RunAsGroup != 2001 || !*sc.RunAsNonRoot {
		t.Fatalf("security context = %+v", sc)
	}
	if jobs[0].Labels[model.LabelGroupFolder] != got.Name || len(jobs[0].OwnerReferences) != 0 {
		t.Fatal("the Job does not name its folder by label")
	}
	if spec.Containers[0].Image != testImage.Ref || !pullsTestImage(spec) {
		t.Fatalf("image %q, pull secrets %v", spec.Containers[0].Image, spec.ImagePullSecrets)
	}
	args := strings.Join(spec.Containers[0].Args, " ")
	if want := "storage prepare --dir /vol/data --uid 65532 --gid 2001 --mode 02775"; args != want {
		t.Fatalf("args = %q, want %q", args, want)
	}
	if strings.Contains(args, "--no-mkdir") {
		t.Fatal("the folder directory must be created")
	}
}

func TestAFolderIsReadyOnceItsPrepareJobSucceeds(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/research", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/research": 2002})
	reconcileFolder(t, r, f)
	finishJob(t, folderJobs(t, f)[0], true, "")
	reconcileFolder(t, r, f)

	got := getFolder(t, f)
	if !got.Status.Prepared {
		t.Fatal("not prepared after the Job succeeded")
	}
	if c := folderReady(t, f); c.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestAFolderWhoseGroupHasNoGidWaitsForIt(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/new-group", nil)
	r := folderReconciler(t, env(t).k8s, gids{})
	reconcileFolder(t, r, f)

	if c := folderReady(t, f); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.GroupHasNoId) {
		t.Fatalf("Ready = %+v", c)
	}
	var claims corev1.PersistentVolumeClaimList
	must(t, env(t).k8s.List(ctx, &claims, client.InNamespace(homeNS), client.MatchingLabels{model.LabelGroupFolder: f.Name}))
	if len(claims.Items) != 0 || len(folderJobs(t, f)) != 0 {
		t.Fatalf("made %d claims and %d jobs without a gid", len(claims.Items), len(folderJobs(t, f)))
	}

	r.GIDs = gids{"/org/new-group": 2003}
	reconcileFolder(t, r, f)
	if got := getFolder(t, f); got.Status.GID != 2003 {
		t.Fatalf("gid = %d after allocation", got.Status.GID)
	}
}

// addClasses records classes the inspection found, by name and whether each
// can be mounted by many machines.
func addClasses(t *testing.T, rwx map[string]bool) {
	t.Helper()
	cs := storageNow(t)
	for name, ok := range rwx {
		cs.Status.Classes = append(cs.Status.Classes, model.ClassCapability{
			Name: name, RWXFile: fact(ok), PosixOwnership: fact(true),
			SizeEnforced: fact(false), Expansion: fact(false), Snapshots: fact(false)})
	}
	setStatus(t, cs.Status)
}

func TestFolderClassComesFromTheFolderThenTheAdminThenTheChoice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		folderCls string
		adminCls  string
		want      string
	}{
		{"folder beats admin", "own", "admin", "own"},
		{"admin beats choice", "", "admin", "admin"},
		{"choice", "", "", "nfs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folderStorage(t, model.HomeModeShared, 3, "nfs", func(cs *model.ClusterStorage) {
				cs.Spec.SharedFolders.StorageClassName = tc.adminCls
			})
			addClasses(t, map[string]bool{"own": true, "admin": true})
			f := newFolder(t, "/org/classes", func(f *model.SharedFolder) { f.Spec.StorageClassName = tc.folderCls })
			reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/classes": 2004}), f)
			claim := getClaim(t, getFolder(t, f).Status.ClaimName)
			if got := *claim.Spec.StorageClassName; got != tc.want {
				t.Fatalf("class = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestFoldersOnAPerUserClusterNeedAnSharedClassUnlessThereIsOneNode(t *testing.T) {
	t.Parallel()
	t.Run("many nodes, no shared class: nothing is made", func(t *testing.T) {
		folderStorage(t, model.HomeModePerUser, 3, "", nil)
		f := newFolder(t, "/org/many", nil)
		reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/many": 2005}), f)
		if c := folderReady(t, f); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.SharedFoldersUnavailable) {
			t.Fatalf("Ready = %+v", c)
		}
		if got := getFolder(t, f); got.Status.ClaimName != "" {
			t.Fatalf("claim %s made on a multi-node cluster with no shared class", got.Status.ClaimName)
		}
	})
	t.Run("one node: a ReadWriteOnce claim on the home class", func(t *testing.T) {
		folderStorage(t, model.HomeModePerUser, 1, "", nil)
		f := newFolder(t, "/org/single", nil)
		reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/single": 2006}), f)
		claim := getClaim(t, getFolder(t, f).Status.ClaimName)
		if claim.Spec.AccessModes[0] != corev1.ReadWriteOnce || *claim.Spec.StorageClassName != "fast" {
			t.Fatalf("claim = %+v", claim.Spec)
		}
	})
}

func TestAFailedPrepareJobShowsTheStatResultAsFolderPermissionsUnsupported(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/denied", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/denied": 2001})
	reconcileFolder(t, r, f)
	finishJob(t, folderJobs(t, f)[0], false, "group 0, want 2001")
	reconcileFolder(t, r, f)

	c := folderReady(t, f)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.FolderPermissionsUnsupported) || !strings.Contains(c.Message, "group 0, want 2001") {
		t.Fatalf("Ready = %+v", c)
	}
	if getFolder(t, f).Status.Prepared {
		t.Fatal("a failed prepare must not mark the folder prepared")
	}
}

func TestFoldersAreNotOfferedAcrossPerUserNamespaces(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/spanning", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/spanning": 2007})
	r.PerUserNamespaces = true
	reconcileFolder(t, r, f)

	c := folderReady(t, f)
	if c.Reason != string(reasons.SharedFoldersUnavailable) || !strings.Contains(c.Message, "cannot span namespaces") {
		t.Fatalf("Ready = %+v", c)
	}
	if got := getFolder(t, f); got.Status.ClaimName != "" || len(folderJobs(t, f)) != 0 {
		t.Fatalf("a folder was built in per-user namespace mode: %+v", got.Status)
	}
}

func TestAFolderClaimNameTakenByAnotherObjectIsNotUsed(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/taken", nil)
	stranger := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: naming.FolderClaimName(f.Name), Namespace: homeNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	must(t, env(t).k8s.Create(ctx, stranger))
	reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/taken": 2008}), f)

	if got := getFolder(t, f); got.Status.ClaimName != "" || len(folderJobs(t, f)) != 0 {
		t.Fatalf("used a claim not labelled for this folder: %+v", got.Status)
	}
	if c := folderReady(t, f); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.FolderClaimForeign) {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestAFolderClassThatCannotBeSharedIsNotMadeOnSeveralNodes(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	addClasses(t, map[string]bool{"block": false})
	f := newFolder(t, "/org/block", func(f *model.SharedFolder) { f.Spec.StorageClassName = "block" })
	reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/block": 2010}), f)

	if c := folderReady(t, f); c.Reason != string(reasons.SharedFoldersUnavailable) {
		t.Fatalf("Ready = %+v", c)
	}
	if got := getFolder(t, f); got.Status.ClaimName != "" {
		t.Fatalf("claim %s asked for ReadWriteMany on a class that cannot give it", got.Status.ClaimName)
	}
}

func TestAFolderWithTheLongestNameGetsAPrepareJob(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := createFolder(t, &model.SharedFolder{
		Name: strings.Repeat("l", 55) + "z", Namespace: homeNS,
		Spec: model.SharedFolderSpec{Group: "/org/long"},
	})
	reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/long": 2011}), f)

	if c := folderReady(t, f); c.Reason != string(reasons.FolderPreparing) {
		t.Fatalf("Ready = %+v", c)
	}
	jobs := folderJobs(t, f)
	if len(jobs) != 1 || len(jobs[0].Name) > 63 {
		t.Fatalf("jobs = %d, name %q", len(jobs), jobs[0].Name)
	}
}

// A setup that can never work is not retried for ever: after the last
// attempt the Job stays as the evidence and no new one is made.
func TestAFolderWhosePrepareKeepsFailingStopsGettingNewJobs(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/stuck", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/stuck": 2012})
	for i := 0; i < maxPrepareAttempts; i++ {
		reconcileFolder(t, r, f)
		j := folderJobs(t, f)[0]
		said := ""
		if i == maxPrepareAttempts-1 {
			said = "group 0, want 2012"
		}
		finishJob(t, j, false, said)
		reconcileFolder(t, r, f)
		if i < maxPrepareAttempts-1 {
			must(t, env(t).k8s.Delete(ctx, &j, client.PropagationPolicy(metav1.DeletePropagationBackground))) // what the TTL does
		}
	}
	last := folderJobs(t, f)[0]
	if last.Spec.TTLSecondsAfterFinished != nil {
		t.Fatalf("the last attempt's Job must outlive its TTL as evidence, TTL = %d", *last.Spec.TTLSecondsAfterFinished)
	}
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: f.ID}})
	must(t, err)
	c := folderReady(t, f)
	if c.Reason != string(reasons.FolderPermissionsUnsupported) || !strings.Contains(c.Message, "group 0, want 2012") {
		t.Fatalf("Ready = %+v", c)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("no retry is scheduled after the last attempt, got %v", res.RequeueAfter)
	}
}

func TestAFolderJobThatNeverRanIsNotBlamedOnPermissions(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/neverran", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/neverran": 2013})
	reconcileFolder(t, r, f)
	finishJob(t, folderJobs(t, f)[0], false, "") // deadline hit, no container report
	reconcileFolder(t, r, f)

	if c := folderReady(t, f); c.Reason != string(reasons.FolderPrepareFailed) {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestAFolderClaimLeftByAnotherGroupIsNotTakenOver(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	mk := func(group string) *model.SharedFolder {
		return createFolder(t, &model.SharedFolder{Name: "reborn", Namespace: homeNS, Spec: model.SharedFolderSpec{Group: group}})
	}
	first := mk("/org/reborn-a")
	reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/reborn-a": 2101}), first)
	if got := getFolder(t, first); got.Status.ClaimName == "" {
		t.Fatalf("first folder got no claim: %+v", got.Status)
	}
	jobsBefore := len(folderJobs(t, first))
	must(t, env(t).db.SharedFolders().Delete(ctx, model.Actor{Person: "admin"}, first.ID)) // the claim outlives the folder

	second := mk("/org/reborn-b")
	reconcileFolder(t, folderReconciler(t, env(t).k8s, gids{"/org/reborn-b": 2102}), second)

	if got := getFolder(t, second); got.Status.ClaimName != "" || len(folderJobs(t, second)) != jobsBefore {
		t.Fatalf("took over another group's claim: %+v", got.Status)
	}
	if c := folderReady(t, second); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.FolderClaimForeign) {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestAFolderWhoseClaimIsGoneIsNotReady(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/lost-claim", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/lost-claim": 2103})
	reconcileFolder(t, r, f)
	got := getFolder(t, f)
	got.Status.ClaimName, got.Status.Prepared = "folder-missing", true
	_, err := env(t).db.SharedFolders().Update(ctx, *got, got.Version)
	must(t, err)

	reconcileFolder(t, r, f)
	c := folderReady(t, f)
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.FolderPending) || !strings.Contains(c.Message, "folder-missing not found") {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestTwoFoldersWithTheSameMountDirectoryDoNotBothGetOne(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	mk := func(name, group string) *model.SharedFolder {
		return createFolder(t, &model.SharedFolder{Name: name, Namespace: homeNS, Spec: model.SharedFolderSpec{Group: group}})

	}
	a, b := mk("dup-a", "/a/dup-dir"), mk("dup-b", "/b/dup-dir")
	r := folderReconciler(t, env(t).k8s, gids{"/a/dup-dir": 2104, "/b/dup-dir": 2105})
	reconcileFolder(t, r, a)
	reconcileFolder(t, r, b)

	if got := getFolder(t, a); got.Status.ClaimName == "" {
		t.Fatalf("the first folder was refused: %+v", got.Status)
	}
	got := getFolder(t, b)
	c := folderReady(t, b)
	if got.Status.ClaimName != "" || c.Reason != string(reasons.FolderDirTaken) || !strings.Contains(c.Message, "/a/dup-dir") {
		t.Fatalf("second folder: status %+v, Ready %+v", got.Status, c)
	}
}

// A folder whose volume never mounts fails with the kubelet's words instead of
// staying "setting up".
func TestAFolderWhoseVolumeKeepsFailingToMountFails(t *testing.T) {
	t.Parallel()
	folderStorage(t, model.HomeModeShared, 3, "nfs", nil)
	f := newFolder(t, "/org/nomount", nil)
	r := folderReconciler(t, env(t).k8s, gids{"/org/nomount": 2014})
	reconcileFolder(t, r, f)
	podWarning(t, jobPod(t, folderJobs(t, f)[0]), "FailedMount", noNFSHelper, time.Now().Add(-5*time.Minute))
	reconcileFolder(t, r, f)

	if c := folderReady(t, f); c.Reason != string(reasons.FolderPrepareFailed) || !strings.Contains(c.Message, "helper program") {
		t.Fatalf("Ready = %+v", c)
	}
}
