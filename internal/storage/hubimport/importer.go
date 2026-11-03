package hubimport

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
)

const (
	oldMount = "/old"
	newMount = "/new"
	// jobTTL keeps a finished Job (and its log) for a day; the import
	// deletes its own Jobs once the step after them is done.
	jobTTL = 86400
)

// Source is one JupyterHub home claim.
type Source struct{ Namespace, Claim string }

// String is "<namespace>/<claim>", the value of the imported-from annotation.
func (s Source) String() string { return s.Namespace + "/" + s.Claim }

// Target is the person the home goes to and their Javo home claim, which
// must exist (the controller makes and readies it).
type Target struct {
	// Owner is the person's name (alice-1a2b3c4d).
	Owner string
	// UID and GID are what the person's files and pods use.
	UID, GID  int64
	Namespace string
	HomeClaim string
}

// Phase is where one home's import stands.
type Phase string

const (
	// PhaseStaging: the stage Job copies the JupyterHub home to the staging claim.
	PhaseStaging Phase = "staging"
	// PhaseMoving: the staging volume is moving to Javo's namespace.
	PhaseMoving Phase = "moving"
	// PhaseCopying: the copy Job copies the staged files into the home.
	PhaseCopying Phase = "copying"
	// PhaseDone: the home holds the files and the staging volume is gone.
	PhaseDone Phase = "done"
	// PhaseFailed: a Job failed; Detail says why. Retry starts it again.
	PhaseFailed Phase = "failed"
	// PhaseBlocked: something in use must stop first; Detail says what.
	PhaseBlocked Phase = "blocked"
)

// Progress is the outcome of a Step.
type Progress struct {
	Phase Phase
	// Job is the Job at work while staging or copying, to follow its log.
	Job    *types.NamespacedName
	Detail []string
}

// Importer moves JupyterHub homes into Javo homes. Client must read without
// a cache (claims, volumes and Jobs change under it).
type Importer struct {
	Client client.Client
	// Image is the javo image the Jobs run.
	Image podspec.Image
	// Into is the folder of the home the files go to, relative to the home;
	// "." is the home itself.
	Into string
}

// stagingClaimName is the staging claim's name, in both namespaces.
func stagingClaimName(owner string) string { return naming.NamespaceName("javo-import", owner) }

func stageJobName(owner string) string { return naming.NamespaceName("javo-import-stage", owner) }

func copyJobName(owner string) string { return naming.NamespaceName("javo-import-copy", owner) }

func labels(owner string) map[string]string {
	return map[string]string{model.LabelRole: model.RoleHomeImport, model.LabelOwner: owner}
}

// Step looks at where src's import into t stands and takes the next step.
// It returns at once; call it again until the phase is done, failed or
// blocked.
func (im Importer) Step(ctx context.Context, src Source, t Target) (Progress, error) {
	home := &corev1.PersistentVolumeClaim{}
	if err := im.Client.Get(ctx, client.ObjectKey{Namespace: t.Namespace, Name: t.HomeClaim}, home); err != nil {
		return Progress{}, fmt.Errorf("read home claim %s: %w", t.HomeClaim, err)
	}
	if home.Annotations[model.AnnotationImportedFrom] == src.String() {
		return Progress{Phase: PhaseDone}, im.cleanUp(ctx, src, t)
	}
	moved, err := im.claim(ctx, t.Namespace, stagingClaimName(t.Owner))
	if err != nil {
		return Progress{}, err
	}
	if moved != nil {
		return im.copy(ctx, src, t, moved, home)
	}
	staging, err := im.claim(ctx, src.Namespace, stagingClaimName(t.Owner))
	if err != nil {
		return Progress{}, err
	}
	switch {
	case staging == nil:
		pv, err := im.volume(ctx, src, t)
		if err != nil || pv != nil {
			return Progress{Phase: PhaseMoving}, im.rebind(ctx, pv, t)
		}
		if p, err := im.sourceInUse(ctx, src); p != nil || err != nil {
			return *p, err
		}
		return im.stage(ctx, src, t)
	case !staging.DeletionTimestamp.IsZero():
		return Progress{Phase: PhaseMoving}, nil // the claim goes once no pod holds it
	}
	key := types.NamespacedName{Namespace: src.Namespace, Name: stageJobName(t.Owner)}
	switch state, detail, err := im.jobState(ctx, key); {
	case err != nil:
		return Progress{}, err
	case state == jobMissing:
		if p, err := im.sourceInUse(ctx, src); p != nil || err != nil {
			return *p, err
		}
		return im.stage(ctx, src, t)
	case state == jobFailed:
		return Progress{Phase: PhaseFailed, Job: &key, Detail: detail}, nil
	case state == jobRunning:
		return Progress{Phase: PhaseStaging, Job: &key}, nil
	}
	return Progress{Phase: PhaseMoving}, im.release(ctx, src, t, staging, key)
}

