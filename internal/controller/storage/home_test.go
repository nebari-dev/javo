package storage

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/features/storage/peruser"
	"github.com/nebari-dev/javo/internal/features/storage/shared"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
)

// testImage is a private platform image: every Job that runs it must pull it
// with its Secret.
var testImage = podspec.Image{Ref: "javo:test", PullSecrets: []string{"ghcr"}}

func pullsTestImage(spec corev1.PodSpec) bool {
	return slices.Equal(spec.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "ghcr"}})
}

const homeNS = "default"

var homeSeq int

// uniqueOwner gives each test its own owner: claims made in envtest are never
// really removed (nothing clears their protection finalizer).
func uniqueOwner(prefix string) string {
	homeSeq++
	return fmt.Sprintf("%s-%02d", prefix, homeSeq)
}

type storageOpts struct {
	mode         model.HomeMode
	class        string
	expandable   bool
	sizeEnforced bool
	nodes        int32
	maxSize      string
}

func fact(v bool) model.Fact { return model.Fact{Value: v, Provenance: model.ProvenanceKnown} }

// homeStorage makes the storage the home reconciler reads: the choice and
// the capabilities of the chosen class.
func homeStorage(t *testing.T, o storageOpts) {
	t.Helper()
	clean(t)
	if o.maxSize != "" {
		q := resource.MustParse(o.maxSize)
		cfg.Home.MaxSize = &q
	}
	setStatus(t, model.ClusterStorageStatus{
		Nodes: o.nodes,
		Classes: []model.ClassCapability{{
			Name: o.class, RWXFile: fact(o.mode == model.HomeModeShared), PosixOwnership: fact(true),
			SizeEnforced: fact(o.sizeEnforced), Expansion: fact(o.expandable), Snapshots: fact(false),
		}},
		Choice: &model.StorageChoice{Mode: o.mode, HomeClass: o.class, SingleNode: o.nodes == 1},
	})
}

func newHome(t *testing.T, owner, size string) *model.Home {
	t.Helper()
	var q *resource.Quantity
	if size != "" {
		v := resource.MustParse(size)
		q = &v
	}
	h, err := db.Homes().Ensure(ctx, homeNS, owner, q)
	must(t, err)
	return &h
}

// testBackends are both storage backends by mode.
func testBackends() map[model.HomeMode]storage.Backend {
	return map[model.HomeMode]storage.Backend{
		model.HomeModePerUser: peruser.New(),
		model.HomeModeShared:  shared.New(),
	}
}

// fixedUIDs gives everyone the fixed ids, as identity.uidMode fixed does.
func fixedUIDs() ids.UserIDs {
	return ids.People{People: db.People(store.IDRange{}), Mode: config.UIDModeFixed}
}

func homeReconciler(c client.Client) *HomeReconciler {
	return &HomeReconciler{
		Client: c, Reader: c, Rows: testRows(), Image: testImage,
		Backends: testBackends(), UIDs: fixedUIDs(),
	}
}

func reconcileHome(t *testing.T, r *HomeReconciler, owner string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: getHome(t, owner).ID}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func getHome(t *testing.T, owner string) *model.Home { return homeOf(t, homeNS, owner) }

func getClaim(t *testing.T, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	c := &corev1.PersistentVolumeClaim{}
	must(t, k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: name}, c))
	return c
}

func ready(t *testing.T, h *model.Home) *metav1.Condition {
	t.Helper()
	c := meta.FindStatusCondition(h.Status.Conditions, model.ConditionReady)
	if c == nil {
		t.Fatalf("no Ready condition in %+v", h.Status.Conditions)
	}
	return c
}

func prepareJob(t *testing.T, owner string) batchv1.Job {
	t.Helper()
	var l batchv1.JobList
	must(t, k8s.List(ctx, &l, client.InNamespace(homeNS), client.MatchingLabels{
		model.LabelRole: model.RoleHomePrepare, model.LabelOwner: owner}))
	if len(l.Items) != 1 {
		t.Fatalf("prepare jobs of %s = %d, want 1", owner, len(l.Items))
	}
	return l.Items[0]
}

