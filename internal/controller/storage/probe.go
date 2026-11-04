package storage

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/probestep"
	"github.com/nebari-dev/javo/internal/store"
)

// labelProbeStep carries the probe step a Job runs.
const labelProbeStep = model.Group + "/probe-step"

// Failed steps that are not one of the three Job steps.
const (
	failProvision = "provision"
	failAttach    = "attach"
	failMount     = "mount"
	failTimeout   = "timeout"
)

const (
	probeMountPath     = "/probe"
	probeUID           = 1000
	probeGID           = 100
	probeDefaultWait   = 5 * time.Minute
	probePollInterval  = 2 * time.Second
	probeJobTTL        = 300
	probeMaxEvidence   = 6
	probeClaimSize     = "1Gi"
	probeNamePrefix    = model.ProductName + "-probe-"
	kubeHostnameFields = "metadata.name"
)

// probeSteps is the order the Jobs run in.
var probeSteps = []string{probestep.StepPrepare, probestep.StepWrite, probestep.StepCheck, probestep.StepConfirm}

// ProbeReconciler runs the opt-in two-node probe of one StorageClass: a
// claim, then a write Job, a check Job on another node and a confirm Job back
// on the first node. It runs only when an admin asks for it (a new probe
// nonce in the storage requests), and records the outcome in the probe
// findings, where the classification reads it.
//
// Progress lives on the claim (an annotation), not in the Jobs, so Jobs that
// the TTL removes do not restart the probe. Claim, Jobs, Pods and Events are
// read straight from the API: the manager's cache holds none of them.
type ProbeReconciler struct {
	client.Client
	Rows
	// Reader reads claims, Jobs, Pods and Events without the cache.
	Reader client.Reader
	// Namespace is where the probe's claim and Jobs live.
	Namespace string
	// Image is the javo image the Jobs run (`javo storage probe-step`).
	Image podspec.Image
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// RBAC: namespaced rules for the probe's own namespace are in the chart's
// controller Role; the markers keep the merged ClusterRole honest.
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;create;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;create;delete;patch

func (r *ProbeReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// probeHash is the short name of a nonce, used in object names and labels.
func probeHash(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])[:8]
}

func (r *ProbeReconciler) pvcName(nonce string) string { return probeNamePrefix + probeHash(nonce) }

func probeLabels(nonce string) map[string]string {
	return map[string]string{model.LabelRole: model.RoleStorageProbe, model.LabelProbe: probeHash(nonce)}
}

// ours is true for an object carrying this probe's labels. Anything else
// under the same name is not ours to use or delete.
func ours(o client.Object, nonce string) bool {
	for k, v := range probeLabels(nonce) {
		if o.GetLabels()[k] != v {
			return false
		}
	}
	return true
}