// claim reads a claim; nil when there is none.
func (im Importer) claim(ctx context.Context, ns, name string) (*corev1.PersistentVolumeClaim, error) {
	c := &corev1.PersistentVolumeClaim{}
	err := im.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, c)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return c, err
}

// volume is the staging volume of src's import for t, once its claim is
// gone: the volume labelled and annotated as this import's.
func (im Importer) volume(ctx context.Context, src Source, t Target) (*corev1.PersistentVolume, error) {
	var l corev1.PersistentVolumeList
	if err := im.Client.List(ctx, &l, client.MatchingLabels(labels(t.Owner))); err != nil {
		return nil, fmt.Errorf("list staging volumes: %w", err)
	}
	for i := range l.Items {
		if l.Items[i].Annotations[model.AnnotationImportedFrom] == src.String() {
			return &l.Items[i], nil
		}
	}
	return nil, nil
}

// release keeps the staged volume and lets go of its claim in JupyterHub's
// namespace. The volume is labelled first, so it is found again whatever
// happens next.
func (im Importer) release(ctx context.Context, src Source, t Target, staging *corev1.PersistentVolumeClaim, stageJob types.NamespacedName) error {
	if staging.Status.Phase != corev1.ClaimBound || staging.Spec.VolumeName == "" {
		return fmt.Errorf("staging claim %s/%s is %s, not bound, after the copy", staging.Namespace, staging.Name, staging.Status.Phase)
	}
	pv := &corev1.PersistentVolume{}
	if err := im.Client.Get(ctx, client.ObjectKey{Name: staging.Spec.VolumeName}, pv); err != nil {
		return fmt.Errorf("read staging volume: %w", err)
	}
	base := pv.DeepCopy()
	pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain
	for k, v := range labels(t.Owner) {
		metav1.SetMetaDataLabel(&pv.ObjectMeta, k, v)
	}
	metav1.SetMetaDataAnnotation(&pv.ObjectMeta, model.AnnotationImportedFrom, src.String())
	if err := im.Client.Patch(ctx, pv, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("keep staging volume %s: %w", pv.Name, err)
	}
	if err := im.deleteJob(ctx, stageJob); err != nil {
		return err
	}
	if err := im.Client.Delete(ctx, staging); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete staging claim: %w", err)
	}
	return nil
}

// rebind binds the kept volume to a claim of the same name in Javo's
// namespace: the volume is reserved for it (claimRef without a uid), then
// the claim names the volume.
func (im Importer) rebind(ctx context.Context, pv *corev1.PersistentVolume, t Target) error {
	name := stagingClaimName(t.Owner)
	if r := pv.Spec.ClaimRef; r == nil || r.Namespace != t.Namespace || r.Name != name {
		base := pv.DeepCopy()
		pv.Spec.ClaimRef = &corev1.ObjectReference{Kind: "PersistentVolumeClaim", APIVersion: "v1", Namespace: t.Namespace, Name: name}
		if err := im.Client.Patch(ctx, pv, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("reserve staging volume %s for %s/%s: %w", pv.Name, t.Namespace, name, err)
		}
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: t.Namespace, Labels: labels(t.Owner),
			Annotations: map[string]string{model.AnnotationImportedFrom: pv.Annotations[model.AnnotationImportedFrom]}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      pv.Spec.AccessModes,
			StorageClassName: ptr.To(pv.Spec.StorageClassName),
			VolumeName:       pv.Name,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: pv.Spec.Capacity[corev1.ResourceStorage]}},
		},
	}
	if err := im.Client.Create(ctx, claim); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s/%s: %w", t.Namespace, name, err)
	}
	return nil
}