func finishJob(t *testing.T, j batchv1.Job, ok bool, termMessage string) {
	t.Helper()
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	j.Status.StartTime = &started
	if ok {
		done := metav1.Now()
		j.Status.CompletionTime = &done
		j.Status.Succeeded = 1
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}
	} else {
		j.Status.Failed = 1
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"},
		}
	}
	must(t, k8s.Status().Update(ctx, &j))
	if termMessage == "" {
		return
	}
	// The kubelet's report of how the container ended.
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: j.Name + "-pod", Namespace: homeNS, Labels: map[string]string{batchv1.JobNameLabel: j.Name}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "prepare", Image: "x"}}},
	}
	must(t, k8s.Create(ctx, p))
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "prepare", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: termMessage}}}}
	must(t, k8s.Status().Update(ctx, p))
}

// settled takes a new Home through binding and its prepare Job, so later
// passes see a bound claim and a prepared home.
func settled(t *testing.T, r *HomeReconciler, owner string) {
	t.Helper()
	reconcileHome(t, r, owner)
	claim := getClaim(t, naming.HomeClaimName(owner))
	claim.Status.Phase = corev1.ClaimBound
	must(t, k8s.Status().Update(ctx, claim))
	finishJob(t, prepareJob(t, owner), true, "")
	reconcileHome(t, r, owner)
	if !getHome(t, owner).Status.Prepared {
		t.Fatal("home not prepared")
	}
}

func TestANewHomeGetsAClaimForTheChosenMode(t *testing.T) {
	for _, tc := range []struct {
		mode   model.HomeMode
		access corev1.PersistentVolumeAccessMode
	}{
		{model.HomeModePerUser, corev1.ReadWriteOnce},
		{model.HomeModeShared, corev1.ReadWriteMany},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			homeStorage(t, storageOpts{mode: tc.mode, class: "fast", sizeEnforced: true, nodes: 3})
			owner := uniqueOwner("alice")
			newHome(t, owner, "10Gi")
			reconcileHome(t, homeReconciler(k8s), owner)

			claim := getClaim(t, naming.HomeClaimName(owner))
			if got := claim.Spec.AccessModes; len(got) != 1 || got[0] != tc.access {
				t.Fatalf("access modes = %v, want [%s]", got, tc.access)
			}
			if claim.Spec.StorageClassName == nil || *claim.Spec.StorageClassName != "fast" {
				t.Fatalf("class = %v", claim.Spec.StorageClassName)
			}
			if got := claim.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("10Gi")) != 0 {
				t.Fatalf("size = %s", got.String())
			}
			if len(claim.OwnerReferences) != 0 {
				t.Fatal("a home claim must not be owned: deleting the Home would delete the files")
			}
			h := getHome(t, owner)
			if h.Status.Mode != tc.mode || h.Status.ClaimName != claim.Name || h.Status.StorageClassName != "fast" || !h.Status.SizeEnforced {
				t.Fatalf("status = %+v", h.Status)
			}
		})
	}
}

func TestAPerUserHomeMadeOnAOneNodeClusterIsMarked(t *testing.T) {
	for nodes, want := range map[int32]bool{1: true, 3: false} {
		homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: nodes})
		owner := uniqueOwner("bob")
		newHome(t, owner, "")
		reconcileHome(t, homeReconciler(k8s), owner)
		if got := getHome(t, owner).Status.CreatedOnSingleNode; got != want {
			t.Errorf("%d nodes: createdOnSingleNode = %v, want %v", nodes, got, want)
		}
	}
}

// An M1 claim carries the owner label and no storage class of the new kind.
// Taking it over must change nothing on it.
func TestAnEarlierClaimWithTheOwnerLabelIsAdoptedUntouched(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModeShared, class: "fast", expandable: true, nodes: 3})
	owner := uniqueOwner("carol")
	old := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: naming.HomeClaimName(owner), Namespace: homeNS, Labels: map[string]string{model.LabelOwner: owner}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}},
		},
	}
	must(t, k8s.Create(ctx, old))
	newHome(t, owner, "10Gi")
	reconcileHome(t, homeReconciler(k8s), owner)

	got := getClaim(t, old.Name)
	if !equality.Semantic.DeepEqual(got.Spec.Resources, old.Spec.Resources) || !slices.Equal(got.Spec.AccessModes, old.Spec.AccessModes) {
		t.Fatalf("the adopted claim's spec was changed: %+v", got.Spec)
	}
	if len(got.Labels) != 1 || got.Labels[model.LabelOwner] != owner {
		t.Fatalf("labels = %v, want only the owner's", got.Labels)
	}
	h := getHome(t, owner)
	// The claim is one disk, whatever the cluster would choose for a new home.
	if h.Status.ClaimName != old.Name || h.Status.Mode != model.HomeModePerUser {
		t.Fatalf("status = %+v", h.Status)
	}
}

