package hubimport

import (
	"context"
	"os"
	"slices"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

var (
	k8s client.Client
	ctx = context.Background()
)

func TestMain(m *testing.M) {
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixture is a JupyterHub namespace holding alice's home claim, a Javo
// namespace holding her prepared home claim, and the importer between them.
type fixture struct {
	im     Importer
	src    Source
	target Target
	source *corev1.PersistentVolumeClaim
}

var nsCount int

func newNamespace(t *testing.T, prefix string) string {
	t.Helper()
	nsCount++
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix + "-"}}
	must(t, k8s.Create(ctx, ns))
	return ns.Name
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	hub, apps := newNamespace(t, "jupyterhub"), newNamespace(t, "javo-apps")
	source := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-alice", Namespace: hub},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: ptr.To("standard"),
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}},
		},
	}
	must(t, k8s.Create(ctx, source))
	home := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "home-alice-1a2b3c4d", Namespace: apps, Labels: map[string]string{model.LabelOwner: "alice-1a2b3c4d"}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}},
		},
	}
	must(t, k8s.Create(ctx, home))
	return &fixture{
		im:     Importer{Client: k8s, Image: podspec.Image{Ref: "registry.example.org/javo:1"}, Into: "jupyterhub"},
		src:    Source{Namespace: hub, Claim: "claim-alice"},
		target: Target{Owner: "alice-1a2b3c4d", UID: 100001, GID: 100, Namespace: apps, HomeClaim: home.Name},
		source: source,
	}
}

func (f *fixture) step(t *testing.T) Progress {
	t.Helper()
	p, err := f.im.Step(ctx, f.src, f.target)
	must(t, err)
	return p
}

func job(t *testing.T, ns, name string) *batchv1.Job {
	t.Helper()
	j := &batchv1.Job{}
	must(t, k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, j))
	return j
}

// The first step stages: a claim in JupyterHub's namespace as large as the
// home and of its class, and a Job that mounts the home read-only and the
// staging claim, and copies as root with only the capabilities a copy that
// sets owners needs, giving every file the person's uid and gid.
func TestFirstStepStagesTheHomeInJupyterHubsNamespace(t *testing.T) {
	f := newFixture(t)
	p := f.step(t)
	if p.Phase != PhaseStaging || p.Job == nil || p.Job.Namespace != f.src.Namespace {
		t.Fatalf("progress = %+v", p)
	}
	staging := &corev1.PersistentVolumeClaim{}
	must(t, k8s.Get(ctx, client.ObjectKey{Namespace: f.src.Namespace, Name: stagingClaimName(f.target.Owner)}, staging))
	if got := staging.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse("20Gi")) != 0 || ptr.Deref(staging.Spec.StorageClassName, "") != "standard" {
		t.Errorf("staging claim: %s of %v", got.String(), staging.Spec.StorageClassName)
	}
	j := job(t, p.Job.Namespace, p.Job.Name)
	pod := j.Spec.Template.Spec
	c := pod.Containers[0]
	if c.Image != "registry.example.org/javo:1" || !slices.Equal(c.Command, []string{"/javo", "storage", "import-copy", "--old", "/old", "--new", "/new", "--uid", "100001", "--gid", "100"}) {
		t.Errorf("container %s %v", c.Image, c.Command)
	}
	if ptr.Deref(pod.SecurityContext.RunAsUser, -1) != 0 || c.SecurityContext.Capabilities == nil ||
		!slices.Equal(c.SecurityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) ||
		!slices.Equal(c.SecurityContext.Capabilities.Add, []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"}) {
		t.Errorf("security: pod %+v container %+v", pod.SecurityContext, c.SecurityContext)
	}
	mounts := map[string]corev1.VolumeSource{}
	for _, v := range pod.Volumes {
		mounts[v.Name] = v.VolumeSource
	}
	if s := mounts["old"].PersistentVolumeClaim; s == nil || s.ClaimName != "claim-alice" || !s.ReadOnly {
		t.Errorf("old volume %+v", mounts["old"])
	}
	if s := mounts["new"].PersistentVolumeClaim; s == nil || s.ClaimName != staging.Name || s.ReadOnly {
		t.Errorf("new volume %+v", mounts["new"])
	}
	if ptr.Deref(pod.AutomountServiceAccountToken, true) {
		t.Error("the Job gets an API token")
	}
}

