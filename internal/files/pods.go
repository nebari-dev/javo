//go:build !local

package files

// File pods: what serves a person's files where apps run on Kubernetes.
// They are made, read and reaped with the Kubernetes client, so a binary
// built with the tag local, which serves files from folders of the
// machine (Folders), leaves this file out.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/status"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

// PodsConfig configures NewPods.
type PodsConfig struct {
	// Client must read from the API server, not a cache: Ensure reads a pod
	// right after creating it and lists events by involvedObject.name.
	Client client.Client
	// Namespace is where homes are unless a call's context says otherwise
	// (WithNamespace).
	Namespace string
	// ReapIn lists the namespaces Reap sweeps; nil means only Namespace.
	ReapIn func(ctx context.Context) ([]string, error)
	// ReapAcrossNamespaces makes Reap list file pods cluster-wide in one call
	// and keep those in the ReapIn namespaces, for when there is a namespace
	// per person. It needs a cluster-wide grant on pods.
	ReapAcrossNamespaces bool
	// Image is the rclone image, pinned by digest.
	Image string
	// Seal seals each file pod's rclone password into an annotation of the
	// pod, so the gateway (which cannot read Secrets in workspace namespaces)
	// can read it back after a restart or from another replica; the
	// container gets it from a Secret, never from the pod spec. Required:
	// the sessions key, shared by all replicas.
	Seal tink.AEAD
	// UIDs gives the uid and gid the owner's apps run as; the file pod runs
	// as them too, so a file it writes belongs to the owner. Required;
	// ids.People is the implementation.
	UIDs UserIDs
	// Storage resolves what a file pod mounts: the owner's home (its current
	// claim), group folders and co-location, as for the owner's apps.
	// Required.
	Storage Storage
	// IdleAfter is how long a file pod lives after its user was last
	// active (Ensure or Touch); zero means 10 minutes.
	IdleAfter time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Pods creates, reports on and reaps file pods.
type Pods struct {
	cfg PodsConfig
	use activity
}

// NewPods returns a file pod manager. Call Run to delete idle pods.
func NewPods(cfg PodsConfig) (*Pods, error) {
	if cfg.Client == nil || cfg.Namespace == "" || cfg.Image == "" || cfg.Storage == nil || cfg.UIDs == nil || cfg.Seal == nil {
		return nil, errors.New("files: client, namespace, image, storage, uids and seal are required")
	}
	if cfg.IdleAfter == 0 {
		cfg.IdleAfter = defaultIdle
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Pods{cfg: cfg}, nil
}

// ns is the namespace of the home a call is about.
func (p *Pods) ns(ctx context.Context) string { return namespaceOr(ctx, p.cfg.Namespace) }

func podName(userSlug string) string { return model.FilesPodPrefix + userSlug }

// Ensure returns "<user>:<password>@<podIP>:5572", the pod's rclone login
// and address, when the user's file pod is ready. Otherwise it asks for the
// user's home if they have none (as starting an app does), creates the pod
// once the home is prepared, and returns ErrHomeStarting, ErrHomeBusy or
// ErrHomeConflict. The home claim (the one the user's home records, after a
// mode change the new one) must record userID as its owner, checked on every
// call because a colliding slug shares the pod name.
func (p *Pods) Ensure(ctx context.Context, userID string) (string, error) {
	return p.EnsureGroup(ctx, userID, "")
}

// EnsureGroup is Ensure for a pod that also mounts the group folder dir (""
// for none). A pod that exists without that mount, because the folder became
// ready or the user joined the group after it was created, is deleted and
// ErrHomeStarting returned so the next call creates it with the folder.
func (p *Pods) EnsureGroup(ctx context.Context, userID, dir string) (string, error) {
	p.Touch(userID)

	c, ns := p.cfg.Client, p.ns(ctx)
	in, err := homeInput(ctx, p.cfg.Storage, ns, userID)
	if err != nil {
		return "", err
	}
	var claim corev1.PersistentVolumeClaim
	err = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: in.Home.Status.ClaimName}, &claim)
	if apierrors.IsNotFound(err) {
		return "", ErrHomeStarting
	}
	if err != nil {
		return "", err
	}
	if claim.Annotations[model.AnnotationOwnerID] != userID {
		return "", ErrHomeConflict
	}
	if err := runAs(ctx, p.cfg.UIDs, userID, &in); err != nil {
		return "", err
	}
	uid := in.UID

	var pod corev1.Pod
	err = c.Get(ctx, client.ObjectKey{Namespace: ns, Name: podName(userID)}, &pod)
	if held(in.Home) {
		// A restore or a move waits for the owner's pods to go, the file pod
		// too: it writes to the home.
		if err == nil && isFilePodOf(&pod, userID) {
			if err := deleteExact(ctx, c, &pod); err != nil {
				return "", err
			}
		}
		return "", ErrHomeBusy
	}
	if apierrors.IsNotFound(err) {
		if !in.Home.Status.Prepared {
			// As for an app: no new pod mounts a home its one-time Job has
			// not readied yet.
			return "", ErrHomeStarting
		}
		return "", p.create(ctx, buildPod(userID, ns, p.cfg.Image, in))
	}
	if err != nil {
		return "", err
	}
	if !isFilePodOf(&pod, userID) {
		return "", ErrForeignPod
	}
	if pod.DeletionTimestamp != nil {
		return "", ErrHomeStarting
	}
	login := rcloneLogin(p.cfg.Seal, &pod)
	// A pod without a password (made by an earlier version) is replaced
	// too: its rclone answers anyone who reaches it.
	if login == nil || !writesAs(&pod, uid) || !hasFolders(&pod, in, dir) {
		if err := deleteExact(ctx, c, &pod); err != nil {
			return "", err
		}
		return "", ErrHomeStarting
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		if err := deleteExact(ctx, c, &pod); err != nil {
			return "", err
		}
		return "", ErrHomeStarting
	}

	var evs corev1.EventList
	if err := c.List(ctx, &evs, client.InNamespace(ns), client.MatchingFields{"involvedObject.name": pod.Name}); err != nil {
		return "", err
	}
	mine := evs.Items[:0]
	for _, e := range evs.Items {
		if e.InvolvedObject.UID == pod.UID {
			mine = append(mine, e)
		}
	}
	// Reuse the workspace status rules so the file pod and apps agree on
	// what counts as a busy home.
	phase, conds, _ := status.Derive(&model.App{}, status.Observed{Workload: kube.Observe(kube.Seen{Pod: &pod, Events: mine}), Now: p.cfg.Now()})
	if phase == "Running" && pod.Status.PodIP != "" {
		return login.String() + "@" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(rcPort)), nil
	}
	for _, cd := range conds {
		if cd.Status == metav1.ConditionFalse && cd.Reason == string(reasons.HomeBusy) {
			return "", ErrHomeBusy
		}
	}
	return "", ErrHomeStarting
}