func TestAClaimWithoutTheOwnerLabelIsNotAdopted(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("dave")
	other := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: naming.HomeClaimName(owner), Namespace: homeNS, Labels: map[string]string{model.LabelOwner: "someone-else"}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	must(t, k8s.Create(ctx, other))
	newHome(t, owner, "")
	reconcileHome(t, homeReconciler(k8s), owner)

	h := getHome(t, owner)
	if h.Status.ClaimName != "" {
		t.Fatalf("adopted %s", h.Status.ClaimName)
	}
	if c := ready(t, h); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeClaimForeign) {
		t.Fatalf("Ready = %+v", c)
	}
	if got := getClaim(t, other.Name); got.ResourceVersion != other.ResourceVersion {
		t.Fatal("the other owner's claim was touched")
	}
}

// Nothing changes silently: a new mode applies to new homes only.
func TestChangingTheChosenModeLeavesAnExistingHomeAlone(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("erin")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)

	cs := get(t)
	cs.Status.Choice.Mode = model.HomeModeShared
	setStatus(t, cs.Status)
	reconcileHome(t, r, owner)

	h := getHome(t, owner)
	if h.Status.Mode != model.HomeModePerUser {
		t.Fatalf("mode = %s after the cluster choice changed", h.Status.Mode)
	}
	if got := getClaim(t, h.Status.ClaimName).Spec.AccessModes; got[0] != corev1.ReadWriteOnce {
		t.Fatalf("claim access modes = %v", got)
	}
}

func TestHomeSizeChanges(t *testing.T) {
	size := func(c *corev1.PersistentVolumeClaim) string {
		q := c.Spec.Resources.Requests[corev1.ResourceStorage]
		return q.String()
	}
	resize := func(t *testing.T, owner, to string) {
		t.Helper()
		_, err := db.Homes().SetSize(ctx, model.Actor{Person: "admin"}, getHome(t, owner).ID, resource.MustParse(to))
		must(t, err)
	}

	t.Run("grow on a class that expands", func(t *testing.T) {
		homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", expandable: true, nodes: 3})
		owner := uniqueOwner("frank")
		// The API server only resizes claims of a class that allows it.
		must(t, k8s.Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Provisioner: "example.com/csi", AllowVolumeExpansion: ptr.To(true)}))
		newHome(t, owner, "10Gi")
		r := homeReconciler(k8s)
		settled(t, r, owner)
		resize(t, owner, "20Gi")
		reconcileHome(t, r, owner)
		if got := size(getClaim(t, naming.HomeClaimName(owner))); got != "20Gi" {
			t.Fatalf("claim size = %s, want 20Gi", got)
		}
	})
	t.Run("grow on a class that cannot", func(t *testing.T) {
		homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
		owner := uniqueOwner("grace")
		newHome(t, owner, "10Gi")
		r := homeReconciler(k8s)
		settled(t, r, owner)
		resize(t, owner, "20Gi")
		reconcileHome(t, r, owner)
		if got := size(getClaim(t, naming.HomeClaimName(owner))); got != "10Gi" {
			t.Fatalf("claim size = %s, want it left at 10Gi", got)
		}
		if c := ready(t, getHome(t, owner)); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeCannotGrow) {
			t.Fatalf("Ready = %+v", c)
		}
	})
	t.Run("grow past the maximum", func(t *testing.T) {
		homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", expandable: true, nodes: 3, maxSize: "20Gi"})
		owner := uniqueOwner("judy")
		must(t, k8s.Create(ctx, &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Provisioner: "example.com/csi", AllowVolumeExpansion: ptr.To(true)}))
		newHome(t, owner, "10Gi")
		r := homeReconciler(k8s)
		settled(t, r, owner)
		resize(t, owner, "500Gi")
		reconcileHome(t, r, owner)
		if got := size(getClaim(t, naming.HomeClaimName(owner))); got != "10Gi" {
			t.Fatalf("claim size = %s, want it left at 10Gi", got)
		}
		c := ready(t, getHome(t, owner))
		if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeTooLarge) || !strings.Contains(c.Message, "500Gi") || !strings.Contains(c.Message, "20Gi") {
			t.Fatalf("Ready = %+v", c)
		}
	})
	t.Run("shrink", func(t *testing.T) {
		homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", expandable: true, nodes: 3})
		owner := uniqueOwner("heidi")
		newHome(t, owner, "20Gi")
		r := homeReconciler(k8s)
		settled(t, r, owner)
		resize(t, owner, "10Gi")
		reconcileHome(t, r, owner)
		if got := size(getClaim(t, naming.HomeClaimName(owner))); got != "20Gi" {
			t.Fatalf("claim size = %s, want it left at 20Gi", got)
		}
		if c := ready(t, getHome(t, owner)); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeCannotShrink) {
			t.Fatalf("Ready = %+v", c)
		}
	})
}