// copy runs the Job that copies the staged files into the home, as the
// person, and finishes when it is done.
func (im Importer) copy(ctx context.Context, src Source, t Target, moved, home *corev1.PersistentVolumeClaim) (Progress, error) {
	if moved.Status.Phase != corev1.ClaimBound {
		return Progress{Phase: PhaseMoving}, nil
	}
	key := types.NamespacedName{Namespace: t.Namespace, Name: copyJobName(t.Owner)}
	switch state, detail, err := im.jobState(ctx, key); {
	case err != nil:
		return Progress{}, err
	case state == jobMissing:
		if p, err := im.homeInUse(ctx, t); p != nil || err != nil {
			return *p, err
		}
		if err := im.Client.Create(ctx, im.copyJob(key, moved.Name, t)); err != nil && !apierrors.IsAlreadyExists(err) {
			return Progress{}, fmt.Errorf("create copy Job: %w", err)
		}
		return Progress{Phase: PhaseCopying, Job: &key}, nil
	case state == jobFailed:
		return Progress{Phase: PhaseFailed, Job: &key, Detail: detail}, nil
	case state == jobRunning:
		return Progress{Phase: PhaseCopying, Job: &key}, nil
	}
	// Marked first: the mark is what says the import is done.
	base := home.DeepCopy()
	metav1.SetMetaDataAnnotation(&home.ObjectMeta, model.AnnotationImportedFrom, src.String())
	if err := im.Client.Patch(ctx, home, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return Progress{}, fmt.Errorf("mark home claim %s: %w", home.Name, err)
	}
	return Progress{Phase: PhaseDone}, im.cleanUp(ctx, src, t)
}

// cleanUp removes what a finished import left: its Jobs, and the staging
// volume with its claim.
func (im Importer) cleanUp(ctx context.Context, src Source, t Target) error {
	for _, key := range []types.NamespacedName{
		{Namespace: src.Namespace, Name: stageJobName(t.Owner)}, {Namespace: t.Namespace, Name: copyJobName(t.Owner)},
	} {
		if err := im.deleteJob(ctx, key); err != nil {
			return err
		}
	}
	moved, err := im.claim(ctx, t.Namespace, stagingClaimName(t.Owner))
	if err != nil || moved == nil {
		return err
	}
	if moved.Spec.VolumeName != "" {
		pv := &corev1.PersistentVolume{}
		switch err := im.Client.Get(ctx, client.ObjectKey{Name: moved.Spec.VolumeName}, pv); {
		case err == nil && pv.Labels[model.LabelRole] == model.RoleHomeImport:
			base := pv.DeepCopy()
			pv.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimDelete
			if err := im.Client.Patch(ctx, pv, client.MergeFrom(base)); err != nil {
				return fmt.Errorf("let staging volume %s go: %w", pv.Name, err)
			}
		case client.IgnoreNotFound(err) != nil:
			return err
		}
	}
	if err := im.Client.Delete(ctx, moved); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete staging claim: %w", err)
	}
	return nil
}

