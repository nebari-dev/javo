package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/controller/namespace"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/workload"
)

// Executor runs each app as a Pod, named and labelled after its Ref.
// The controller sets everything but PodLogs (SetupWithManager fills the
// first three); the gateway, which only asks for addresses and logs, sets
// PodLogs alone.
type Executor struct {
	// Client reads the cluster as the manager's cache shows it, and writes
	// to it.
	Client client.Client
	// Reader reads the cluster live: a Pod that Start just made, which the
	// cache may not show yet, and Secrets, which are never cached.
	Reader client.Reader
	// Informers are the manager's cache, which Watch listens to.
	Informers cache.Informers
	// PodLogs opens the logs Logs returns.
	PodLogs PodLogs

	// Isolation is applied to every Pod (config workloads).
	Isolation podspec.Isolation
	// Sizes tell whether the size a Pod was built at may wait for a bigger
	// machine, and Machines which machines there are: a Pod that fits none
	// of them fails as SizeTooBig instead of waiting.
	Sizes    sizes.Catalog
	Machines capacity.Source
	// HomeClaim names the claim that holds owner's home in namespace, ""
	// while there is none.
	HomeClaim func(ctx context.Context, namespace, owner string) (string, error)
	// ManagedOnly keeps to namespaces labelled javo.nebari.dev/managed=true:
	// any other has no Pod Security labels and no gateway-only
	// NetworkPolicy. Set it wherever the controller sees more than the one
	// workspace namespace.
	ManagedOnly bool

	// started are the Pods, and for runs the Jobs, Start made that the
	// cache has not shown yet.
	started sync.Map
}

// PodLogs reads a pod container's log (pods/log get).
type PodLogs interface {
	Stream(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

var _ executor.Executor = (*Executor)(nil)

// indexEventPod is the cache index Observe finds a Pod's events by. It is
// named after the API's own field selector, so a client that reads the API
// server directly needs no index.
const indexEventPod = "involvedObject.name"

func eventPod(o client.Object) []string {
	return []string{o.(*corev1.Event).InvolvedObject.Name}
}

// SetupWithManager makes e read and write through mgr, and has mgr's cache
// index events by the object they are about. Machines is required.
func (e *Executor) SetupWithManager(mgr ctrl.Manager) error {
	if e.Machines == nil {
		return errors.New("kube.Executor: Machines is required")
	}
	e.Client, e.Reader, e.Informers = mgr.GetClient(), mgr.GetAPIReader(), mgr.GetCache()
	return mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Event{}, indexEventPod, eventPod)
}

func key(ref workload.Ref) client.ObjectKey {
	return client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
}

// own reads the object called after ref into obj and reports whether it is
// there and carries the ref's id under label. One that carries another id
// (or none) is not the workload's, and is never touched.
func (e *Executor) own(ctx context.Context, label string, ref workload.Ref, obj client.Object) (bool, error) {
	if err := e.Client.Get(ctx, key(ref), obj); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return obj.GetLabels()[label] == ref.ID, nil
}

// drop deletes the object called after ref, read into obj, when it is the
// app's.
func (e *Executor) drop(ctx context.Context, ref workload.Ref, obj client.Object) error {
	if mine, err := e.own(ctx, model.LabelApp, ref, obj); err != nil || !mine {
		return err
	}
	return client.IgnoreNotFound(e.Client.Delete(ctx, obj))
}

// Start creates the Pod that runs s. The app's Service is Hold's.
func (e *Executor) Start(ctx context.Context, s workload.Spec) error {
	err := e.Client.Create(ctx, Render(s, e.Isolation))
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Made now, or by an earlier Start the cache lagged behind.
	e.started.Store(key(s.Ref), struct{}{})
	return nil
}

// Stop deletes the app's Pod. Its Service stays, for Release.
func (e *Executor) Stop(ctx context.Context, ref workload.Ref) error {
	e.started.Delete(key(ref))
	return e.drop(ctx, ref, &corev1.Pod{})
}

// pod returns the app's Pod, or nil. A Pod of that name that carries
// another app's id (or none) is an error.
func (e *Executor) pod(ctx context.Context, ref workload.Ref) (*corev1.Pod, error) {
	pod := &corev1.Pod{}
	err := e.Client.Get(ctx, key(ref), pod)
	if _, made := e.started.Load(key(ref)); made {
		if apierrors.IsNotFound(err) {
			err = e.Reader.Get(ctx, key(ref), pod)
		}
		if err == nil || apierrors.IsNotFound(err) {
			// The cache shows it now, or it is gone again.
			e.started.Delete(key(ref))
		}
	}
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case pod.Labels[model.LabelApp] != ref.ID:
		return nil, fmt.Errorf("pod %s/%s exists and does not belong to app %s", pod.Namespace, pod.Name, ref.ID)
	}
	return pod, nil
}