// Reconcile moves the probe named by the requested nonce one or more steps on.
func (r *ProbeReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	cs, err := r.cluster(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	nonce := cs.Spec.Probe.Nonce
	if nonce == "" {
		return ctrl.Result{}, nil
	}
	st := cs.Status.Probe
	if st == nil || st.Nonce != nonce {
		// Cleanup only finds the current nonce's objects, so the previous
		// run's claim (a billed volume) would be left behind. Clean it up
		// first whatever its result: a finished run whose cleanup failed has
		// its claim still. Cleanup is idempotent. A failure retries, with the
		// old status still in place.
		if st != nil {
			if err := r.cleanup(ctx, st.Nonce, cs); err != nil {
				return ctrl.Result{}, err
			}
		}
		st = &model.ProbeStatus{
			Nonce: nonce, StorageClassName: probeClass(cs), Result: model.ProbeRunning, Step: probestep.StepPrepare,
		}
		if err := r.setStatus(ctx, cs, st); err != nil {
			return ctrl.Result{}, err
		}
	}
	if st.Result != model.ProbeRunning {
		return ctrl.Result{}, r.cleanup(ctx, nonce, cs)
	}
	if st.StorageClassName == "" {
		return ctrl.Result{}, r.finish(ctx, cs, st, model.ProbeFailed, failProvision, []string{"no StorageClass to probe: name one or set a default"}, 0)
	}
	return r.run(ctx, cs, st)
}

// probeClass is the class to probe: the one named, else the shared
// candidate, else the home class.
func probeClass(cs *model.ClusterStorage) string {
	if cs.Spec.Probe.StorageClassName != "" {
		return cs.Spec.Probe.StorageClassName
	}
	if ch := cs.Status.Choice; ch != nil {
		return cmp.Or(ch.SharedClass, ch.HomeClass)
	}
	return ""
}

func (r *ProbeReconciler) run(ctx context.Context, cs *model.ClusterStorage, st *model.ProbeStatus) (ctrl.Result, error) {
	pvc, err := r.claim(ctx, cs, st)
	if err != nil {
		return ctrl.Result{}, err
	}
	timeout := probeDefaultWait
	if t := cs.Spec.Probe.Timeout; t != nil {
		timeout = t.Duration
	}
	failNow := func(step string, evidence ...string) (ctrl.Result, error) {
		return ctrl.Result{}, r.finish(ctx, cs, st, model.ProbeFailed, step, evidence, 0)
	}
	events, err := r.events(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if r.now().After(pvc.CreationTimestamp.Add(timeout)) {
		ev, err := r.timeoutEvidence(ctx, pvc, events, timeout)
		if err != nil {
			return ctrl.Result{}, err
		}
		return failNow(failTimeout, ev...)
	}
	if ev := eventsFor(events, "PersistentVolumeClaim", pvc.Name, "ProvisioningFailed"); len(ev) > 0 {
		return failNow(failProvision, ev...)
	}
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return ctrl.Result{}, fmt.Errorf("list nodes: %w", err)
	}
	twoNodes := schedulableNodes(nodes.Items) > 1

	for {
		step := nextStep(pvc.Annotations[model.AnnotationProbeDone])
		if step == "" {
			return ctrl.Result{}, r.finish(ctx, cs, st, model.ProbePassed, "", nil, nodesTested(pvc))
		}
		job, err := r.job(ctx, cs, pvc, step, twoNodes)
		if err != nil {
			return ctrl.Result{}, err
		}
		pods, err := r.jobPods(ctx, job)
		if err != nil {
			return ctrl.Result{}, err
		}
		for _, p := range pods {
			if ev := eventsFor(events, "Pod", p.Name, "FailedAttachVolume"); len(ev) > 0 {
				return failNow(failAttach, ev...)
			}
			if ev := eventsFor(events, "Pod", p.Name, "FailedMount"); len(ev) > 0 {
				return failNow(failMount, ev...)
			}
		}
		switch {
		case jobHas(job, batchv1.JobFailed):
			return failNow(step, jobEvidence(job, pods)...)
		case jobHas(job, batchv1.JobComplete):
			if err := r.advance(ctx, pvc, step, pods); err != nil {
				var se *stepError
				if errors.As(err, &se) {
					return failNow(step, se.Error())
				}
				return ctrl.Result{}, err
			}
			continue
		}
		if st.Step != step {
			next := *st // st points into cs: setStatus needs cs unchanged to diff against
			next.Step = step
			if err := r.setStatus(ctx, cs, &next); err != nil {
				return ctrl.Result{}, err
			}
			st = &next
		}
		return ctrl.Result{RequeueAfter: probePollInterval}, nil
	}
}

// timeoutEvidence says that the probe timed out, and why the step in progress
// had not started when its pod could not be scheduled: that points at the
// cluster's nodes, not at the storage.
func (r *ProbeReconciler) timeoutEvidence(ctx context.Context, pvc *corev1.PersistentVolumeClaim, events []corev1.Event, timeout time.Duration) ([]string, error) {
	out := []string{fmt.Sprintf("the probe did not finish within %s", timeout)}
	step := nextStep(pvc.Annotations[model.AnnotationProbeDone])
	if step == "" {
		return out, nil
	}
	pods, err := r.jobPods(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: pvc.Name + "-" + step}})
	if err != nil {
		return nil, err
	}
	for _, p := range pods {
		out = append(out, eventsFor(events, "Pod", p.Name, "FailedScheduling")...)
	}
	return out, nil
}