func TestAHomeLargerThanTheMaximumGetsNoClaim(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3, maxSize: "50Gi"})
	owner := uniqueOwner("ivan")
	newHome(t, owner, "100Gi")
	reconcileHome(t, homeReconciler(k8s), owner)

	if err := k8s.Get(ctx, types.NamespacedName{Namespace: homeNS, Name: naming.HomeClaimName(owner)}, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a claim over the maximum was created: %v", err)
	}
	c := ready(t, getHome(t, owner))
	if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeTooLarge) || !strings.Contains(c.Message, "100Gi") || !strings.Contains(c.Message, "50Gi") {
		t.Fatalf("Ready = %+v", c)
	}
}

// quotaFull refuses to create claims the way the API server does when a
// ResourceQuota is used up (envtest runs no quota controller).
type quotaFull struct {
	client.Client
	msg string
}

func (q quotaFull) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
		return apierrors.NewForbidden(corev1.Resource("persistentvolumeclaims"), obj.GetName(), errors.New(q.msg))
	}
	return q.Client.Create(ctx, obj, opts...)
}

func TestAnExhaustedQuotaIsTeamStorageFullWithTheAPIMessage(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("judy")
	newHome(t, owner, "10Gi")
	const apiMsg = "exceeded quota: team-storage, requested: requests.storage=10Gi, used: requests.storage=95Gi, limited: requests.storage=100Gi"
	reconcileHome(t, homeReconciler(quotaFull{k8s, apiMsg}), owner)

	got := ready(t, getHome(t, owner))
	if got.Status != metav1.ConditionFalse || got.Reason != string(reasons.TeamStorageFull) || !strings.Contains(got.Message, apiMsg) {
		t.Fatalf("Ready = %+v", got)
	}
	if !strings.Contains(got.Message, "namespace "+homeNS) {
		t.Fatalf("the message does not say which team space is full: %q", got.Message)
	}
}

