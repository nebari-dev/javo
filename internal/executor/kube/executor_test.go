package kube

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/workload"
)

var ctx = context.Background()

// executorOn is an Executor on a cluster that holds objs.
func executorOn(objs ...client.Object) (*Executor, client.Client) {
	c := fake.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(&corev1.Pod{}).
		WithIndex(&corev1.Event{}, indexEventPod, eventPod).Build()
	return &Executor{Client: c, Reader: c}, c
}

func podOf(t *testing.T, c client.Client, ref workload.Ref) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

func gone(c client.Client, obj client.Object, ref workload.Ref) bool {
	return apierrors.IsNotFound(c.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, obj))
}

func TestAWorkloadStartsOnceIsSeenAndStops(t *testing.T) {
	e, c := executorOn()
	if st, err := e.Observe(ctx, sales); st != nil || err != nil {
		t.Fatalf("before Start: %+v, %v", st, err)
	}
	spec := someSpec()
	spec.Fingerprint = workload.Fingerprint{Size: "small", Run: "r1", APIAccess: model.APIAccessNone}
	if err := e.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	first := podOf(t, c, sales)
	if first.Labels[model.LabelApp] != "app-1" || first.Spec.Containers[0].Image != "example.com/app:1" {
		t.Fatalf("pod = %+v", first)
	}

	st, err := e.Observe(ctx, sales)
	if err != nil || st == nil {
		t.Fatalf("Observe = %+v, %v", st, err)
	}
	if st.Phase != "Starting" || st.Step != model.ConditionScheduled {
		t.Errorf("a new pod is %s at %s", st.Phase, st.Step)
	}
	if st.Fingerprint.Size != "small" || st.Fingerprint.Run != "r1" || st.Stale || st.Running {
		t.Errorf("state = %+v", st)
	}

	// Started again, even from another spec, it is left as it is.
	spec.Main.Image = "example.com/app:2"
	if err := e.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if again := podOf(t, c, sales); again.UID != first.UID || again.Spec.Containers[0].Image != "example.com/app:1" {
		t.Errorf("the second Start changed the pod: %s", again.Spec.Containers[0].Image)
	}

	for range 2 {
		if err := e.Stop(ctx, sales); err != nil {
			t.Fatal(err)
		}
	}
	if !gone(c, &corev1.Pod{}, sales) {
		t.Error("the pod survived Stop")
	}
	if st, err := e.Observe(ctx, sales); st != nil || err != nil {
		t.Fatalf("after Stop: %+v, %v", st, err)
	}
}

// A Pod of the same name that carries another app's id is not the
// workload's: it is not reported as it and never deleted.
func TestAnotherAppsPodIsNeverTheWorkloads(t *testing.T) {
	theirs := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sales.Namespace, Name: sales.Name, Labels: map[string]string{model.LabelApp: "app-2"}}}
	e, c := executorOn(theirs)
	if _, err := e.Observe(ctx, sales); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Errorf("Observe = %v", err)
	}
	if err := e.Stop(ctx, sales); err != nil {
		t.Fatal(err)
	}
	if gone(c, &corev1.Pod{}, sales) {
		t.Error("Stop deleted another app's pod")
	}
}

// staleCache is a client that does not show Pods yet, as an informer cache
// right after a create.
type staleCache struct{ client.Client }

func (s staleCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Pod); ok {
		return apierrors.NewNotFound(corev1.Resource("pods"), key.Name)
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

// The cache lags behind a create: a workload Start just made is seen
// anyway, and one nobody started is not looked for past the cache.
func TestAStartedWorkloadIsSeenBeforeTheCacheShowsIt(t *testing.T) {
	e, c := executorOn()
	e.Client = staleCache{c}
	if err := e.Start(ctx, someSpec()); err != nil {
		t.Fatal(err)
	}
	if st, err := e.Observe(ctx, sales); st == nil || err != nil {
		t.Fatalf("Observe after Start = %+v, %v", st, err)
	}

	other := sales
	other.Name, other.ID = "alice-other", "app-9"
	spec := someSpec()
	spec.Ref = other
	if err := (&Executor{Client: c, Reader: c}).Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if st, err := e.Observe(ctx, other); st != nil || err != nil {
		t.Fatalf("Observe of a pod the cache does not show = %+v, %v", st, err)
	}
}

// started is the app's pod with a size recorded, waiting for a machine.
func startedPod(t *testing.T, e *Executor, c client.Client, size string) *corev1.Pod {
	t.Helper()
	spec := someSpec()
	spec.Fingerprint.Size = size
	spec.Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("64")}
	if err := e.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	return podOf(t, c, sales)
}