// schedulableNodes counts the nodes a probe Job (which tolerates no taint)
// can run on: not cordoned, not NotReady, no NoSchedule or NoExecute taint.
func schedulableNodes(nodes []corev1.Node) int {
	n := 0
	for _, node := range nodes {
		if node.Spec.Unschedulable {
			continue
		}
		if slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}) {
			continue
		}
		if slices.ContainsFunc(node.Status.Conditions, func(c corev1.NodeCondition) bool {
			return c.Type == corev1.NodeReady && c.Status != corev1.ConditionTrue
		}) {
			continue
		}
		n++
	}
	return n
}

// nodesTested is how many nodes the steps ran on, from the nodes recorded on
// the claim: 2 only when check ran on another node than write.
func nodesTested(pvc *corev1.PersistentVolumeClaim) int32 {
	write, check := pvc.Annotations[model.AnnotationProbeNode], pvc.Annotations[model.AnnotationProbeCheckNode]
	if check != "" && check != write {
		return 2
	}
	return 1
}

// nextStep is the step after the last finished one; "" when all are done.
func nextStep(done string) string {
	i := slices.Index(probeSteps, done)
	if done != "" && i < 0 {
		return probeSteps[0]
	}
	if i+1 >= len(probeSteps) {
		return ""
	}
	return probeSteps[i+1]
}

// claim returns the probe's PVC, creating it on the first pass.
func (r *ProbeReconciler) claim(ctx context.Context, cs *model.ClusterStorage, st *model.ProbeStatus) (*corev1.PersistentVolumeClaim, error) {
	key := client.ObjectKey{Namespace: r.Namespace, Name: r.pvcName(st.Nonce)}
	pvc := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, key, pvc); {
	case apierrors.IsNotFound(err):
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: probeLabels(st.Nonce)},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:       ptr.To(corev1.PersistentVolumeFilesystem),
				StorageClassName: ptr.To(st.StorageClassName),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(probeClaimSize)},
				},
			},
		}
		if err := r.Create(ctx, pvc); err != nil {
			return nil, fmt.Errorf("create probe claim: %w", err)
		}
		return pvc, nil
	case err != nil:
		return nil, err
	case !ours(pvc, st.Nonce):
		return nil, fmt.Errorf("claim %s exists and is not a storage probe claim; not using it", key)
	}
	return pvc, nil
}

// job returns the Job of a step, creating it when missing.
func (r *ProbeReconciler) job(ctx context.Context, cs *model.ClusterStorage, pvc *corev1.PersistentVolumeClaim, step string, twoNodes bool) (*batchv1.Job, error) {
	nonce := cs.Spec.Probe.Nonce
	key := client.ObjectKey{Namespace: r.Namespace, Name: pvc.Name + "-" + step}
	job := &batchv1.Job{}
	switch err := r.Reader.Get(ctx, key, job); {
	case apierrors.IsNotFound(err):
		timeout := probeDefaultWait
		if t := cs.Spec.Probe.Timeout; t != nil {
			timeout = t.Duration
		}
		job = r.buildJob(key, nonce, step, pvc, timeout, nodeAffinity(step, twoNodes, pvc.Annotations[model.AnnotationProbeNode]))
		if err := r.Create(ctx, job); err != nil {
			return nil, fmt.Errorf("create %s job: %w", step, err)
		}
		return job, nil
	case err != nil:
		return nil, err
	case !ours(job, nonce):
		return nil, fmt.Errorf("job %s exists and is not a storage probe job; not using it", key)
	}
	return job, nil
}

// nodeAffinity keeps check off the node write ran on and confirm on it. On a
// one-node cluster nothing is constrained.
func nodeAffinity(step string, twoNodes bool, nodeA string) *corev1.Affinity {
	if !twoNodes || nodeA == "" || step == probestep.StepWrite {
		return nil
	}
	op := corev1.NodeSelectorOpNotIn
	if step == probestep.StepConfirm {
		op = corev1.NodeSelectorOpIn
	}
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchFields: []corev1.NodeSelectorRequirement{{Key: kubeHostnameFields, Operator: op, Values: []string{nodeA}}},
		}}},
	}}
}