// finish marks a Job's end as the Job controller would.
func finish(t *testing.T, j *batchv1.Job, ok bool) {
	t.Helper()
	now := metav1.Now()
	j.Status.StartTime = &now
	conds := []batchv1.JobConditionType{batchv1.JobSuccessCriteriaMet, batchv1.JobComplete}
	if ok {
		j.Status.CompletionTime = &now
		j.Status.Succeeded = 1
	} else {
		conds = []batchv1.JobConditionType{batchv1.JobFailureTarget, batchv1.JobFailed}
		j.Status.Failed = 1
	}
	for _, c := range conds {
		j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{Type: c, Status: corev1.ConditionTrue, LastTransitionTime: now, LastProbeTime: now})
	}
	must(t, k8s.Status().Update(ctx, j))
}

// bind binds claim to a new volume, as a provisioner and the volume
// controller would.
func bind(t *testing.T, claim *corev1.PersistentVolumeClaim, reclaim corev1.PersistentVolumeReclaimPolicy) *corev1.PersistentVolume {
	t.Helper()
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "pvc-"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: claim.Spec.Resources.Requests[corev1.ResourceStorage]},
			AccessModes:                   claim.Spec.AccessModes,
			PersistentVolumeReclaimPolicy: reclaim,
			StorageClassName:              ptr.Deref(claim.Spec.StorageClassName, ""),
			PersistentVolumeSource:        corev1.PersistentVolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/tmp/" + claim.Name}},
			ClaimRef:                      &corev1.ObjectReference{Kind: "PersistentVolumeClaim", Namespace: claim.Namespace, Name: claim.Name, UID: claim.UID},
		},
	}
	must(t, k8s.Create(ctx, pv))
	claim.Spec.VolumeName = pv.Name
	must(t, k8s.Update(ctx, claim))
	claim.Status.Phase = corev1.ClaimBound
	claim.Status.Capacity = pv.Spec.Capacity
	must(t, k8s.Status().Update(ctx, claim))
	return pv
}

// forget lets a deleted claim go, as the protection controller does once no
// running pod uses it.
func forget(t *testing.T, ns, name string) {
	t.Helper()
	c := &corev1.PersistentVolumeClaim{}
	if err := k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, c); err != nil {
		return
	}
	c.Finalizers = nil
	must(t, k8s.Update(ctx, c))
}

func get[T client.Object](t *testing.T, ns, name string, obj T) T {
	t.Helper()
	must(t, k8s.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj))
	return obj
}