// Retry deletes the failed Jobs of src's import for t, so the next Step
// runs them again. A copy that failed half way carries on: files already
// copied are skipped.
func (im Importer) Retry(ctx context.Context, src Source, t Target) error {
	for _, key := range []types.NamespacedName{
		{Namespace: src.Namespace, Name: stageJobName(t.Owner)}, {Namespace: t.Namespace, Name: copyJobName(t.Owner)},
	} {
		if state, _, err := im.jobState(ctx, key); err != nil {
			return err
		} else if state == jobFailed {
			if err := im.deleteJob(ctx, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (im Importer) deleteJob(ctx context.Context, key types.NamespacedName) error {
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	if err := im.Client.Delete(ctx, j, client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("delete Job %s: %w", key, err)
	}
	return nil
}

type jobPhase int

const (
	jobMissing jobPhase = iota
	jobRunning
	jobSucceeded
	jobFailed
)

// jobState says how the Job stands; for a failed one, why (its pod's
// termination message, which the copy writes).
func (im Importer) jobState(ctx context.Context, key types.NamespacedName) (jobPhase, []string, error) {
	j := &batchv1.Job{}
	switch err := im.Client.Get(ctx, key, j); {
	case apierrors.IsNotFound(err):
		return jobMissing, nil, nil
	case err != nil:
		return 0, nil, err
	case !j.DeletionTimestamp.IsZero():
		return jobRunning, nil, nil // going; the next step makes it again
	}
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return jobSucceeded, nil, nil
		case batchv1.JobFailed:
			detail := []string{c.Reason + ": " + c.Message}
			var pods corev1.PodList
			if err := im.Client.List(ctx, &pods, client.InNamespace(key.Namespace), client.MatchingLabels{batchv1.JobNameLabel: key.Name}); err != nil {
				return 0, nil, err
			}
			for _, p := range pods.Items {
				for _, cs := range p.Status.ContainerStatuses {
					if term := cs.State.Terminated; term != nil && term.Message != "" {
						detail = append(detail, strings.Split(strings.TrimSpace(term.Message), "\n")...)
					}
				}
			}
			return jobFailed, detail, nil
		}
	}
	return jobRunning, nil, nil
}

// sourceInUse blocks while a pod (the person's JupyterHub server) uses the
// JupyterHub claim: its files are changing, and a disk that attaches to one
// node cannot be read elsewhere.
func (im Importer) sourceInUse(ctx context.Context, src Source) (*Progress, error) {
	var pods corev1.PodList
	if err := im.Client.List(ctx, &pods, client.InNamespace(src.Namespace)); err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", src.Namespace, err)
	}
	for _, p := range pods.Items {
		if finished(&p) {
			continue
		}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == src.Claim {
				return &Progress{Phase: PhaseBlocked, Detail: []string{fmt.Sprintf("pod %s uses %s: stop the JupyterHub server first", p.Name, src.Claim)}}, nil
			}
		}
	}
	return nil, nil
}

// homeInUse blocks while any pod of the person runs in Javo's namespace:
// their home is in use.
func (im Importer) homeInUse(ctx context.Context, t Target) (*Progress, error) {
	var pods corev1.PodList
	if err := im.Client.List(ctx, &pods, client.InNamespace(t.Namespace), client.MatchingLabels{model.LabelOwner: t.Owner}); err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", t.Namespace, err)
	}
	for _, p := range pods.Items {
		if !finished(&p) && p.Labels[model.LabelRole] != model.RoleHomeImport {
			return &Progress{Phase: PhaseBlocked, Detail: []string{fmt.Sprintf("pod %s of %s is running: stop their apps first", p.Name, t.Owner)}}, nil
		}
	}
	return nil, nil
}

func finished(p *corev1.Pod) bool {
	return p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
}