// buildJob is one probe step as a Job that passes Pod Security "restricted".
// Only the prepare step sets fsGroup (it mounts the one volume, like a home's
// prepare Job); the steps after it set none, which would hide the ownership
// the probe checks.
func (r *ProbeReconciler) buildJob(key client.ObjectKey, nonce, step string, pvc *corev1.PersistentVolumeClaim, timeout time.Duration, aff *corev1.Affinity) *batchv1.Job {
	labels := probeLabels(nonce)
	podSec := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		RunAsUser:      ptr.To(int64(probeUID)),
		RunAsGroup:     ptr.To(int64(probeGID)),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if step == probestep.StepPrepare {
		podSec.FSGroup = ptr.To(int64(probeGID))
		podSec.FSGroupChangePolicy = ptr.To(corev1.FSGroupChangeOnRootMismatch)
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: key.Name, Namespace: key.Namespace,
			Labels: map[string]string{model.LabelRole: labels[model.LabelRole], model.LabelProbe: labels[model.LabelProbe], labelProbeStep: step},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			TTLSecondsAfterFinished: ptr.To(int32(probeJobTTL)),
			ActiveDeadlineSeconds:   ptr.To(int64(timeout.Seconds())),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					Affinity:                     aff,
					SecurityContext:              podSec,
					Containers: []corev1.Container{{
						Name:                     "probe",
						Image:                    r.Image.Ref,
						Args:                     []string{"storage", "probe-step", "--step", step, "--dir", probeMountPath},
						VolumeMounts:             []corev1.VolumeMount{{Name: "probe", MountPath: probeMountPath}},
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: []corev1.Volume{{Name: "probe", VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name},
					}}},
				},
			},
		},
	}
	r.Image.Pull(&job.Spec.Template.Spec)
	return job
}

func (r *ProbeReconciler) jobPods(ctx context.Context, job *batchv1.Job) ([]corev1.Pod, error) {
	var l corev1.PodList
	if err := r.Reader.List(ctx, &l, client.InNamespace(r.Namespace), client.MatchingLabels{batchv1.JobNameLabel: job.Name}); err != nil {
		return nil, fmt.Errorf("list probe pods: %w", err)
	}
	return l.Items, nil
}

func (r *ProbeReconciler) events(ctx context.Context) ([]corev1.Event, error) {
	var l corev1.EventList
	if err := r.Reader.List(ctx, &l, client.InNamespace(r.Namespace)); err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return l.Items, nil
}

// eventsFor returns the messages of events with the given reason on the
// named object. Events are matched on reason only.
func eventsFor(events []corev1.Event, kind, name, reason string) []string {
	var out []string
	for _, e := range events {
		if e.InvolvedObject.Kind == kind && e.InvolvedObject.Name == name && e.Reason == reason {
			out = append(out, fmt.Sprintf("%s %s: %s", reason, name, e.Message))
		}
	}
	return out
}

func jobHas(j *batchv1.Job, t batchv1.JobConditionType) bool {
	return slices.ContainsFunc(j.Status.Conditions, func(c batchv1.JobCondition) bool {
		return c.Type == t && c.Status == corev1.ConditionTrue
	})
}

// jobEvidence is the Job's failure condition and what its pods' containers
// said when they stopped.
func jobEvidence(j *batchv1.Job, pods []corev1.Pod) []string {
	var out []string
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			out = append(out, fmt.Sprintf("job %s failed: %s", j.Name, strings.TrimSpace(c.Reason+" "+c.Message)))
		}
	}
	for _, p := range pods {
		for _, cs := range p.Status.ContainerStatuses {
			if t := cs.State.Terminated; t != nil && t.Message != "" {
				out = append(out, strings.TrimSpace(t.Message))
			}
		}
	}
	return out
}

// stepError is a step that cannot be recorded as done.
type stepError struct{ msg string }

func (e *stepError) Error() string { return e.msg }

// advance records a finished step on the claim. After write it also records
// the node the step ran on, which check and confirm are pinned against; after
// check, the node check ran on, which says how many nodes were tested.
func (r *ProbeReconciler) advance(ctx context.Context, pvc *corev1.PersistentVolumeClaim, step string, pods []corev1.Pod) error {
	base := pvc.DeepCopy()
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	i := slices.IndexFunc(pods, func(p corev1.Pod) bool { return p.Spec.NodeName != "" })
	switch {
	case step == probestep.StepWrite && i < 0:
		return &stepError{"could not tell which node the write step ran on: its pod is gone"}
	case step == probestep.StepWrite:
		pvc.Annotations[model.AnnotationProbeNode] = pods[i].Spec.NodeName
	case step == probestep.StepCheck && i >= 0:
		// A check pod already gone leaves no node: nodesTested then says 1,
		// since crossing nodes cannot be shown.
		pvc.Annotations[model.AnnotationProbeCheckNode] = pods[i].Spec.NodeName
	}
	pvc.Annotations[model.AnnotationProbeDone] = step
	if err := r.Patch(ctx, pvc, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("record probe progress: %w", err)
	}
	return nil
}