func TestThePrepareJobMountsOnlyTheHomeAndSetsFSGroup(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("kate")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)
	reconcileHome(t, r, owner) // a second pass must not make a second Job

	h := getHome(t, owner)
	if h.Status.Prepared {
		t.Fatal("prepared before the Job ran")
	}
	if c := ready(t, h); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomePreparing) {
		t.Fatalf("Ready = %+v", c)
	}
	j := prepareJob(t, owner)
	spec := j.Spec.Template.Spec
	if len(spec.Volumes) != 1 || spec.Volumes[0].PersistentVolumeClaim == nil || spec.Volumes[0].PersistentVolumeClaim.ClaimName != h.Status.ClaimName {
		t.Fatalf("volumes = %+v, want only the home claim", spec.Volumes)
	}
	sc := spec.SecurityContext
	if sc.FSGroup == nil || *sc.FSGroup != 100 || sc.FSGroupChangePolicy == nil || *sc.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
		t.Fatalf("security context = %+v", sc)
	}
	if *sc.RunAsUser != 1000 || *sc.RunAsGroup != 100 || !*sc.RunAsNonRoot {
		t.Fatalf("runs as %d:%d nonroot=%v", *sc.RunAsUser, *sc.RunAsGroup, *sc.RunAsNonRoot)
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("service account token is mounted")
	}
	if spec.Containers[0].Image != testImage.Ref || !pullsTestImage(spec) {
		t.Fatalf("image %q, pull secrets %v", spec.Containers[0].Image, spec.ImagePullSecrets)
	}
	c := spec.Containers[0]
	if *c.SecurityContext.AllowPrivilegeEscalation || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container security context = %+v", c.SecurityContext)
	}
	wantArgs := []string{"storage", "prepare", "--dir", "/home-root", "--uid", "1000", "--gid", "100", "--mode", "0775", "--no-mkdir"}
	if strings.Join(c.Args, " ") != strings.Join(wantArgs, " ") {
		t.Fatalf("args = %v, want %v", c.Args, wantArgs)
	}
	if j.Spec.TTLSecondsAfterFinished == nil || j.Labels[model.LabelHome] != h.ID || len(j.OwnerReferences) != 0 {
		t.Fatalf("ttl = %v, labels %v, owner references %v: the Job names its home by label", j.Spec.TTLSecondsAfterFinished, j.Labels, j.OwnerReferences)

	}
}

func TestAHomeIsPreparedOnceItsJobCompletes(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("liam")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)
	finishJob(t, prepareJob(t, owner), true, "")
	reconcileHome(t, r, owner)

	h := getHome(t, owner)
	if !h.Status.Prepared {
		t.Fatal("not prepared")
	}
	if c := ready(t, h); c.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %+v", c)
	}
}

// A driver that ignores fsGroup makes the Job fail; the home stays unprepared
// and the Job's own words reach the condition.
func TestAFailedPrepareJobIsEvidenceNotAReadyHome(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("mia")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)
	const said = "volume at /home-root is owned by 0:0 with mode drwxr-xr-x, which user 1000 in group 100 cannot write: the storage driver did not apply fsGroup"
	finishJob(t, prepareJob(t, owner), false, said)
	res := reconcileHome(t, r, owner)

	h := getHome(t, owner)
	c := ready(t, h)
	if h.Status.Prepared || c.Status != metav1.ConditionFalse || !strings.Contains(c.Message, said) {
		t.Fatalf("prepared = %v, Ready = %+v", h.Status.Prepared, c)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("a failed prepare is looked at again, once the Job is gone")
	}
}

func TestAHomeWaitsForTheOwnersUIDInAllocatedMode(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("noah")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	r.UIDs = ids.People{People: db.People(store.IDRange{}), Mode: config.UIDModeAllocated}
	reconcileHome(t, r, owner)

	if c := ready(t, getHome(t, owner)); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.IdentityPending) {
		t.Fatalf("Ready = %+v", c)
	}
	var jobs batchv1.JobList
	must(t, k8s.List(ctx, &jobs, client.InNamespace(homeNS), client.MatchingLabels{model.LabelOwner: owner}))
	if len(jobs.Items) != 0 {
		t.Fatal("a Job ran as an unknown uid")
	}
}

func TestAHomeWithoutAStorageChoiceWaits(t *testing.T) {
	clean(t)
	owner := uniqueOwner("olga")
	newHome(t, owner, "")
	res := reconcileHome(t, homeReconciler(k8s), owner)
	if res.RequeueAfter == 0 {
		t.Fatal("a Home read before the cluster was inspected must be looked at again")
	}
	if c := ready(t, getHome(t, owner)); c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomePending) {
		t.Fatalf("Ready = %+v", c)
	}
}