// Once the stage Job is done, the staging volume moves to Javo's namespace:
// it is kept when its claim goes (Retain), its claim is deleted, and it is
// bound to a claim of the same name in Javo's namespace. The JupyterHub
// claim is never changed.
func TestAStagedVolumeMovesToJavosNamespace(t *testing.T) {
	f := newFixture(t)
	p := f.step(t)
	staging := get(t, f.src.Namespace, stagingClaimName(f.target.Owner), &corev1.PersistentVolumeClaim{})
	pv := bind(t, staging, corev1.PersistentVolumeReclaimDelete)
	finish(t, job(t, p.Job.Namespace, p.Job.Name), true)
	sourceVersion := get(t, f.src.Namespace, f.src.Claim, &corev1.PersistentVolumeClaim{}).ResourceVersion

	if p = f.step(t); p.Phase != PhaseMoving {
		t.Fatalf("progress = %+v", p)
	}
	pv = get(t, "", pv.Name, &corev1.PersistentVolume{})
	if pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatalf("the staging volume would be deleted with its claim: %s", pv.Spec.PersistentVolumeReclaimPolicy)
	}
	forget(t, f.src.Namespace, staging.Name)
	for range 3 {
		if p = f.step(t); p.Phase != PhaseMoving {
			break
		}
		moved := &corev1.PersistentVolumeClaim{}
		if err := k8s.Get(ctx, client.ObjectKey{Namespace: f.target.Namespace, Name: stagingClaimName(f.target.Owner)}, moved); err == nil {
			if moved.Spec.VolumeName != pv.Name {
				t.Fatalf("the moved claim names volume %q, want %q", moved.Spec.VolumeName, pv.Name)
			}
			moved.Status.Phase = corev1.ClaimBound
			must(t, k8s.Status().Update(ctx, moved))
		}
	}
	pv = get(t, "", pv.Name, &corev1.PersistentVolume{})
	if r := pv.Spec.ClaimRef; r == nil || r.Namespace != f.target.Namespace || r.Name != stagingClaimName(f.target.Owner) {
		t.Fatalf("volume claimRef = %+v", pv.Spec.ClaimRef)
	}
	if p.Phase != PhaseCopying {
		t.Fatalf("progress after the move = %+v", p)
	}
	if v := get(t, f.src.Namespace, f.src.Claim, &corev1.PersistentVolumeClaim{}).ResourceVersion; v != sourceVersion {
		t.Error("the JupyterHub claim was changed")
	}
}

// toCopy drives f's import to the copy: the stage Job succeeds, and the
// volume moves, playing the controllers' part. It returns the volume.
func toCopy(t *testing.T, f *fixture) (Progress, *corev1.PersistentVolume) {
	t.Helper()
	p := f.step(t)
	pv := bind(t, get(t, f.src.Namespace, stagingClaimName(f.target.Owner), &corev1.PersistentVolumeClaim{}), corev1.PersistentVolumeReclaimDelete)
	finish(t, job(t, p.Job.Namespace, p.Job.Name), true)
	f.step(t)
	forget(t, f.src.Namespace, stagingClaimName(f.target.Owner))
	f.step(t)
	moved := get(t, f.target.Namespace, stagingClaimName(f.target.Owner), &corev1.PersistentVolumeClaim{})
	moved.Status.Phase = corev1.ClaimBound
	must(t, k8s.Status().Update(ctx, moved))
	return f.step(t), pv
}

// The copy into the home runs in Javo's namespace as the person, without
// root or any capability, with the staged files read-only, into the folder
// asked for. When it is done the home claim says where its files came from,
// the staging volume is let go, and a later run finds the import done.
func TestTheCopyRunsAsThePersonAndMarksTheHome(t *testing.T) {
	f := newFixture(t)
	p, pv := toCopy(t, f)
	if p.Phase != PhaseCopying || p.Job == nil || p.Job.Namespace != f.target.Namespace {
		t.Fatalf("progress = %+v", p)
	}
	pod := job(t, p.Job.Namespace, p.Job.Name).Spec.Template.Spec
	c := pod.Containers[0]
	if !slices.Equal(c.Command, []string{"/javo", "storage", "copy", "--old", "/old", "--new", "/new/jupyterhub"}) {
		t.Errorf("command %v", c.Command)
	}
	if ptr.Deref(pod.SecurityContext.RunAsUser, 0) != 100001 || ptr.Deref(pod.SecurityContext.RunAsGroup, 0) != 100 ||
		!ptr.Deref(pod.SecurityContext.RunAsNonRoot, false) || len(c.SecurityContext.Capabilities.Add) != 0 {
		t.Errorf("security: pod %+v container %+v", pod.SecurityContext, c.SecurityContext)
	}
	for _, v := range pod.Volumes {
		if s := v.PersistentVolumeClaim; v.Name == "old" && (s.ClaimName != stagingClaimName(f.target.Owner) || !s.ReadOnly) ||
			v.Name == "new" && s.ClaimName != f.target.HomeClaim {
			t.Errorf("volume %s: %+v", v.Name, s)
		}
	}

	finish(t, job(t, p.Job.Namespace, p.Job.Name), true)
	if p = f.step(t); p.Phase != PhaseDone {
		t.Fatalf("progress = %+v", p)
	}
	home := get(t, f.target.Namespace, f.target.HomeClaim, &corev1.PersistentVolumeClaim{})
	if got := home.Annotations[model.AnnotationImportedFrom]; got != f.src.Namespace+"/claim-alice" {
		t.Errorf("home claim imported-from = %q", got)
	}
	if pv = get(t, "", pv.Name, &corev1.PersistentVolume{}); pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimDelete {
		t.Errorf("the staging volume is kept: %s", pv.Spec.PersistentVolumeReclaimPolicy)
	}
	if moved, _ := f.im.claim(ctx, f.target.Namespace, stagingClaimName(f.target.Owner)); moved != nil && moved.DeletionTimestamp.IsZero() {
		t.Error("the moved staging claim is not deleted")
	}
	if p = f.step(t); p.Phase != PhaseDone {
		t.Fatalf("a later run: %+v", p)
	}
}

