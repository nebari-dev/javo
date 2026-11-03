package storage

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

const probeNS = "default"

var probeSeq atomic.Int32

// probeEnv is one probe run against the shared envtest: storage requests
// asking for a probe with a nonce no other test uses.
type probeEnv struct {
	t     *testing.T
	r     *ProbeReconciler
	nonce string
	now   time.Time
}

func newProbe(t *testing.T, nodes ...string) *probeEnv {
	t.Helper()
	clean(t)
	for _, n := range nodes {
		nodeWith(t, n, func(*corev1.Node) {})
	}
	nonce := fmt.Sprintf("nonce-%d", probeSeq.Add(1))
	setRequests(t, func(r *model.StorageRequests) { r.Probe = model.ProbeSpec{StorageClassName: "nfs", Nonce: nonce} })
	e := &probeEnv{t: t, nonce: nonce, now: time.Now()}
	e.r = &ProbeReconciler{Client: env(t).k8s, Reader: env(t).k8s, Rows: testRows(t), Namespace: probeNS, Image: testImage, Now: func() time.Time { return e.now }}
	return e
}

func (e *probeEnv) reconcile() {
	e.t.Helper()
	if _, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *probeEnv) probe() *model.ProbeStatus {
	e.t.Helper()
	return get(e.t).Status.Probe
}

func (e *probeEnv) pvcName() string { return e.r.pvcName(e.nonce) }

func (e *probeEnv) jobs() map[string]batchv1.Job {
	e.t.Helper()
	var l batchv1.JobList
	must(e.t, env(e.t).k8s.List(ctx, &l, client.InNamespace(probeNS), client.MatchingLabels{model.LabelProbe: probeHash(e.nonce)}))
	out := map[string]batchv1.Job{}
	for _, j := range l.Items {
		out[j.Labels[labelProbeStep]] = j
	}
	return out
}

// gone reports whether the claim is deleted or being deleted (envtest has no
// controller that removes the protection finalizer).
func (e *probeEnv) pvcGone() bool {
	e.t.Helper()
	p := &corev1.PersistentVolumeClaim{}
	err := env(e.t).k8s.Get(ctx, client.ObjectKey{Namespace: probeNS, Name: e.pvcName()}, p)
	return apierrors.IsNotFound(err) || p.DeletionTimestamp != nil
}

func (e *probeEnv) jobsGone() bool {
	e.t.Helper()
	for _, j := range e.jobs() {
		if j.DeletionTimestamp == nil {
			return false
		}
	}
	return true
}

// podFor stands in for the scheduler: a pod of the job on a node.
func (e *probeEnv) podFor(step, nodeName string) *corev1.Pod {
	e.t.Helper()
	j := e.jobs()[step]
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: j.Name + "-pod", Namespace: probeNS, Labels: map[string]string{batchv1.JobNameLabel: j.Name}},
		Spec:       corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "c", Image: "x"}}},
	}
	must(e.t, env(e.t).k8s.Create(ctx, p))
	return p
}

func (e *probeEnv) finish(step string, ok bool) {
	e.t.Helper()
	j := e.jobs()[step]
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
	must(e.t, env(e.t).k8s.Status().Update(ctx, &j))
}

// prepared finishes the prepare step and moves the probe on to write.
func (e *probeEnv) prepared() {
	e.t.Helper()
	e.finish("prepare", true)
	e.reconcile()
}

func (e *probeEnv) event(kind, name, reason, msg string) {
	e.t.Helper()
	must(e.t, env(e.t).k8s.Create(ctx, &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: "ev-", Namespace: probeNS},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: name, Namespace: probeNS},
		Reason:         reason, Message: msg, Type: corev1.EventTypeWarning,
	}))
}