func TestAnAdoptedClaimKeepsItsSizeWhateverTheClusterDefaultIs(t *testing.T) {
	for _, tc := range []struct{ name, claim, cluster string }{{"larger default", "10Gi", "20Gi"}, {"smaller default", "20Gi", "10Gi"}} {
		t.Run(tc.name, func(t *testing.T) {
			homeStorage(t, storageOpts{mode: model.HomeModeShared, class: "fast", expandable: true, nodes: 3})
			owner := uniqueOwner("mallory")
			old := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: naming.HomeClaimName(owner), Namespace: homeNS, Labels: map[string]string{model.LabelOwner: owner}},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(tc.claim)}},
				},
			}
			must(t, k8s.Create(ctx, old))
			old.Status.Phase = corev1.ClaimBound
			must(t, k8s.Status().Update(ctx, old))
			old = getClaim(t, old.Name)
			newHome(t, owner, tc.cluster) // what the workspace controller asks for a new Home
			r := homeReconciler(k8s)
			reconcileHome(t, r, owner)
			reconcileHome(t, r, owner)

			if got := getClaim(t, old.Name); !equality.Semantic.DeepEqual(got.Spec.Resources, old.Spec.Resources) {
				t.Fatalf("the adopted claim's size was changed: %v -> %v", old.Spec.Resources, got.Spec.Resources)
			}
			finishJob(t, prepareJob(t, owner), true, "")
			reconcileHome(t, r, owner)
			h := getHome(t, owner)
			if c := ready(t, h); c.Status != metav1.ConditionTrue {
				t.Fatalf("Ready = %+v", c)
			}
			if got := h.Spec.Size.String(); got != tc.claim {
				t.Fatalf("home size = %s, want the claim's %s", got, tc.claim)
			}
		})
	}
}

func TestThePrepareJobStaysOnTheNodeOfTheOwnersPodsOnlyWhenTheDiskIsPerUserOnManyNodes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  model.HomeMode
		nodes int32
		want  bool
	}{
		{"per-user, many nodes", model.HomeModePerUser, 3, true},
		{"per-user, one node", model.HomeModePerUser, 1, false},
		{"shared, many nodes", model.HomeModeShared, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homeStorage(t, storageOpts{mode: tc.mode, class: "fast", nodes: tc.nodes})
			owner := uniqueOwner("niaj")
			newHome(t, owner, "")
			reconcileHome(t, homeReconciler(k8s), owner)
			aff := prepareJob(t, owner).Spec.Template.Spec.Affinity
			if !tc.want {
				if aff != nil {
					t.Fatalf("affinity = %+v, want none", aff)
				}
				return
			}
			terms := aff.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
			if len(terms) != 1 || terms[0].TopologyKey != "kubernetes.io/hostname" ||
				terms[0].LabelSelector.MatchLabels[model.LabelOwner] != owner {
				t.Fatalf("affinity terms = %+v", terms)
			}
		})
	}
}

// claimRefused refuses to create claims with an error that is neither a quota
// nor an already-exists, the way a LimitRange or an admission webhook does.
type claimRefused struct {
	client.Client
	msg string
}

func (c claimRefused) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
		return apierrors.NewBadRequest(c.msg)
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestARefusedClaimShowsTheAPIErrorOnTheHome(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("nina")
	newHome(t, owner, "10Gi")
	const apiMsg = "maximum storage usage per PersistentVolumeClaim is 5Gi, but request is 10Gi"
	res := reconcileHome(t, homeReconciler(claimRefused{k8s, apiMsg}), owner)

	got := ready(t, getHome(t, owner))
	if got.Status != metav1.ConditionFalse || got.Reason != string(reasons.HomePending) || !strings.Contains(got.Message, apiMsg) {
		t.Fatalf("Ready = %+v", got)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("a refused claim is tried again")
	}
}

func TestAChosenModeWithoutABackendShowsOnTheHome(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("omar")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	r.Backends = nil
	reconcileHome(t, r, owner)

	got := ready(t, getHome(t, owner))
	if got.Status != metav1.ConditionFalse || got.Reason != string(reasons.HomePending) || !strings.Contains(got.Message, "no storage backend") {
		t.Fatalf("Ready = %+v", got)
	}
}

func TestAFailedPrepareIsReportedAsFailedWithTheJobsWords(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("pia")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)
	const said = "the storage driver did not apply fsGroup"
	finishJob(t, prepareJob(t, owner), false, said)
	reconcileHome(t, r, owner)

	c := ready(t, getHome(t, owner))
	if c.Reason != string(reasons.HomePrepareFailed) || !strings.Contains(c.Message, said) {
		t.Fatalf("Ready = %+v", c)
	}
}