// setStatus writes the probe in progress. A finished result is also kept per
// class in the probe results, which classification reads: a run in progress,
// or a probe of another class, then does not erase what an earlier run found.
func (r *ProbeReconciler) setStatus(ctx context.Context, cs *model.ClusterStorage, st *model.ProbeStatus) error {
	results := cs.Status.ProbeResults
	if st.Result != model.ProbeRunning && st.StorageClassName != "" {
		kept := slices.DeleteFunc(slices.Clone(results), func(p model.ProbeStatus) bool {
			return p.StorageClassName == st.StorageClassName
		})
		results = append(kept, *st)
	}
	if err := r.Store.Storage().SetProbeStatus(ctx, st, results); err != nil {
		return err
	}
	cs.Status.Probe, cs.Status.ProbeResults = st, results
	return nil
}

// finish records the outcome first and then deletes what the probe made, so
// a failed delete is retried by the next reconcile of a finished nonce.
func (r *ProbeReconciler) finish(ctx context.Context, cs *model.ClusterStorage, st *model.ProbeStatus, result model.ProbeResult, failedStep string, evidence []string, nodes int32) error {
	if len(evidence) > probeMaxEvidence {
		evidence = evidence[:probeMaxEvidence]
	}
	done := metav1.NewTime(r.now())
	out := *st
	out.Result, out.FailedStep, out.Evidence, out.FinishedAt, out.NodesTested, out.Step = result, failedStep, evidence, &done, nodes, ""
	if err := r.setStatus(ctx, cs, &out); err != nil {
		return err
	}
	return r.cleanup(ctx, st.Nonce, cs)
}

// cleanup deletes the probe's Jobs and claim. Only objects carrying the
// probe's labels are touched, and each delete requires the UID just read.
func (r *ProbeReconciler) cleanup(ctx context.Context, nonce string, _ *model.ClusterStorage) error {
	var jobs batchv1.JobList
	if err := r.Reader.List(ctx, &jobs, client.InNamespace(r.Namespace), client.MatchingLabels(probeLabels(nonce))); err != nil {
		return fmt.Errorf("list probe jobs: %w", err)
	}
	for i := range jobs.Items {
		if err := r.deleteOwned(ctx, &jobs.Items[i]); err != nil {
			return err
		}
	}
	pvc := &corev1.PersistentVolumeClaim{}
	switch err := r.Reader.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: r.pvcName(nonce)}, pvc); {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return err
	case !ours(pvc, nonce):
		return fmt.Errorf("claim %s/%s is not a storage probe claim; not deleting it", pvc.Namespace, pvc.Name)
	}
	return r.deleteOwned(ctx, pvc)
}

func (r *ProbeReconciler) deleteOwned(ctx context.Context, o client.Object) error {
	if o.GetDeletionTimestamp() != nil {
		return nil
	}
	uid := o.GetUID()
	err := r.Delete(ctx, o, client.Preconditions{UID: &uid}, client.PropagationPolicy(metav1.DeletePropagationBackground))
	return client.IgnoreNotFound(err)
}

// SetupWithManager reconciles at start and whenever an admin's request
// changes, and polls while a probe runs.
func (r *ProbeReconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Reader == nil || r.Namespace == "" || r.Image.Ref == "" {
		return errors.New("storage.ProbeReconciler needs Store, Reader, Namespace and Image")
	}
	all := func(context.Context) ([]reconcile.Request, error) { return singleton, nil }
	return ctrl.NewControllerManagedBy(mgr).Named("storage-probe").
		WatchesRawSource(dbsource.For(feed.Subscribe(), store.TableStorageSettings, 0, all)).
		Complete(r)
}