func TestProbeStartsWithTheClaimAndOnlyThePrepareJob(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a", "node-b")
	e.reconcile()

	p := &corev1.PersistentVolumeClaim{}
	must(t, env(t).k8s.Get(ctx, client.ObjectKey{Namespace: probeNS, Name: e.pvcName()}, p))
	if got := *p.Spec.StorageClassName; got != "nfs" {
		t.Errorf("class = %q, want nfs", got)
	}
	if !slices.Equal(p.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}) ||
		*p.Spec.VolumeMode != corev1.PersistentVolumeFilesystem {
		t.Errorf("claim = %+v, want RWX Filesystem", p.Spec)
	}
	if q := p.Spec.Resources.Requests[corev1.ResourceStorage]; q.String() != "1Gi" {
		t.Errorf("size = %s, want 1Gi", q.String())
	}
	jobs := e.jobs()
	if len(jobs) != 1 || jobs["prepare"].Name == "" {
		t.Fatalf("jobs = %v, want only prepare", slices.Collect(mapKeys(jobs)))
	}
	if st := e.probe(); st.Result != model.ProbeRunning || st.Nonce != e.nonce || st.Step != "prepare" || st.StorageClassName != "nfs" {
		t.Errorf("status = %+v", st)
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestProbeRunsCheckOffTheFirstNodeThenConfirmOnIt(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a", "node-b")
	e.reconcile()
	e.prepared()
	e.podFor("write", "node-a")
	e.finish("write", true)
	e.reconcile()

	jobs := e.jobs()
	if len(jobs) != 3 {
		t.Fatalf("jobs = %v, want prepare, write and check", slices.Collect(mapKeys(jobs)))
	}
	aff := nodeTerm(t, jobs["check"])
	if aff.Key != "metadata.name" || aff.Operator != corev1.NodeSelectorOpNotIn || !slices.Equal(aff.Values, []string{"node-a"}) {
		t.Errorf("check affinity = %+v, want metadata.name NotIn [node-a]", aff)
	}
	if st := e.probe(); st.Step != "check" || st.Result != model.ProbeRunning {
		t.Errorf("status = %+v", st)
	}

	e.podFor("check", "node-b")
	e.finish("check", true)
	e.reconcile()
	aff = nodeTerm(t, e.jobs()["confirm"])
	if aff.Operator != corev1.NodeSelectorOpIn || !slices.Equal(aff.Values, []string{"node-a"}) {
		t.Errorf("confirm affinity = %+v, want In [node-a]", aff)
	}

	e.finish("confirm", true)
	e.reconcile()
	st := e.probe()
	if st.Result != model.ProbePassed || st.NodesTested != 2 || st.FinishedAt == nil || st.Step != "" {
		t.Errorf("status = %+v, want Passed on 2 nodes", st)
	}
	if !e.pvcGone() || !e.jobsGone() {
		t.Error("claim and jobs should be deleted after the probe")
	}
}

func nodeTerm(t *testing.T, j batchv1.Job) corev1.NodeSelectorRequirement {
	t.Helper()
	a := j.Spec.Template.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("job %s has no required node affinity", j.Name)
	}
	terms := a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchFields) != 1 {
		t.Fatalf("job %s terms = %+v", j.Name, terms)
	}
	return terms[0].MatchFields[0]
}

func TestProbeOnOneNodeRunsEverythingThereAndTestsOneNode(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.prepared()
	e.podFor("write", "node-a")
	e.finish("write", true)
	e.reconcile()
	if a := e.jobs()["check"].Spec.Template.Spec.Affinity; a != nil {
		t.Errorf("check on a one-node cluster has affinity %+v, want none", a)
	}
	e.finish("check", true)
	e.reconcile()
	e.finish("confirm", true)
	e.reconcile()
	if st := e.probe(); st.Result != model.ProbePassed || st.NodesTested != 1 {
		t.Errorf("status = %+v, want Passed on 1 node", st)
	}
}

func TestMountFailureEndsTheProbeAndCleansUp(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a", "node-b")
	e.reconcile()
	e.prepared()
	e.podFor("write", "node-a")
	e.finish("write", true)
	e.reconcile()
	pod := e.podFor("check", "node-b")
	e.event("Pod", pod.Name, "FailedMount", "MountVolume.SetUp failed: mount.nfs: command not found")
	e.reconcile()

	st := e.probe()
	if st.Result != model.ProbeFailed || st.FailedStep != "mount" {
		t.Fatalf("status = %+v, want Failed at mount", st)
	}
	if !slices.ContainsFunc(st.Evidence, func(s string) bool { return strings.Contains(s, "mount.nfs: command not found") }) {
		t.Errorf("evidence = %q, want the event text", st.Evidence)
	}
	if !e.pvcGone() || !e.jobsGone() {
		t.Error("claim and jobs should be deleted after a failure")
	}
}