// A setup that can never work is not retried for ever: after the last
// attempt the Job stays as the evidence and no new one is made.
func TestAHomeWhosePrepareKeepsFailingStopsGettingNewJobs(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
	owner := uniqueOwner("quin")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	for i := 0; i < maxPrepareAttempts; i++ {
		reconcileHome(t, r, owner)
		j := prepareJob(t, owner)
		said := ""
		if i == maxPrepareAttempts-1 {
			said = "no fsGroup" // one kubelet report is enough, and its Pod name repeats per Job
		}
		finishJob(t, j, false, said)
		reconcileHome(t, r, owner)
		if i < maxPrepareAttempts-1 {
			must(t, k8s.Delete(ctx, &j, client.PropagationPolicy(metav1.DeletePropagationBackground))) // what the TTL does
		}
	}
	last := prepareJob(t, owner)
	if last.Spec.TTLSecondsAfterFinished != nil {
		t.Fatalf("the last attempt's Job must outlive its TTL as evidence, TTL = %d", *last.Spec.TTLSecondsAfterFinished)
	}
	res := reconcileHome(t, r, owner)
	c := ready(t, getHome(t, owner))
	if c.Reason != string(reasons.HomePrepareFailed) || !strings.Contains(c.Message, "no fsGroup") {
		t.Fatalf("Ready = %+v", c)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("no retry is scheduled after the last attempt, got %v", res.RequeueAfter)
	}
}

// A Job in the prepare Job's name that is not this Home's (another owner's,
// or one without the role label) is never used, and the Home says which Job
// is in the way instead of staying at "preparing".
func TestAPrepareJobNameTakenByAnotherJobShowsOnTheHome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owned bool
	}{{"not owned by the home", false}, {"owned but without the role label", true}} {
		t.Run(tc.name, func(t *testing.T) {
			homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3})
			owner := uniqueOwner("rosa")
			h := newHome(t, owner, "")
			jobName := prepareJobNameFor(naming.HomeClaimName(owner))
			other := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: homeNS},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "x", Image: "x"}},
				}}},
			}
			if tc.owned {
				other.Labels = map[string]string{model.LabelHome: h.ID}
			}
			must(t, k8s.Create(ctx, other))
			res := reconcileHome(t, homeReconciler(k8s), owner)

			c := ready(t, getHome(t, owner))
			if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomePending) || !strings.Contains(c.Message, jobName) {
				t.Fatalf("Ready = %+v", c)
			}
			if res.RequeueAfter == 0 {
				t.Fatal("the Home is looked at again once the Job is moved")
			}
		})
	}
}

// afterHomeGet runs hook once, at the reconciler's first read of the cluster,
// which comes after it read the home: another writer got in between that
// read and the reconciler's status write.
type afterHomeGet struct {
	client.Client
	hook func()
}

func (c *afterHomeGet) run() {
	if h := c.hook; h != nil {
		c.hook = nil
		h()
	}
}

func (c *afterHomeGet) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	c.run()
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *afterHomeGet) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.run()
	return c.Client.List(ctx, list, opts...)
}

// A Home reconciler pass that read the Home before the migration reconciler
// wrote Migrating=True must not write its older conditions over it: that would
// lift the hold on the owner's apps in the middle of a copy.
func TestAStaleHomePassDoesNotOverwriteTheMigratingHold(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModePerUser, class: "fast", nodes: 3, expandable: true})
	owner := uniqueOwner("rex")
	newHome(t, owner, "10Gi")
	c := &afterHomeGet{Client: k8s}
	c.hook = func() {
		editHome(t, homeNS, owner, func(h *model.Home) {
			meta.SetStatusCondition(&h.Status.Conditions, metav1.Condition{Type: model.ConditionMigrating, Status: metav1.ConditionTrue, Reason: "Copying", Message: "x"})
		})
	}
	r := homeReconciler(c)
	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: getHome(t, owner).ID}})

	if err != nil {
		t.Fatalf("a conflict is a requeue, not an error: %v", err)
	}
	if !res.Requeue && res.RequeueAfter == 0 {
		t.Error("no requeue after losing the write")
	}
	if c := meta.FindStatusCondition(getHome(t, owner).Status.Conditions, model.ConditionMigrating); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("Migrating = %+v, want the hold kept", c)
	}
	// The next pass derives the same status from the current Home.
	reconcileHome(t, r, owner)
	if h := getHome(t, owner); h.Status.ClaimName == "" || meta.FindStatusCondition(h.Status.Conditions, model.ConditionMigrating) == nil {
		t.Fatalf("status = %+v", h.Status)
	}
}