// Observe reads the app's state off its Pod, the claim of its owner's
// home, the events about the Pod and the cluster's machines.
func (e *Executor) Observe(ctx context.Context, ref workload.Ref) (*workload.State, error) {
	if e.ManagedOnly {
		if managed, err := namespace.IsManaged(ctx, e.Client, ref.Namespace); err != nil {
			return nil, err
		} else if !managed {
			// The namespace may gain the label later, or the cache may lag.
			return nil, fmt.Errorf("namespace %s %w", ref.Namespace, executor.ErrUnmanaged)
		}
	}
	pod, err := e.pod(ctx, ref)
	if err != nil || pod == nil {
		return nil, err
	}
	seen := Seen{Pod: pod}
	if seen.Claim, err = e.homeClaim(ctx, ref); err != nil {
		return nil, err
	}
	if seen.Events, err = e.podEvents(ctx, pod); err != nil {
		return nil, err
	}
	if e.Machines != nil {
		if seen.Machines, err = e.Machines.Cluster(ctx); err != nil {
			return nil, err
		}
	}
	// The size the Pod records is the one its app asks for: a Pod built at
	// another is replaced before anyone looks at its state. One that
	// records none runs at the default.
	size, _ := e.Sizes.Resolve(pod.Annotations[model.AnnotationSize])
	seen.SizeMayWait = size.AllowUnschedulable

	st := Observe(seen)
	st.Fingerprint = FingerprintOf(pod)
	st.Stale = readsLegacySecrets(pod)
	st.Running = pod.Status.Phase == corev1.PodRunning
	st.Commit = commitOf(pod)
	return st, nil
}

// homeClaim returns the claim of the home of ref's owner, or nil while it
// has none.
func (e *Executor) homeClaim(ctx context.Context, ref workload.Ref) (*corev1.PersistentVolumeClaim, error) {
	if e.HomeClaim == nil {
		return nil, nil
	}
	name, err := e.HomeClaim(ctx, ref.Namespace, ref.Owner)
	if err != nil || name == "" {
		return nil, err
	}
	claim := &corev1.PersistentVolumeClaim{}
	err = e.Client.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: name}, claim)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return claim, err
}

// podEvents returns the events about this exact Pod. Matching on UID keeps a
// deleted Pod's events from describing the Pod that replaced it.
func (e *Executor) podEvents(ctx context.Context, pod *corev1.Pod) ([]corev1.Event, error) {
	var list corev1.EventList
	if err := e.Client.List(ctx, &list, client.InNamespace(pod.Namespace), client.MatchingFields{indexEventPod: pod.Name}); err != nil {
		return nil, err
	}
	var out []corev1.Event
	for _, ev := range list.Items {
		if ev.InvolvedObject.Kind == "Pod" && ev.InvolvedObject.UID == pod.UID {
			out = append(out, ev)
		}
	}
	return out, nil
}

// commitOf is the commit the pod's clone checked out, once source-check has
// finished, or "".
func commitOf(pod *corev1.Pod) string {
	for _, cs := range pod.Status.InitContainerStatuses {
		if t := cs.State.Terminated; cs.Name == source.CheckContainer && t != nil && t.ExitCode == 0 {
			c, _ := source.CommitOf(t.Message)
			return c
		}
	}
	return ""
}

// refBy names the workload an object carries the id of under label; its ID
// is empty when the object carries none.
func refBy(o client.Object, label string) workload.Ref {
	l := o.GetLabels()
	return workload.Ref{Namespace: o.GetNamespace(), Name: o.GetName(), ID: l[label], Owner: l[model.LabelOwner]}
}