type machines capacity.Cluster

func (m machines) Cluster(context.Context) (capacity.Cluster, error) { return capacity.Cluster(m), nil }

// A pod no machine can hold fails at once, unless the size it was built at
// may wait for a bigger machine.
func TestAPodTooBigForEveryMachineFailsUnlessItsSizeMayWait(t *testing.T) {
	catalog, err := sizes.Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: huge, displayName: Huge, cpu: "64", memory: 2Gi, allowUnschedulable: true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	small := machines{{CPU: resource.MustParse("4"), Memory: resource.MustParse("8Gi")}}
	for size, want := range map[string]reasons.Code{"small": reasons.SizeTooBig, "": reasons.SizeTooBig, "custom:64000m,1": reasons.SizeTooBig, "huge": reasons.Starting} {
		e, c := executorOn()
		e.Sizes, e.Machines = catalog, small
		startedPod(t, e, c, size)
		st, err := e.Observe(ctx, sales)
		if err != nil {
			t.Fatal(err)
		}
		if st.Code != want {
			t.Errorf("size %q: %s (%s), want %s", size, st.Code, st.Phase, want)
		}
	}
}

// Events are matched to the pod by UID: one about a pod of the same name
// that was replaced says nothing about this one.
func TestOnlyThisPodsEventsCount(t *testing.T) {
	e, c := executorOn()
	pod := startedPod(t, e, c, "")
	event := func(name string, uid apitypes.UID) *corev1.Event {
		return &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: sales.Namespace, Name: name},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: sales.Name, Namespace: sales.Namespace, UID: uid},
			Reason:         "FailedAttachVolume", Message: "Multi-Attach error for volume",
		}
	}
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if err := c.Create(ctx, event("old", "replaced-pod")); err != nil {
		t.Fatal(err)
	}
	if st, err := e.Observe(ctx, sales); err != nil || st.Code == reasons.HomeBusy {
		t.Fatalf("an event of a replaced pod counted: %+v, %v", st, err)
	}
	if err := c.Create(ctx, event("new", pod.UID)); err != nil {
		t.Fatal(err)
	}
	if st, err := e.Observe(ctx, sales); err != nil || st.Code != reasons.HomeBusy {
		t.Fatalf("the pod's own event did not count: %+v, %v", st, err)
	}
}

// What the reconciler reads off the pod besides its phase: that it runs,
// the commit its clone reported, and the claim of the owner's home.
func TestObserveReportsRunningCommitAndTheHomeClaim(t *testing.T) {
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: sales.Namespace, Name: "home-alice-2"},
		Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending}}
	e, c := executorOn(claim)
	e.HomeClaim = func(_ context.Context, ns, owner string) (string, error) {
		if ns != sales.Namespace || owner != "alice" {
			t.Errorf("home asked for %s/%s", ns, owner)
		}
		return "home-alice-2", nil
	}
	pod := startedPod(t, e, c, "")
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "source-check", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: "commit=0123456789abcdef0123456789abcdef01234567"}}}}
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	st, err := e.Observe(ctx, sales)
	if err != nil {
		t.Fatal(err)
	}
	if st.Code != reasons.HomePending || st.Running || st.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("state = %+v", st)
	}

	pod.Status.Phase = corev1.PodRunning
	if err := c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	if st, err := e.Observe(ctx, sales); err != nil || !st.Running {
		t.Errorf("a running pod: %+v, %v", st, err)
	}
}