// jobPod is the pod the Job controller would make for j, not yet started.
func jobPod(t *testing.T, j batchv1.Job) *corev1.Pod {
	t.Helper()
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: j.Name + "-x1", Namespace: j.Namespace, Labels: map[string]string{batchv1.JobNameLabel: j.Name}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "prepare", Image: "x"}}},
	}
	must(t, k8s.Create(ctx, p))
	return p
}

// podWarning is what the kubelet reports about p: reason, first seen at first
// and seen again just now.
func podWarning(t *testing.T, p *corev1.Pod, reason, msg string, first time.Time) {
	t.Helper()
	must(t, k8s.Create(ctx, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: "ev-", Namespace: p.Namespace},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: p.Name, Namespace: p.Namespace, UID: p.UID},
		Reason:         reason, Message: msg, Type: corev1.EventTypeWarning, Count: 5,
		FirstTimestamp: metav1.NewTime(first), LastTimestamp: metav1.Now(),
	}))
}

const noNFSHelper = "MountVolume.MountDevice failed for volume \"pvc-1\" : mount failed: exit status 32\nmount: /var/lib/kubelet/x: bad option; for several filesystems (e.g. nfs, cifs) you might need a /sbin/mount.<type> helper program."

// A prepare Job whose volume never mounts is not "setting up" for ever: once
// the kubelet has kept failing past the grace, the Home fails with its words.
func TestAPrepareJobWhoseVolumeKeepsFailingToMountFailsTheHome(t *testing.T) {
	for _, reason := range []string{"FailedMount", "FailedAttachVolume"} {
		t.Run(reason, func(t *testing.T) {
			homeStorage(t, storageOpts{mode: model.HomeModeShared, class: "longhorn", nodes: 3})
			owner := uniqueOwner("sven")
			newHome(t, owner, "")
			r := homeReconciler(k8s)
			reconcileHome(t, r, owner)
			podWarning(t, jobPod(t, prepareJob(t, owner)), reason, noNFSHelper, time.Now().Add(-5*time.Minute))
			res := reconcileHome(t, r, owner)

			c := ready(t, getHome(t, owner))
			if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeMountFailed) || !strings.Contains(c.Message, "mount.<type> helper program") {
				t.Fatalf("Ready = %+v", c)
			}
			if res.RequeueAfter == 0 {
				t.Fatal("a mount that starts working later must still finish the home")
			}
		})
	}
}

// The first mount attempts of a new volume often fail while it is attached;
// they are not a failure yet.
func TestAMountFailureWithinTheGraceIsStillSettingUp(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModeShared, class: "longhorn", nodes: 3})
	owner := uniqueOwner("tove")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)
	podWarning(t, jobPod(t, prepareJob(t, owner)), "FailedMount", noNFSHelper, time.Now().Add(-5*time.Second))
	reconcileHome(t, r, owner)

	if c := ready(t, getHome(t, owner)); c.Reason != string(reasons.HomePreparing) {
		t.Fatalf("Ready = %+v", c)
	}
}

// The Job's deadline ends a pod that never mounted; the Home still says it
// was the mount, not a setup that failed.
func TestAPrepareJobThatTimedOutMountingSaysItWasTheMount(t *testing.T) {
	homeStorage(t, storageOpts{mode: model.HomeModeShared, class: "longhorn", nodes: 3})
	owner := uniqueOwner("ulla")
	newHome(t, owner, "")
	r := homeReconciler(k8s)
	reconcileHome(t, r, owner)
	j := prepareJob(t, owner)
	podWarning(t, jobPod(t, j), "FailedMount", noNFSHelper, time.Now().Add(-10*time.Minute))
	finishJob(t, j, false, "")
	reconcileHome(t, r, owner)

	c := ready(t, getHome(t, owner))
	if c.Reason != string(reasons.HomeMountFailed) || !strings.Contains(c.Message, "mount.<type> helper program") {
		t.Fatalf("Ready = %+v", c)
	}
}