func TestAttachFailureAndProvisioningFailure(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.prepared()
	pod := e.podFor("write", "node-a")
	e.event("Pod", pod.Name, "FailedAttachVolume", "AttachVolume.Attach failed")
	e.reconcile()
	if st := e.probe(); st.FailedStep != "attach" {
		t.Errorf("failedStep = %q, want attach", st.FailedStep)
	}

	e = newProbe(t, "node-a")
	e.reconcile()
	e.event("PersistentVolumeClaim", e.pvcName(), "ProvisioningFailed", "storageclass nfs not found")
	e.reconcile()
	st := e.probe()
	if st.FailedStep != "provision" || st.Result != model.ProbeFailed {
		t.Errorf("status = %+v, want Failed at provision", st)
	}
}

func TestFailedJobNamesItsStep(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a", "node-b")
	e.reconcile()
	e.prepared()
	e.podFor("write", "node-a")
	e.finish("write", true)
	e.reconcile()
	e.finish("check", false)
	e.reconcile()
	st := e.probe()
	if st.Result != model.ProbeFailed || st.FailedStep != "check" || len(st.Evidence) == 0 {
		t.Errorf("status = %+v, want Failed at check with evidence", st)
	}
}

func TestProbeTimesOut(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.now = e.now.Add(6 * time.Minute) // the default timeout is 5m
	e.reconcile()
	st := e.probe()
	if st.Result != model.ProbeFailed || st.FailedStep != "timeout" {
		t.Errorf("status = %+v, want Failed at timeout", st)
	}
	if !e.pvcGone() || !e.jobsGone() {
		t.Error("claim and jobs should be deleted after a timeout")
	}
}

func TestTheSameNonceRunsOnce(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.reconcile()
	if n := len(e.jobs()); n != 1 {
		t.Fatalf("jobs after two reconciles = %d, want 1", n)
	}
	e.prepared()
	e.podFor("write", "node-a")
	e.finish("write", true)
	e.reconcile()
	e.finish("check", true)
	e.reconcile()
	e.finish("confirm", true)
	e.reconcile()
	done := e.probe().FinishedAt

	e.reconcile()
	e.reconcile()
	if !e.probe().FinishedAt.Equal(done) || e.probe().Result != model.ProbePassed {
		t.Errorf("a finished nonce ran again: %+v", e.probe())
	}
	var claims corev1.PersistentVolumeClaimList
	must(t, env(t).k8s.List(ctx, &claims, client.InNamespace(probeNS), client.MatchingLabels{model.LabelProbe: probeHash(e.nonce)}))
	if len(claims.Items) != 1 {
		t.Errorf("claims = %d, want the one from the first run", len(claims.Items))
	}
}

func TestANewNonceStartsAgain(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.now = e.now.Add(6 * time.Minute)
	e.reconcile() // times out
	e.setNonce(e.nonce + "-again")
	e.now = time.Now()
	e.reconcile()
	if st := e.probe(); st.Result != model.ProbeRunning || st.Nonce != e.nonce || len(e.jobs()) != 1 {
		t.Errorf("status = %+v, jobs = %d", st, len(e.jobs()))
	}
}

func TestNoNonceNoProbe(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.setNonce("")
	e.reconcile()
	if st := e.probe(); st != nil {
		t.Errorf("status = %+v, want none", st)
	}
	if n := len(e.jobs()); n != 0 {
		t.Errorf("jobs = %d, want 0", n)
	}
}

func TestProbeRefusesAClaimItDoesNotOwn(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	must(t, env(t).k8s.Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: e.pvcName(), Namespace: probeNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}))
	if _, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "default"}}); err == nil {
		t.Fatal("want an error for a claim without the probe labels")
	}
	if len(e.jobs()) != 0 {
		t.Error("no job may run against a claim the probe did not make")
	}
}

// A failed probe on the shared candidate drops the choice
// to one disk per user.
func TestProbeFailureDowngradesChoice(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	class(t, "standard", "rancher.io/local-path", true, nil)
	class(t, "nfs", "nfs.csi.k8s.io", false, nil)
	run(t, newReconciler(t))
	if ch := get(t).Status.Choice; ch.Mode != model.HomeModeShared || ch.HomeClass != "nfs" {
		t.Fatalf("before the probe: choice = %+v, want shared on nfs", ch)
	}

	e.reconcile()
	e.prepared()
	pod := e.podFor("write", "node-a")
	e.event("Pod", pod.Name, "FailedMount", "mount.nfs: command not found")
	e.reconcile()
	run(t, newReconciler(t))

	ch := get(t).Status.Choice
	if ch.Mode != model.HomeModePerUser || ch.HomeClass != "standard" {
		t.Errorf("after a failed probe: choice = %+v, want perUser on standard", ch)
	}
}