// A pod built when an app's Secrets were called "<name>-javo-<use>" must be
// built again; one that reads them under today's names is current.
func TestAPodReadingSecretsUnderTheirOldNamesIsStale(t *testing.T) {
	e, c := executorOn()
	if err := e.Start(ctx, someSpec()); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.Observe(ctx, sales); st == nil || st.Stale {
		t.Fatalf("a pod reading today's names: %+v", st)
	}
	for _, old := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Spec.Volumes[0].Secret.SecretName = "alice-sales-javo-nebi" },
		func(p *corev1.Pod) { p.Spec.Containers[0].Env[0].ValueFrom.SecretKeyRef.Name = "alice-sales-javo-api" },
		func(p *corev1.Pod) {
			p.Spec.InitContainers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: "alice-parent-javo-git"}}}}
		},
	} {
		pod := podOf(t, c, sales)
		old(pod)
		if err := c.Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
		if st, _ := e.Observe(ctx, sales); st == nil || !st.Stale {
			t.Errorf("a pod reading %v is not stale", pod.Spec)
		}
		if err := e.Stop(ctx, sales); err != nil {
			t.Fatal(err)
		}
		if err := e.Start(ctx, someSpec()); err != nil {
			t.Fatal(err)
		}
	}
}

func namespaced(name string, managed bool) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if managed {
		ns.Labels = map[string]string{model.LabelManaged: "true"}
	}
	return ns
}

// Asked to keep to managed namespaces, the executor reports a workload in
// any other as not its to run, and lists none there.
func TestOnlyManagedNamespacesAreServedWhenAsked(t *testing.T) {
	labels := map[string]string{model.LabelApp: "app-1", model.LabelOwner: "alice"}
	pod := func(ns, name string, l map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: l},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.com/app:1"}}}}
	}
	e, _ := executorOn(namespaced("javo", true), namespaced("other", false),
		pod("javo", "alice-sales", labels),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-sales", Labels: labels}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "bob-lab", Labels: map[string]string{model.LabelApp: "app-2", model.LabelOwner: "bob"}}},
		pod("javo", "run-1", map[string]string{model.LabelJob: "job-1"}),
		pod("other", "alice-sales", labels),
	)
	all, err := e.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []workload.Ref{sales, {Namespace: "other", Name: "alice-sales", ID: "app-1", Owner: "alice"}, {Namespace: "javo", Name: "bob-lab", ID: "app-2", Owner: "bob"}}
	if !sameRefs(all, want) {
		t.Errorf("List = %v, want %v", all, want)
	}

	e.ManagedOnly = true
	managed, err := e.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []workload.Ref{sales, {Namespace: "javo", Name: "bob-lab", ID: "app-2", Owner: "bob"}}; !sameRefs(managed, want) {
		t.Errorf("List of managed namespaces = %v, want %v", managed, want)
	}
	elsewhere := sales
	elsewhere.Namespace = "other"
	if _, err := e.Observe(ctx, elsewhere); !errors.Is(err, executor.ErrUnmanaged) {
		t.Errorf("Observe in an unmanaged namespace = %v", err)
	}
	if st, err := e.Observe(ctx, sales); st == nil || err != nil {
		t.Errorf("Observe in a managed namespace = %+v, %v", st, err)
	}
}

func sameRefs(got, want []workload.Ref) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[workload.Ref]bool{}
	for _, r := range got {
		seen[r] = true
	}
	for _, r := range want {
		if !seen[r] {
			return false
		}
	}
	return true
}