// stage makes the staging claim and the Job that fills it.
func (im Importer) stage(ctx context.Context, src Source, t Target) (Progress, error) {
	source := &corev1.PersistentVolumeClaim{}
	if err := im.Client.Get(ctx, client.ObjectKey{Namespace: src.Namespace, Name: src.Claim}, source); err != nil {
		return Progress{}, fmt.Errorf("read %s: %w", src, err)
	}
	staging := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: stagingClaimName(t.Owner), Namespace: src.Namespace, Labels: labels(t.Owner),
			Annotations: map[string]string{model.AnnotationImportedFrom: src.String()}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: source.Spec.StorageClassName,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: sizeOf(source)}},
		},
	}
	if err := im.Client.Create(ctx, staging); err != nil && !apierrors.IsAlreadyExists(err) {
		return Progress{}, fmt.Errorf("create staging claim: %w", err)
	}
	key := types.NamespacedName{Namespace: src.Namespace, Name: stageJobName(t.Owner)}
	if err := im.Client.Create(ctx, im.stageJob(key, src.Claim, staging.Name, t)); err != nil && !apierrors.IsAlreadyExists(err) {
		return Progress{}, fmt.Errorf("create stage Job: %w", err)
	}
	return Progress{Phase: PhaseStaging, Job: &key}, nil
}

// sizeOf is the claim's size: what it holds, else what it asks for.
func sizeOf(c *corev1.PersistentVolumeClaim) resource.Quantity {
	if q, ok := c.Status.Capacity[corev1.ResourceStorage]; ok {
		return q
	}
	return c.Spec.Resources.Requests[corev1.ResourceStorage]
}

func claimVolume(name, claim string, readOnly bool) corev1.Volume {
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim, ReadOnly: readOnly}}}
}

// job is a one-try Job of the javo image running command with the
// JupyterHub (or staged) home at /old, read-only, and the target at /new.
func (im Importer) job(key types.NamespacedName, owner string, pod *corev1.PodSecurityContext, sc *corev1.SecurityContext, command []string, old, new corev1.Volume) *batchv1.Job {
	l := labels(owner)
	j := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: l},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			TTLSecondsAfterFinished: ptr.To(int32(jobTTL)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: l},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext:              pod,
					Containers: []corev1.Container{{
						Name: "copy", Image: im.Image.Ref, Command: command,
						VolumeMounts: []corev1.VolumeMount{
							{Name: old.Name, MountPath: oldMount, ReadOnly: true},
							{Name: new.Name, MountPath: newMount},
						},
						TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						SecurityContext:          sc,
					}},
					Volumes: []corev1.Volume{old, new},
				},
			},
		},
	}
	im.Image.Pull(&j.Spec.Template.Spec)
	return j
}

// stageJob copies the JupyterHub home to the staging claim as root: only
// root can read every file of uid 1000 and give it to the person. It keeps
// only the capabilities that takes, all within Pod Security "baseline".
func (im Importer) stageJob(key types.NamespacedName, source, staging string, t Target) *batchv1.Job {
	cmd := []string{"/" + model.ProductName, "storage", "import-copy", "--old", oldMount, "--new", newMount,
		"--uid", strconv.FormatInt(t.UID, 10), "--gid", strconv.FormatInt(t.GID, 10)}
	return im.job(key, t.Owner,
		&corev1.PodSecurityContext{RunAsUser: ptr.To(int64(0)), RunAsGroup: ptr.To(int64(0)), RunAsNonRoot: ptr.To(false),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		&corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"},
				Add: []corev1.Capability{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID"}},
		},
		cmd, claimVolume("old", source, true), claimVolume("new", staging, false))
}

// copyJob copies the staged files (already the person's) into the home as
// the person, the way a home mode change does (`javo storage copy`), so it
// runs under Pod Security "restricted".
func (im Importer) copyJob(key types.NamespacedName, staged string, t Target) *batchv1.Job {
	cmd := []string{"/" + model.ProductName, "storage", "copy", "--old", oldMount, "--new", path.Join(newMount, im.Into)}
	return im.job(key, t.Owner,
		&corev1.PodSecurityContext{RunAsUser: ptr.To(t.UID), RunAsGroup: ptr.To(t.GID), RunAsNonRoot: ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		&corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		cmd, claimVolume("old", staged, true), claimVolume("new", t.HomeClaim, false))
}