func TestProbeJobsRunUnderRestrictedPodSecurity(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.prepared()
	j := e.jobs()["write"]
	pod := j.Spec.Template.Spec
	if pod.SecurityContext.FSGroup != nil {
		t.Error("the write step must not set fsGroup: it would hide the ownership the probe is there to check")
	}
	sc := pod.Containers[0].SecurityContext
	if pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		*pod.SecurityContext.RunAsUser != 1000 || *pod.SecurityContext.RunAsGroup != 100 ||
		pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
		*sc.AllowPrivilegeEscalation || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("security context = %+v / %+v", pod.SecurityContext, sc)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("service account token must not be mounted")
	}
	if pod.Containers[0].Image != testImage.Ref || !pullsTestImage(pod) {
		t.Errorf("image %q, pull secrets %v", pod.Containers[0].Image, pod.ImagePullSecrets)
	}
	if *j.Spec.BackoffLimit != 0 || *j.Spec.TTLSecondsAfterFinished != 300 || *j.Spec.ActiveDeadlineSeconds != 300 {
		t.Errorf("job spec = backoff %d ttl %d deadline %d", *j.Spec.BackoffLimit, *j.Spec.TTLSecondsAfterFinished, *j.Spec.ActiveDeadlineSeconds)
	}
	if j.Labels[model.LabelRole] != model.RoleStorageProbe || len(j.OwnerReferences) != 0 {
		t.Errorf("job labels = %v, owners = %v", j.Labels, j.OwnerReferences)
	}
	if got := pod.Containers[0].Args; !slices.Equal(got, []string{"storage", "probe-step", "--step", "write", "--dir", "/probe"}) {
		t.Errorf("args = %v", got)
	}
}

// A fresh volume's root is often root-owned. The prepare step readies it the
// way the home prepare Job readies a home (fsGroup, one volume); the steps
// after it run without fsGroup.
func TestPrepareStepReadiesTheVolumeRootWithFSGroupOnOneVolume(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	pod := e.jobs()["prepare"].Spec.Template.Spec
	if pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 100 {
		t.Errorf("prepare fsGroup = %v, want 100 (the probe gid)", pod.SecurityContext.FSGroup)
	}
	if p := pod.SecurityContext.FSGroupChangePolicy; p == nil || *p != corev1.FSGroupChangeOnRootMismatch {
		t.Errorf("prepare fsGroupChangePolicy = %v, want OnRootMismatch", p)
	}
	if len(pod.Volumes) != 1 {
		t.Errorf("prepare mounts %d volumes, want exactly 1", len(pod.Volumes))
	}
	if got := pod.Containers[0].Args; !slices.Equal(got, []string{"storage", "probe-step", "--step", "prepare", "--dir", "/probe"}) {
		t.Errorf("args = %v", got)
	}
	e.prepared()
	e.podFor("write", "node-a")
	e.finish("write", true)
	e.reconcile()
	e.finish("check", true)
	e.reconcile()
	for step, j := range e.jobs() {
		if step == "prepare" {
			continue
		}
		if j.Spec.Template.Spec.SecurityContext.FSGroup != nil {
			t.Errorf("%s job sets fsGroup", step)
		}
	}
}

// A failed prepare is the probe's answer: homes could not be readied there.
func TestFailedPrepareNamesItsStep(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	e.finish("prepare", false)
	e.reconcile()
	if st := e.probe(); st.Result != model.ProbeFailed || st.FailedStep != "prepare" {
		t.Errorf("status = %+v, want Failed at prepare", st)
	}
}