// The Service outlives the app's pods: Hold makes it and keeps its port
// current, Stop leaves it, and Release deletes it unless it is another
// app's.
func TestTheServiceIsHeldAcrossPodsAndReleasedWithTheApp(t *testing.T) {
	e, c := executorOn()
	svc := func() *corev1.Service {
		s := &corev1.Service{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: sales.Namespace, Name: sales.Name}, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, port := range []int32{8080, 9090} {
		if err := e.Hold(ctx, sales, port); err != nil {
			t.Fatal(err)
		}
		s := svc()
		if len(s.Spec.Ports) != 1 || s.Spec.Ports[0].Port != port || s.Spec.Ports[0].TargetPort.IntVal != port || s.Spec.Ports[0].Protocol != corev1.ProtocolTCP {
			t.Errorf("ports = %+v, want %d", s.Spec.Ports, port)
		}
		if s.Spec.Selector[model.LabelApp] != "app-1" || s.Labels[model.LabelApp] != "app-1" || s.Labels[model.LabelOwner] != "alice" {
			t.Errorf("service = %+v", s)
		}
	}
	if err := e.Start(ctx, someSpec()); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(ctx, sales); err != nil {
		t.Fatal(err)
	}
	svc()

	other := sales
	other.ID = "app-2"
	if err := e.Release(ctx, other); err != nil {
		t.Fatal(err)
	}
	svc()
	for range 2 {
		if err := e.Release(ctx, sales); err != nil {
			t.Fatal(err)
		}
	}
	if !gone(c, &corev1.Service{}, sales) {
		t.Error("the Service survived Release")
	}
}

// Tidy deletes the app's Secrets under their old names, handing the API
// token one of them names to revoke first. Another app's Secret of such a
// name stays.
func TestTidyDropsTheAppsSecretsUnderTheirOldNames(t *testing.T) {
	secret := func(name, app string, annotations map[string]string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: sales.Namespace, Name: name,
			Labels: map[string]string{model.LabelApp: app}, Annotations: annotations}}
	}
	e, c := executorOn(
		secret("alice-sales-javo-api", "app-1", map[string]string{model.AnnotationTokenID: "tok-1"}),
		secret("alice-sales-javo-git", "app-1", map[string]string{model.AnnotationTokenID: "not-an-api-token"}),
		secret("alice-sales-javo-nebi", "app-2", nil),
		secret("alice-sales.app-api", "app-1", nil),
	)
	var revoked []string
	revoke := func(_ context.Context, id string) error { revoked = append(revoked, id); return nil }
	if err := e.Tidy(ctx, sales, revoke); err != nil {
		t.Fatal(err)
	}
	if len(revoked) != 1 || revoked[0] != "tok-1" {
		t.Errorf("revoked %v", revoked)
	}
	left := func(name string) bool {
		return c.Get(ctx, client.ObjectKey{Namespace: sales.Namespace, Name: name}, &corev1.Secret{}) == nil
	}
	if left("alice-sales-javo-api") || left("alice-sales-javo-git") {
		t.Error("an old Secret of the app is left")
	}
	if !left("alice-sales-javo-nebi") || !left("alice-sales.app-api") {
		t.Error("a Secret that is not an old one of the app was deleted")
	}

	// A token that cannot be revoked keeps its Secret, to try again.
	e, c = executorOn(secret("alice-sales-javo-api", "app-1", map[string]string{model.AnnotationTokenID: "tok-1"}))
	if err := e.Tidy(ctx, sales, func(context.Context, string) error { return io.ErrUnexpectedEOF }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("Tidy = %v", err)
	}
	if !left("alice-sales-javo-api") {
		t.Error("the Secret went although its token was not revoked")
	}
}

func TestAddrIsTheServicesNameInTheCluster(t *testing.T) {
	got, err := (&Executor{}).Addr(ctx, sales, 8501)
	if err != nil || got != "alice-sales.javo.svc:8501" {
		t.Errorf("Addr = %q, %v", got, err)
	}
}

// podLogs records what Logs asks the cluster for.
type podLogs struct {
	namespace, pod string
	opts           *corev1.PodLogOptions
}

func (l *podLogs) Stream(_ context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	l.namespace, l.pod, l.opts = namespace, pod, opts
	return io.NopCloser(strings.NewReader("line\n")), nil
}

func TestLogsReadsTheUnitsContainer(t *testing.T) {
	logs := &podLogs{}
	e := &Executor{PodLogs: logs}
	since := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	rc, err := e.Logs(ctx, sales, executor.LogOptions{Unit: "build", Follow: true, Previous: true, Tail: 50, Since: since, Timestamps: true, MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	o := logs.opts
	if logs.namespace != "javo" || logs.pod != "alice-sales" || o.Container != "build" || !o.Follow || !o.Previous || !o.Timestamps ||
		o.TailLines == nil || *o.TailLines != 50 || o.LimitBytes == nil || *o.LimitBytes != 1024 || o.SinceTime == nil || !o.SinceTime.Time.Equal(since) {
		t.Errorf("asked %s/%s for %+v", logs.namespace, logs.pod, o)
	}

	// Nothing asked for: the whole log of the pod's own container.
	if _, err := e.Logs(ctx, sales, executor.LogOptions{}); err != nil {
		t.Fatal(err)
	}
	if o := logs.opts; o.Container != "" || o.TailLines != nil || o.LimitBytes != nil || o.SinceTime != nil || o.Follow {
		t.Errorf("asked for %+v", o)
	}
	if _, err := (&Executor{}).Logs(ctx, sales, executor.LogOptions{}); err == nil {
		t.Error("Logs without a log reader returned no error")
	}
}