// List names every app that has a Pod or a Service, each once. With
// ManagedOnly, those in other namespaces are left out: nothing there is
// the executor's to stop.
func (e *Executor) List(ctx context.Context) ([]workload.Ref, error) {
	return e.list(ctx, model.LabelApp, &corev1.PodList{}, &corev1.ServiceList{})
}

// list names the workloads the objects of lists carry the id of under
// label, each once.
func (e *Executor) list(ctx context.Context, label string, lists ...client.ObjectList) ([]workload.Ref, error) {
	var out []workload.Ref
	seen := map[workload.Ref]bool{}
	managed := map[string]bool{}
	for _, l := range lists {
		if err := e.Client.List(ctx, l, client.HasLabels{label}); err != nil {
			return nil, err
		}
	}
	for _, l := range lists {
		items, err := meta.ExtractList(l)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			ref := refBy(item.(client.Object), label)
			if seen[ref] {
				continue
			}
			seen[ref] = true
			if e.ManagedOnly {
				ok, known := managed[ref.Namespace]
				if !known {
					if ok, err = namespace.IsManaged(ctx, e.Client, ref.Namespace); err != nil {
						return nil, err
					}
					managed[ref.Namespace] = ok
				}
				if !ok {
					continue
				}
			}
			out = append(out, ref)
		}
	}
	return out, nil
}

// labelled reports the workload an object that changed carries the id of
// under label, if it carries one.
func labelled(label string, changed func(workload.Ref)) func(any) {
	return func(o any) {
		if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
			o = t.Obj
		}
		if obj, ok := o.(client.Object); ok {
			if ref := refBy(obj, label); ref.ID != "" {
				changed(ref)
			}
		}
	}
}

// watched is a kind Watch listens to and what it does with each change.
type watched struct {
	kind   client.Object
	notify func(any)
}

// inform calls each kind's notify for every object of it that changes,
// until ctx ends.
func (e *Executor) inform(ctx context.Context, kinds ...watched) error {
	for _, w := range kinds {
		informer, err := e.Informers.GetInformer(ctx, w.kind)
		if err != nil {
			return err
		}
		reg, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc:    w.notify,
			UpdateFunc: func(old, new any) { w.notify(old); w.notify(new) },
			DeleteFunc: w.notify,
		})
		if err != nil {
			return err
		}
		defer func() { _ = informer.RemoveEventHandler(reg) }()
	}
	<-ctx.Done()
	return nil
}

// Watch reports the app of every Pod and Service that changes, and of
// every Pod an event is about.
func (e *Executor) Watch(ctx context.Context, changed func(workload.Ref)) error {
	app := labelled(model.LabelApp, changed)
	aboutPod := func(o any) {
		if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
			o = t.Obj
		}
		ev, ok := o.(*corev1.Event)
		if !ok || ev.InvolvedObject.Kind != "Pod" {
			return
		}
		pod := &corev1.Pod{}
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: ev.Namespace, Name: ev.InvolvedObject.Name}, pod); err == nil {
			app(pod)
		}
	}
	return e.inform(ctx, watched{&corev1.Pod{}, app}, watched{&corev1.Service{}, app}, watched{&corev1.Event{}, aboutPod})
}

// Addr is the app's Service by its name in the cluster.
func (e *Executor) Addr(_ context.Context, ref workload.Ref, port int32) (string, error) {
	return net.JoinHostPort(fmt.Sprintf("%s.%s.svc", ref.Name, ref.Namespace), strconv.Itoa(int(port))), nil
}

// Logs opens the log of the container o.Unit of the app's Pod; with no
// unit named, of the Pod's only container.
func (e *Executor) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	if e.PodLogs == nil {
		return nil, errors.New("no pod log reader configured")
	}
	return e.PodLogs.Stream(ctx, ref.Namespace, ref.Name, logOptions(o))
}

// logOptions is o as the cluster takes it.
func logOptions(o executor.LogOptions) *corev1.PodLogOptions {
	opts := &corev1.PodLogOptions{Container: o.Unit, Follow: o.Follow, Previous: o.Previous, Timestamps: o.Timestamps}
	if o.Tail > 0 {
		opts.TailLines = &o.Tail
	}
	if o.MaxBytes > 0 {
		opts.LimitBytes = &o.MaxBytes
	}
	if !o.Since.IsZero() {
		opts.SinceTime = &metav1.Time{Time: o.Since}
	}
	return opts
}