// A finished run whose cleanup failed has left its claim: the next nonce must
// still remove it.
func TestANewNonceCleansUpAFinishedRunWhoseCleanupFailed(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	oldClaim := e.pvcName()
	// finish() records the result before it deletes anything: a failed delete
	// leaves a finished status with the claim still there.
	cs := get(t)
	cs.Status.Probe.Result = model.ProbeFailed
	cs.Status.Probe.FailedStep = "prepare"
	must(t, env(t).db.Storage().SetProbeStatus(ctx, cs.Status.Probe, cs.Status.ProbeResults))

	e.setNonce(e.nonce + "-again")
	e.reconcile()

	p := &corev1.PersistentVolumeClaim{}
	err := env(t).k8s.Get(ctx, client.ObjectKey{Namespace: probeNS, Name: oldClaim}, p)
	if !apierrors.IsNotFound(err) && p.DeletionTimestamp == nil {
		t.Errorf("the finished run's claim %s is still there", oldClaim)
	}
}

func (e *probeEnv) setNonce(n string) {
	e.t.Helper()
	setRequests(e.t, func(r *model.StorageRequests) { r.Probe.Nonce = n })
	e.nonce = n
}

// A second `javo storage verify` while the first still runs must not leave the
// first run's volume behind: a leaked volume is billed.
func TestANewNonceMidRunCleansUpThePreviousRun(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	oldClaim, oldJobs := e.pvcName(), e.jobs()
	if len(oldJobs) != 1 {
		t.Fatalf("jobs = %d, want the write job", len(oldJobs))
	}

	e.setNonce(e.nonce + "-again")
	e.reconcile()

	p := &corev1.PersistentVolumeClaim{}
	err := env(t).k8s.Get(ctx, client.ObjectKey{Namespace: probeNS, Name: oldClaim}, p)
	if !apierrors.IsNotFound(err) && p.DeletionTimestamp == nil {
		t.Errorf("the previous run's claim %s is still there", oldClaim)
	}
	for _, j := range oldJobs {
		got := &batchv1.Job{}
		err := env(t).k8s.Get(ctx, client.ObjectKeyFromObject(&j), got)
		if !apierrors.IsNotFound(err) && got.DeletionTimestamp == nil {
			t.Errorf("the previous run's job %s is still there", j.Name)
		}
	}
	if st := e.probe(); st.Nonce != e.nonce || st.Result != model.ProbeRunning || len(e.jobs()) != 1 {
		t.Errorf("status = %+v, jobs = %d, want the new run started", st, len(e.jobs()))
	}
}

// failNFS runs a probe on nfs to a mount failure and returns the env.
func failNFS(t *testing.T) *probeEnv {
	t.Helper()
	e := newProbe(t, "node-a")
	class(t, "standard", "rancher.io/local-path", true, nil)
	class(t, "nfs", "nfs.csi.k8s.io", false, nil)
	e.reconcile()
	e.prepared()
	pod := e.podFor("write", "node-a")
	e.event("Pod", pod.Name, "FailedMount", "mount.nfs: command not found")
	e.reconcile()
	run(t, newReconciler(t))
	if ch := get(t).Status.Choice; ch.Mode != model.HomeModePerUser {
		t.Fatalf("after the failed probe: choice = %+v, want perUser", ch)
	}
	return e
}

// Re-probing a class that failed must not flip the choice
// back to shared while the new run is still going.
func TestReprobeOfAFailedClassKeepsTheChoiceWhileRunning(t *testing.T) {
	t.Parallel()
	e := failNFS(t)
	e.setNonce(e.nonce + "-again")
	e.reconcile()
	if st := e.probe(); st.Result != model.ProbeRunning {
		t.Fatalf("status = %+v, want Running", st)
	}
	run(t, newReconciler(t))
	if ch := get(t).Status.Choice; ch.Mode != model.HomeModePerUser || ch.HomeClass != "standard" {
		t.Errorf("while re-probing: choice = %+v, want perUser on standard", ch)
	}
}

// Probing class B must not erase the verified failure of class A.
func TestProbingAnotherClassKeepsAnEarlierFailure(t *testing.T) {
	t.Parallel()
	e := failNFS(t)
	class(t, "efs", "efs.csi.aws.com", false, nil)
	setRequests(t, func(r *model.StorageRequests) { r.Probe.StorageClassName = "efs" })
	e.setNonce(e.nonce + "-efs")

	e.reconcile()
	run(t, newReconciler(t))

	for _, c := range get(t).Status.Classes {
		if c.Name == "nfs" && (c.RWXFile.Provenance != model.ProvenanceVerified || c.RWXFile.Value) {
			t.Errorf("nfs rwxFile = %+v, want the verified failure kept", c.RWXFile)
		}
	}
}