// Nothing starts while the person's JupyterHub server has the home, or
// while one of their Javo apps has theirs.
func TestAHomeInUseBlocksTheImport(t *testing.T) {
	f := newFixture(t)
	server := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "jupyter-alice", Namespace: f.src.Namespace},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "notebook", Image: "jupyter"}},
			Volumes: []corev1.Volume{claimVolume("home", "claim-alice", false)}},
	}
	must(t, k8s.Create(ctx, server))
	if p := f.step(t); p.Phase != PhaseBlocked || len(p.Detail) == 0 {
		t.Fatalf("progress = %+v", p)
	}
	if c, _ := f.im.claim(ctx, f.src.Namespace, stagingClaimName(f.target.Owner)); c != nil {
		t.Fatal("a staging claim was made while the server runs")
	}
	must(t, k8s.Delete(ctx, server, client.GracePeriodSeconds(0)))

	app := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: f.target.Namespace, Labels: map[string]string{model.LabelOwner: f.target.Owner}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "app"}}},
	}
	must(t, k8s.Create(ctx, app))
	if p, _ := toCopy(t, f); p.Phase != PhaseBlocked {
		t.Fatalf("progress with an app running = %+v", p)
	}
}

// A failed Job is reported with its termination message, stays failed on
// the next step, and Retry runs it again.
func TestAFailedJobIsReportedAndRetried(t *testing.T) {
	f := newFixture(t)
	p := f.step(t)
	j := job(t, p.Job.Namespace, p.Job.Name)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "stage-pod", Namespace: j.Namespace, Labels: map[string]string{batchv1.JobNameLabel: j.Name}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "copy", Image: "javo"}}},
	}
	must(t, k8s.Create(ctx, pod))
	pod.Status.Phase = corev1.PodFailed
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "copy", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		ExitCode: 1, Message: "copy failed: partial transfer\nnotebooks/big.csv: no space left on device\n"}}}}
	must(t, k8s.Status().Update(ctx, pod))
	finish(t, j, false)

	for range 2 {
		p = f.step(t)
		if p.Phase != PhaseFailed || !slices.Contains(p.Detail, "notebooks/big.csv: no space left on device") {
			t.Fatalf("progress = %+v", p)
		}
	}
	must(t, f.im.Retry(ctx, f.src, f.target))
	if state, _, err := f.im.jobState(ctx, *p.Job); err != nil || state != jobMissing && state != jobRunning {
		t.Fatalf("after Retry the Job is %v, %v", state, err)
	}
	if p = f.step(t); p.Phase != PhaseStaging {
		t.Fatalf("progress after Retry = %+v", p)
	}
}