// create makes pod with a new password: sealed in its annotation, and for
// the container in a Secret the pod owns (deleted with it). It returns
// ErrHomeStarting once both exist.
func (p *Pods) create(ctx context.Context, pod *corev1.Pod) error {
	pass := newPassword()
	secretName, err := sealLogin(p.cfg.Seal, pod, pass)
	if err != nil {
		return err
	}
	c := p.cfg.Client
	if err := c.Create(ctx, pod); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ErrHomeStarting
		}
		return err
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: secretName, Namespace: pod.Namespace,
			Labels: map[string]string{model.LabelRole: roleFiles},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID,
				BlockOwnerDeletion: ptr.To(false),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{passwordKey: []byte(pass)},
	}
	if err := c.Create(ctx, sec); err != nil {
		// A pod whose password never got a Secret cannot start; it goes.
		return errors.Join(fmt.Errorf("create the file pod's password: %w", err), deleteExact(ctx, c, pod))
	}
	return ErrHomeStarting
}

// sealLogin points pod's rclone at a new password Secret and records pass,
// sealed with seal and bound to the namespace, the pod and that Secret, in
// the pod's annotation. It returns the Secret's name.
func sealLogin(seal tink.AEAD, pod *corev1.Pod, pass string) (string, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails
	name := pod.Name + "-rc-" + hex.EncodeToString(b)
	sealed, err := seal.Encrypt([]byte(pass), loginContext(pod, name))
	if err != nil {
		return "", fmt.Errorf("seal the file pod's password: %w", err)
	}
	ctr := &pod.Spec.Containers[0]
	ctr.Env = slices.DeleteFunc(ctr.Env, func(e corev1.EnvVar) bool { return e.Name == envRCPass })
	ctr.Env = append(ctr.Env, corev1.EnvVar{Name: envRCPass, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: passwordKey},
	}})
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[annotationLogin] = base64.StdEncoding.EncodeToString(sealed)
	return name, nil
}

// loginContext binds a sealed password to where it belongs, so one copied
// onto another pod does not open.
func loginContext(pod *corev1.Pod, secretName string) []byte {
	return []byte(pod.Namespace + "/" + pod.Name + "/" + secretName)
}

// Touch records that the user is active, so their file pod (if any) is
// kept for another IdleAfter. It never creates a pod.
func (p *Pods) Touch(userID string) { p.use.touch(userID, p.cfg.Now()) }