// nodeWith makes a schedulable, Ready node, as a kubelet would, and then
// applies mutate. The API server taints a fresh node not-ready; with no
// kubelet in envtest the helper clears that.
func nodeWith(t *testing.T, name string, mutate func(*corev1.Node)) {
	t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
	must(t, env(t).k8s.Create(ctx, n))
	n.Spec.Taints = nil
	must(t, env(t).k8s.Update(ctx, n))
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	mutate(n)
	conds := n.Status.Conditions
	must(t, env(t).k8s.Update(ctx, n)) // spec changes; the reply drops the status ones
	n.Status.Conditions = conds
	must(t, env(t).k8s.Status().Update(ctx, n))
}

// A tainted control plane (or a cordoned node) cannot run the Jobs, so it
// must not make the cluster look like it has two nodes: the check Job's
// "not on node A" rule could never be met.
func TestOnlyNodesTheJobsCanRunOnCountAsNodes(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*corev1.Node){
		"tainted": func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}}
		},
		"cordoned": func(n *corev1.Node) { n.Spec.Unschedulable = true },
		"not ready": func(n *corev1.Node) {
			n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newProbe(t, "worker")
			nodeWith(t, "cp", mutate)
			e.reconcile()
			e.prepared()
			e.podFor("write", "worker")
			e.finish("write", true)
			e.reconcile()
			if aff := e.jobs()["check"].Spec.Template.Spec.Affinity; aff != nil {
				t.Errorf("check job affinity = %+v, want none on a one-schedulable-node cluster", aff)
			}
			e.finish("check", true)
			e.reconcile()
			e.finish("confirm", true)
			e.reconcile()
			if st := e.probe(); st.Result != model.ProbePassed || st.NodesTested != 1 {
				t.Errorf("status = %+v, want Passed on 1 node", st)
			}
		})
	}
}

// A timeout caused by scheduling must say so.
func TestTimeoutEvidenceIncludesSchedulingEvents(t *testing.T) {
	t.Parallel()
	e := newProbe(t, "node-a")
	e.reconcile()
	pod := e.podFor("prepare", "")
	e.event("Pod", pod.Name, "FailedScheduling", "0/1 nodes are available: 1 node(s) had untolerated taint")
	e.now = e.now.Add(6 * time.Minute)
	e.reconcile()
	st := e.probe()
	if st.FailedStep != "timeout" || !slices.ContainsFunc(st.Evidence, func(s string) bool { return strings.Contains(s, "untolerated taint") }) {
		t.Errorf("status = %+v, want a timeout whose evidence has the FailedScheduling message", st)
	}
}

// nodesTested says where the steps ran, not how many nodes the cluster has
// when the probe ends: nodes join and leave while it runs.
func TestNodesTestedFollowsWhereTheStepsRanNotTheNodeCount(t *testing.T) {
	t.Parallel()
	t.Run("a node joins after check ran on the write node", func(t *testing.T) {
		e := newProbe(t, "node-a")
		e.reconcile()
		e.prepared()
		e.podFor("write", "node-a")
		e.finish("write", true)
		e.reconcile()
		e.podFor("check", "node-a")
		e.finish("check", true)
		e.reconcile()
		nodeWith(t, "node-b", func(*corev1.Node) {})
		e.finish("confirm", true)
		e.reconcile()
		if st := e.probe(); st.Result != model.ProbePassed || st.NodesTested != 1 {
			t.Errorf("status = %+v, want Passed on 1 node: check ran where write did", st)
		}
	})
	t.Run("the check node goes NotReady after check ran there", func(t *testing.T) {
		e := newProbe(t, "node-a", "node-b")
		e.reconcile()
		e.prepared()
		e.podFor("write", "node-a")
		e.finish("write", true)
		e.reconcile()
		e.podFor("check", "node-b")
		e.finish("check", true)
		e.reconcile()
		n := &corev1.Node{}
		must(t, env(t).k8s.Get(ctx, client.ObjectKey{Name: "node-b"}, n))
		n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
		must(t, env(t).k8s.Status().Update(ctx, n))
		e.finish("confirm", true)
		e.reconcile()
		if st := e.probe(); st.Result != model.ProbePassed || st.NodesTested != 2 {
			t.Errorf("status = %+v, want Passed on 2 nodes: check ran on node-b", st)
		}
	})
}
