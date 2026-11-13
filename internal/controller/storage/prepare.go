package storage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
)

// prepareJobSpec is what differs between the Jobs that ready a volume root.
type prepareJobSpec struct {
	Key    client.ObjectKey
	Labels map[string]string
	Image  podspec.Image
	Claim  string
	// MountPath is where the claim is mounted; Dir is the directory to ready
	// (the mount itself, or a directory made inside it).
	MountPath, Dir string
	// UID and GID run the Job, own the directory and are its fsGroup.
	UID, GID int64
	Mode     string
	NoMkdir  bool
	Affinity *corev1.Affinity
}

// buildPrepareJob is the Job that readies one volume root. It mounts only the
// one claim: fsGroup re-owns everything a pod mounts, so a Job with fsGroup
// must never see another volume. It passes Pod Security "restricted".
func buildPrepareJob(p prepareJobSpec) *batchv1.Job {
	args := []string{"storage", "prepare", "--dir", p.Dir,
		"--uid", fmt.Sprint(p.UID), "--gid", fmt.Sprint(p.GID), "--mode", p.Mode}
	if p.NoMkdir {
		args = append(args, "--no-mkdir")
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: p.Key.Name, Namespace: p.Key.Namespace, Labels: p.Labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(1)),
			TTLSecondsAfterFinished: ptr.To(int32(prepareJobTTL)),
			ActiveDeadlineSeconds:   ptr.To(int64(prepareDeadline)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: p.Labels},
				Spec: corev1.PodSpec{
					Affinity:                     p.Affinity,
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					EnableServiceLinks:           ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:        ptr.To(true),
						RunAsUser:           ptr.To(p.UID),
						RunAsGroup:          ptr.To(p.GID),
						FSGroup:             ptr.To(p.GID),
						FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
						SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:                     "prepare",
						Image:                    p.Image.Ref,
						Args:                     args,
						VolumeMounts:             []corev1.VolumeMount{{Name: "volume", MountPath: p.MountPath}},
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
					}},
					Volumes: []corev1.Volume{{Name: "volume", VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: p.Claim},
					}}},
				},
			},
		},
	}
	p.Image.Pull(&job.Spec.Template.Spec)
	return job
}

// prepareJobNameFor is the name of the Job that readies a claim; a long claim
// name is cut and hashed so the Job's pod labels stay within 63 characters.
func prepareJobNameFor(claim string) string {
	return naming.NamespaceName(model.ProductName+"-prepare", claim)
}

// prepareOutcome is where an owner's prepare Job stands.
type prepareOutcome int

const (
	prepareRunning prepareOutcome = iota
	prepareDone
	// prepareForeign: a Job in the name is not this owner's; it is never used.
	prepareForeign
	// prepareRetry: the last Job failed; a new one follows when its TTL removes it.
	prepareRetry
	// prepareGaveUp: the last allowed Job failed and stays as the evidence.
	prepareGaveUp
)

type prepareResult struct {
	outcome  prepareOutcome
	evidence []string
	// said is true when a container reported why it stopped.
	said bool
	// unmounted is what the kubelet kept reporting while it failed to attach
	// or mount the volume for the Job's pod, past mountGrace; empty when the
	// volume mounted or it is too early to tell. A running Job that has it
	// will not get further on its own.
	unmounted []string
}

// mountGrace is how long a pod may keep failing to attach or mount its volume
// before that counts as a failure. It is the kubelet's own wait for a pod's
// volumes: after it the kubelet reports the pod unable to mount.
const mountGrace = 2 * time.Minute

// mountReasons are the kubelet's Warning reasons for a volume it cannot give a pod.
var mountReasons = []string{"FailedMount", "FailedAttachVolume"}

// jobOwner is the row a helper Job is made for (a home, a folder). Jobs carry
// the row's id in a label instead of an owner reference, so the controller
// finds its own and never uses one it did not make.
type jobOwner struct {
	Namespace string
	// Label is the label key that carries the row id on its Jobs.
	Label, ID string
	// Attempts is how many prepare Jobs were made for the row so far; Count
	// stores a new count on the row before a Job is made.
	Attempts int
	Count    func(ctx context.Context, attempts int) error
}

// ownsJob is true for a Job made for o in role.
func (o jobOwner) ownsJob(j *batchv1.Job, role string) bool {
	return j.Labels[o.Label] == o.ID && j.Labels[model.LabelRole] == role
}

// preparer runs the one-time prepare Job of a row (a home, a folder) and
// counts the Jobs it made on the row.
type preparer struct {
	client.Client
	Reader client.Reader
}