// Reap deletes file pods unused for IdleAfter. A pod this gateway has not
// served (for example after a restart) counts as used when it was created.
func (p *Pods) Reap(ctx context.Context) error {
	namespaces := []string{p.cfg.Namespace}
	if p.cfg.ReapIn != nil {
		var err error
		if namespaces, err = p.cfg.ReapIn(ctx); err != nil {
			return err
		}
	}
	var errs []error
	var pods corev1.PodList
	onlyFiles := client.MatchingLabels{model.LabelRole: roleFiles}
	if p.cfg.ReapAcrossNamespaces && p.cfg.ReapIn != nil {
		var all corev1.PodList
		if err := p.cfg.Client.List(ctx, &all, onlyFiles); err != nil {
			return err
		}
		managed := make(map[string]bool, len(namespaces))
		for _, ns := range namespaces {
			managed[ns] = true
		}
		for _, pod := range all.Items {
			if managed[pod.Namespace] {
				pods.Items = append(pods.Items, pod)
			}
		}
	} else {
		// One namespace failing must not stop the others being swept.
		for _, ns := range namespaces {
			var part corev1.PodList
			if err := p.cfg.Client.List(ctx, &part, client.InNamespace(ns), onlyFiles); err != nil {
				errs = append(errs, fmt.Errorf("list file pods in %s: %w", ns, err))
				continue
			}
			pods.Items = append(pods.Items, part.Items...)
		}
	}
	idle := p.use.idle(p.cfg.Now(), p.cfg.IdleAfter, len(pods.Items), func(i int) (string, time.Time) {
		return pods.Items[i].Labels[model.LabelOwner], pods.Items[i].CreationTimestamp.Time
	})
	for _, i := range idle {
		if err := deleteExact(ctx, p.cfg.Client, &pods.Items[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deleteExact deletes pod only if it is still the same object (same UID), so
// a pod recreated under the same name in between is kept.
func deleteExact(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	err := c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	return err
}

// Run reaps idle file pods every minute until ctx is done.
func (p *Pods) Run(ctx context.Context) { reapLoop(ctx, "reap file pods", p.Reap) }

// hasFolders reports whether pod mounts exactly the group folders it should:
// dir (when asked for) and no folder outside in.Folders and dir, so a folder
// of a group the user has left is not kept mounted.
func hasFolders(pod *corev1.Pod, in podstorage.Input, dir string) bool {
	want := map[string]bool{}
	for _, f := range in.Folders {
		want[podstorage.GroupVolume(naming.GroupDir(f.Spec.Group))] = true
	}
	if dir != "" {
		want[podstorage.GroupVolume(dir)] = true
	}
	found := dir == ""
	for _, v := range pod.Spec.Volumes {
		if !podstorage.IsGroupVolume(v.Name) {
			continue
		}
		if !want[v.Name] {
			return false
		}
		found = found || v.Name == podstorage.GroupVolume(dir)
	}
	return found
}

// writesAs reports whether pod runs as uid with the home mounted writable,
// as buildPod makes it; a pod made by an older gateway or before the
// owner's uid changed does not.
func writesAs(pod *corev1.Pod, uid int64) bool {
	sc := pod.Spec.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != uid || len(pod.Spec.Containers) == 0 {
		return false
	}
	return slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool {
		return m.MountPath == dataDir && !m.ReadOnly
	})
}

// buildPod returns the file pod: the plan of fileSpec as a Pod. Its
// password comes from a Secret (create adds it), so whoever may read pods
// there does not see it.
func buildPod(userSlug, namespace, image string, in podstorage.Input) *corev1.Pod {
	s := fileSpec(podName(userSlug), userSlug, namespace, image, in)
	pod := kube.Render(s, podspec.Isolation{})
	// Kubernetes only: a file pod is found by its role, not by an app's id
	// (the admission policy allows these two labels and no other), and its
	// port keeps its name.
	pod.Labels = map[string]string{model.LabelRole: roleFiles, model.LabelOwner: userSlug}
	pod.Spec.Containers[0].Ports[0].Name = "rc"
	return pod
}

// rcloneLogin is the user and password the file pod's rclone asks for, or
// nil if it has none the gateway can open: an older pod with no password or
// with it in its spec, or one sealed with another key.
func rcloneLogin(seal tink.AEAD, pod *corev1.Pod) *url.Userinfo {
	var user, secretName string
	for _, ctr := range pod.Spec.Containers {
		for _, e := range ctr.Env {
			switch {
			case e.Name == envRCUser:
				user = e.Value
			case e.Name == envRCPass && e.Value == "" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil &&
				e.ValueFrom.SecretKeyRef.Key == passwordKey:
				secretName = e.ValueFrom.SecretKeyRef.Name
			}
		}
	}
	sealed, err := base64.StdEncoding.DecodeString(pod.Annotations[annotationLogin])
	if secretName == "" || err != nil || len(sealed) == 0 {
		return nil
	}
	pass, err := seal.Decrypt(sealed, loginContext(pod, secretName))
	if err != nil || len(pass) == 0 {
		return nil
	}
	return url.UserPassword(user, string(pass))
}

// Release deletes the user's file pod, if any, so the home claim is free
// for an app (a single-node home attaches to one node at a time).
func (p *Pods) Release(ctx context.Context, userID string) error {
	p.use.forget(userID)
	var pod corev1.Pod
	err := p.cfg.Client.Get(ctx, client.ObjectKey{Namespace: p.ns(ctx), Name: podName(userID)}, &pod)
	if err != nil || !isFilePodOf(&pod, userID) {
		// A same-named pod that is not this user's file pod is left alone.
		return client.IgnoreNotFound(err)
	}
	return deleteExact(ctx, p.cfg.Client, &pod)
}

// isFilePodOf reports whether pod carries the file pod labels for userSlug.
func isFilePodOf(pod *corev1.Pod, userSlug string) bool {
	return pod.Labels[model.LabelRole] == roleFiles && pod.Labels[model.LabelOwner] == userSlug
}