// run makes the Job when there is none (the last allowed one without a TTL, so
// it stays as the evidence), and reads the one there is. what names the owner
// in messages.
func (p preparer) run(ctx context.Context, owner jobOwner, what, role string, build func() *batchv1.Job) (prepareResult, error) {
	job := build()
	job.Labels[owner.Label] = owner.ID
	job.Spec.Template.Labels[owner.Label] = owner.ID
	key := client.ObjectKeyFromObject(job)
	existing := &batchv1.Job{}
	switch err := p.Reader.Get(ctx, key, existing); {
	case apierrors.IsNotFound(err):
		attempt := owner.Attempts + 1
		if attempt >= maxPrepareAttempts {
			job.Spec.TTLSecondsAfterFinished = nil
		}
		// The attempt is counted first, against the version the row was read
		// at: a conflict leaves no Job behind, and a crash in between counts
		// an attempt that never ran, which only makes the bound stricter.
		if err := owner.Count(ctx, attempt); err != nil {
			return prepareResult{}, err
		}
		if err := p.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return prepareResult{}, fmt.Errorf("create prepare job: %w", err)
		}
		return prepareResult{outcome: prepareRunning}, nil
	case err != nil:
		return prepareResult{}, err
	case !owner.ownsJob(existing, role):
		// Never used, and the owner names the Job so an admin can move it.
		return prepareResult{outcome: prepareForeign, evidence: []string{fmt.Sprintf("job %s exists and is not this %s's prepare job; not using it", key, what)}}, nil
	}
	if jobHas(existing, batchv1.JobComplete) {
		return prepareResult{outcome: prepareDone}, nil
	}
	var pods corev1.PodList
	if err := p.Reader.List(ctx, &pods, client.InNamespace(owner.Namespace), client.MatchingLabels{batchv1.JobNameLabel: existing.Name}); err != nil {
		return prepareResult{}, fmt.Errorf("list prepare pods: %w", err)
	}
	unmounted, err := p.unmounted(ctx, pods.Items)
	if err != nil {
		return prepareResult{}, err
	}
	switch {
	case jobHas(existing, batchv1.JobFailed):
		res := prepareResult{evidence: append(unmounted, jobEvidence(existing, pods.Items)...), unmounted: unmounted}
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				res.said = res.said || (cs.State.Terminated != nil && cs.State.Terminated.Message != "")
			}
		}
		if n := owner.Attempts; n >= maxPrepareAttempts {

			res.outcome = prepareGaveUp
			res.evidence = append(res.evidence, fmt.Sprintf("tried %d times, not trying again", n))
		} else {
			res.outcome = prepareRetry
			res.evidence = append(res.evidence, fmt.Sprintf("attempt %d of %d", n, maxPrepareAttempts))
		}
		return res, nil
	}
	return prepareResult{outcome: prepareRunning, evidence: unmounted, unmounted: unmounted}, nil
}

// unmounted is what the kubelet reported about the pods' volumes, newest
// event per reason and pod, when it kept failing past mountGrace on a pod
// whose containers never started.
func (p preparer) unmounted(ctx context.Context, pods []corev1.Pod) ([]string, error) {
	var out []string
	for _, pod := range pods {
		if containerStarted(&pod) {
			continue
		}
		var evs corev1.EventList
		if err := p.Reader.List(ctx, &evs, client.InNamespace(pod.Namespace), client.MatchingFields{"involvedObject.name": pod.Name}); err != nil {
			return nil, fmt.Errorf("list prepare pod events: %w", err)
		}
		for _, e := range evs.Items {
			if e.InvolvedObject.Kind != "Pod" || e.InvolvedObject.UID != pod.UID || e.Type != corev1.EventTypeWarning || !slices.Contains(mountReasons, e.Reason) {
				continue
			}
			if time.Since(firstSeen(&e)) >= mountGrace {
				out = append(out, fmt.Sprintf("%s %s: %s", e.Reason, pod.Name, strings.TrimSpace(e.Message)))
			}
		}
	}
	return out, nil
}

// containerStarted reports whether a container of the pod ran: its volumes
// then mounted, whatever failed before.
func containerStarted(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Status.ContainerStatuses, func(cs corev1.ContainerStatus) bool {
		return cs.State.Running != nil || cs.State.Terminated != nil || cs.LastTerminationState.Terminated != nil
	})
}

// firstSeen is when an event first happened.
func firstSeen(e *corev1.Event) time.Time {
	switch {
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}
