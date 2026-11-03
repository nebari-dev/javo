package files_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

const (
	ns    = "javo-apps"
	owner = "alice-aaaaaaaa"
	image = "example.org/rclone@sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// --- rclone client ---

func TestRcloneListParsesEntries(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/operations/list" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"list":[
			{"Path":"Data/b.csv","Name":"b.csv","Size":2,"MimeType":"text/csv","ModTime":"2026-10-30T19:22:27Z","IsDir":false},
			{"Path":"Data/sub","Name":"sub","Size":4096,"ModTime":"2026-10-29T08:00:00.5Z","IsDir":true}]}`))
	}))
	defer srv.Close()

	r := files.Rclone{HTTP: srv.Client()}
	got2, err := r.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "Data")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Truncated {
		t.Error("small listing marked truncated")
	}
	entries := got2.Entries
	if got["fs"] != "/home/user" || got["remote"] != "Data" {
		t.Errorf("request body = %v", got)
	}
	want := []files.Entry{
		{Name: "b.csv", Size: 2, ModTime: time.Date(2026, 10, 30, 19, 22, 27, 0, time.UTC)},
		{Name: "sub", IsDir: true, Size: 4096, ModTime: time.Date(2026, 10, 29, 8, 0, 0, 5e8, time.UTC)},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for i := range want {
		if entries[i].Name != want[i].Name || entries[i].IsDir != want[i].IsDir ||
			entries[i].Size != want[i].Size || !entries[i].ModTime.Equal(want[i].ModTime) {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
}

func TestRcloneListMissingFolderIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"error in ListJSON: directory not found","path":"operations/list","status":404}`))
	}))
	defer srv.Close()
	_, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "nope")
	if !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRcloneListServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	_, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "")
	if err == nil || errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want a non-NotFound error", err)
	}
}

// --- path cleaning ---

func TestCleanPath(t *testing.T) {
	ok := map[string]string{
		"":              "",
		"/":             "",
		".":             "",
		"Data":          "Data",
		"/Data/":        "Data",
		"Data//sub/./x": "Data/sub/x",
		"Data/sub/..":   "Data",
	}
	for in, want := range ok {
		got, err := files.CleanPath(in)
		if err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"..", "../", "../etc", "/../etc", "Data/../../x"} {
		if _, err := files.CleanPath(in); !errors.Is(err, files.ErrOutsideHome) {
			t.Errorf("CleanPath(%q) err = %v, want ErrOutsideHome", in, err)
		}
	}
}

// --- file pod lifecycle ---

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithIndex(&corev1.Event{}, "involvedObject.name", func(o client.Object) []string {
			return []string{o.(*corev1.Event).InvolvedObject.Name}
		}).Build()
}

func homeClaim() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: naming.HomeClaimName(owner), Namespace: ns,
		Annotations: map[string]string{model.AnnotationOwnerID: owner}}}
}

// After a mode change the home's files are on its new claim and the first
// one is deleted when its retention ends; the file pod follows the home.
func TestEnsureUsesTheHomesCurrentClaim(t *testing.T) {
	moved := homeClaim()
	moved.Name = naming.HomeClaimNameGen(owner, 2)
	c := newClient(t, moved)
	in := podstorage.Input{Home: &model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: moved.Name}}}
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{in}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want the pod starting on the new claim", err)
	}
	var pod corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &pod); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool {
		return v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == moved.Name
	}) {
		t.Fatalf("volumes = %+v", pod.Spec.Volumes)
	}
}

func newPods(t *testing.T, c client.Client, clk *clock) *files.Pods {
	t.Helper()
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{}, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func filePod(phase corev1.PodPhase, ready bool, ip string) *corev1.Pod {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: model.FilesPodPrefix + owner, Namespace: ns, UID: "uid-1",
			Labels: map[string]string{model.LabelRole: "files", model.LabelOwner: owner}},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr.To[int64](ownerUID)},
			Containers: []corev1.Container{{Name: "rclone",
				Env:          []corev1.EnvVar{{Name: "RCLONE_RC_USER", Value: "javo"}, {Name: "RCLONE_RC_PASS", Value: "pw-1"}},
				VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/home/user"}}}},
		},
		Status: corev1.PodStatus{Phase: phase, PodIP: ip,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}},
	}
}

func event(reason, msg string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ev-" + strings.ToLower(reason), Namespace: ns},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: model.FilesPodPrefix + owner, Namespace: ns, UID: "uid-1"},
		Reason:         reason, Message: msg, LastTimestamp: metav1.Now(),
	}
}

func TestNewPodsRequiresImage(t *testing.T) {
	if _, err := files.NewPods(files.PodsConfig{Client: newClient(t), Namespace: ns}); err == nil {
		t.Fatal("want error for empty image")
	}
}

func TestEnsureWithoutHomeClaim(t *testing.T) {
	c := newClient(t)
	_, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
	if !errors.Is(err, files.ErrNoHome) {
		t.Fatalf("err = %v, want ErrNoHome", err)
	}
	var pods corev1.PodList
	_ = c.List(context.Background(), &pods)
	if len(pods.Items) != 0 {
		t.Fatalf("created %d pods for a user with no home", len(pods.Items))
	}
}

func TestEnsureCreatesHardenedPod(t *testing.T) {
	c := newClient(t, homeClaim())
	_, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
	if !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
	var p corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &p); err != nil {
		t.Fatal(err)
	}
	if p.Labels[model.LabelRole] != "files" || p.Labels[model.LabelOwner] != owner {
		t.Errorf("labels = %v", p.Labels)
	}
	if p.Spec.AutomountServiceAccountToken == nil || *p.Spec.AutomountServiceAccountToken {
		t.Error("service account token is mounted")
	}
	psc := p.Spec.SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.RunAsUser == nil || *psc.RunAsUser != ownerUID ||
		psc.SeccompProfile == nil || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod security context = %+v", psc)
	}
	if len(p.Spec.Containers) != 1 {
		t.Fatalf("containers = %d", len(p.Spec.Containers))
	}
	ctr := p.Spec.Containers[0]
	if ctr.Image != image {
		t.Errorf("image = %q", ctr.Image)
	}
	if strings.Join(ctr.Args, " ") != "rcd --rc-addr=:5572 --rc-serve" {
		t.Errorf("args = %q", ctr.Args)
	}
	// rclone asks for a password of its own (read from RCLONE_RC_USER and
	// RCLONE_RC_PASS), so the home stays closed to a pod that reaches the
	// port where the network plugin does not enforce NetworkPolicy.
	if user, pass := envOf(ctr, "RCLONE_RC_USER"), envOf(ctr, "RCLONE_RC_PASS"); user == "" || len(pass) < 32 {
		t.Errorf("rclone user %q, password %q; want a user and a long password", user, pass)
	}
	sc := ctr.SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container security context = %+v", sc)
	}
	if rp := ctr.ReadinessProbe; rp == nil || rp.TCPSocket == nil || rp.TCPSocket.Port.IntValue() != 5572 {
		t.Errorf("readiness probe = %+v", rp)
	}
	// The picker waits on readiness, so it is checked from the moment the
	// container starts and then every second, not every two.
	if rp := ctr.ReadinessProbe; rp != nil && (rp.InitialDelaySeconds != 0 || rp.PeriodSeconds != 1) {
		t.Errorf("readiness probe delay %ds, period %ds; want 0s and 1s", rp.InitialDelaySeconds, rp.PeriodSeconds)
	}
	// Nothing runs before rclone: no init containers to wait for.
	if len(p.Spec.InitContainers) != 0 {
		t.Errorf("init containers = %d", len(p.Spec.InitContainers))
	}
	// The home claim is mounted writable at /home/user, for saves from the
	// file editor; the pod runs as the owner, so what it writes is theirs.
	var claimVol string
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			if v.PersistentVolumeClaim.ClaimName != "home-"+owner || v.PersistentVolumeClaim.ReadOnly {
				t.Errorf("claim volume = %+v", v.PersistentVolumeClaim)
			}
			claimVol = v.Name
		}
	}
	found := false
	for _, m := range ctr.VolumeMounts {
		if m.Name == claimVol {
			found = true
			if m.MountPath != "/home/user" || m.ReadOnly {
				t.Errorf("home mount = %+v", m)
			}
		}
	}
	if claimVol == "" || !found {
		t.Error("home claim is not mounted")
	}
}

// envOf is the value of the container's env var name ("" if unset).
func envOf(ctr corev1.Container, name string) string {
	for _, e := range ctr.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// Each file pod gets its own random password: knowing one opens no other.
func TestEachFilePodHasItsOwnPassword(t *testing.T) {
	ctx := context.Background()
	var passwords []string
	for range 2 {
		c := newClient(t, homeClaim())
		if _, err := newPods(t, c, &clock{time.Now()}).Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) {
			t.Fatalf("err = %v, want ErrHomeStarting", err)
		}
		var p corev1.Pod
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &p); err != nil {
			t.Fatal(err)
		}
		passwords = append(passwords, envOf(p.Spec.Containers[0], "RCLONE_RC_PASS"))
	}
	if passwords[0] == "" || passwords[0] == passwords[1] {
		t.Errorf("passwords = %q, want two different ones", passwords)
	}
}

func TestEnsureReadyPodReturnsAddress(t *testing.T) {
	c := newClient(t, homeClaim(), filePod(corev1.PodRunning, true, "10.0.0.5"))
	addr, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
	if err != nil || addr != "javo:pw-1@10.0.0.5:5572" {
		t.Fatalf("Ensure = %q, %v; want the pod's address with its rclone user and password", addr, err)
	}
}

// A file pod an earlier version made without a password serves the home to
// anyone who reaches it; it is replaced rather than used.
func TestEnsureReplacesAFilePodWithoutAPassword(t *testing.T) {
	old := filePod(corev1.PodRunning, true, "10.0.0.5")
	old.Spec.Containers[0].Env = nil
	c := newClient(t, homeClaim(), old)
	if _, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
	var p corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &p); !apierrors.IsNotFound(err) {
		t.Fatalf("pod without a password still there: %v", err)
	}
}

func withGroupVolume(p *corev1.Pod, dir string) *corev1.Pod {
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: podstorage.GroupVolume(dir)})
	return p
}

func TestEnsureGroupReusesAPodThatMountsTheFolder(t *testing.T) {
	c := newClient(t, homeClaim(), withGroupVolume(filePod(corev1.PodRunning, true, "10.0.0.5"), "ml"))
	addr, err := newPods(t, c, &clock{time.Now()}).EnsureGroup(context.Background(), owner, "ml")
	if err != nil || addr != "javo:pw-1@10.0.0.5:5572" {
		t.Fatalf("EnsureGroup = %q, %v", addr, err)
	}
}

// A folder that became ready, or a group joined, after the pod was created is
// not mounted in it; the pod is replaced instead of answering 404 until idle.
func TestEnsureGroupReplacesAPodCreatedBeforeTheFolderWasMounted(t *testing.T) {
	c := newClient(t, homeClaim(), filePod(corev1.PodRunning, true, "10.0.0.5"))
	_, err := newPods(t, c, &clock{time.Now()}).EnsureGroup(context.Background(), owner, "ml")
	if !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
	var p corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &p); !apierrors.IsNotFound(err) {
		t.Fatalf("pod without the folder still there: %v", err)
	}
}

func TestEnsurePendingPod(t *testing.T) {
	cases := []struct {
		name string
		ev   *corev1.Event
		want error
	}{
		{"no events", nil, files.ErrHomeStarting},
		{"volume node affinity", event("FailedScheduling", "0/3 nodes are available: 3 node(s) had volume node affinity conflict."), files.ErrHomeBusy},
		{"multi-attach", event("FailedAttachVolume", `Multi-Attach error for volume "pvc-1" Volume is already used by pod(s) app-1`), files.ErrHomeBusy},
		{"no capacity", event("FailedScheduling", "0/3 nodes are available: 3 Insufficient cpu."), files.ErrHomeStarting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{homeClaim(), filePod(corev1.PodPending, false, "")}
			if tc.ev != nil {
				objs = append(objs, tc.ev)
			}
			_, err := newPods(t, newClient(t, objs...), &clock{time.Now()}).Ensure(context.Background(), owner)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEnsureEventsOfOldPodAreIgnored(t *testing.T) {
	old := event("FailedAttachVolume", "Multi-Attach error")
	old.InvolvedObject.UID = "uid-old"
	c := newClient(t, homeClaim(), filePod(corev1.PodPending, false, ""), old)
	_, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
	if !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
}

func TestEnsureReplacesFinishedPod(t *testing.T) {
	c := newClient(t, homeClaim(), filePod(corev1.PodFailed, false, ""))
	_, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
	if !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
	var p corev1.Pod
	err = c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &p)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("failed pod still there: %v", err)
	}
}

func TestReapDeletesIdlePodsOnly(t *testing.T) {
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	clk := &clock{start}
	bob := "bob-5e6f7a8b"
	bobPod := filePod(corev1.PodRunning, true, "10.0.0.6")
	bobPod.Name, bobPod.Labels[model.LabelOwner] = model.FilesPodPrefix+bob, bob
	bobPod.CreationTimestamp = metav1.NewTime(start.Add(-11 * time.Minute))
	app := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sales-app", Namespace: ns,
		Labels: map[string]string{model.LabelOwner: bob}, CreationTimestamp: metav1.NewTime(start.Add(-time.Hour))}}
	alicePod := filePod(corev1.PodRunning, true, "10.0.0.5")
	alicePod.CreationTimestamp = metav1.NewTime(start.Add(-time.Hour))
	c := newClient(t, homeClaim(), alicePod, bobPod, app)
	pods := newPods(t, c, clk)
	ctx := context.Background()

	// Alice used her file pod 9 minutes ago; Bob's was never used by this gateway
	// and is 11 minutes old.
	clk.t = start.Add(-9 * time.Minute)
	if _, err := pods.Ensure(ctx, owner); err != nil {
		t.Fatal(err)
	}
	clk.t = start
	if err := pods.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	exists := func(name string) bool {
		return c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &corev1.Pod{}) == nil
	}
	if !exists(model.FilesPodPrefix + owner) {
		t.Error("alice's recently used file pod was deleted")
	}
	if exists(model.FilesPodPrefix + bob) {
		t.Error("bob's idle file pod was kept")
	}
	if !exists("sales-app") {
		t.Error("a workspace pod was deleted")
	}

	clk.t = start.Add(2 * time.Minute)
	if err := pods.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if exists(model.FilesPodPrefix + owner) {
		t.Error("alice's file pod was kept after 11 idle minutes")
	}
}

// --- lister ---

type stubEnsurer struct {
	addr string
	err  error
}

func (s stubEnsurer) Ensure(context.Context, string) (string, error) { return s.addr, s.err }
func (s stubEnsurer) Release(context.Context, string) error          { return nil }
func (s stubEnsurer) Touch(string)                                   {}
func (s stubEnsurer) EnsureGroup(context.Context, string, string) (string, error) {
	return s.addr, s.err
}

// stubRemote serves a fixed tree. Paths absent from the map are not found.
type stubRemote map[string][]files.Entry

func (s stubRemote) List(_ context.Context, _, _ string, remote string) (files.Listing, error) {
	e, ok := s[remote]
	if !ok {
		return files.Listing{}, files.ErrNotFound
	}
	return files.Listing{Entries: e, Truncated: len(e) > 1 && remote == "big"}, nil
}

// Stat and FindFile see the whole tree, and a name the parent does not list
// as the right kind (a symlink, in rclone) is not found.
func (s stubRemote) Stat(_ context.Context, _, _ string, remote string) (files.Entry, error) {
	dir, name := path.Split(remote)
	dir = strings.TrimSuffix(dir, "/")
	i := slices.IndexFunc(s[dir], func(e files.Entry) bool { return e.Name == name })
	if i < 0 {
		return files.Entry{}, files.ErrNotFound
	}
	return s[dir][i], nil
}

func (s stubRemote) FindFile(_ context.Context, _ string, dir, name string) (files.Entry, error) {
	i := slices.IndexFunc(s[dir], func(e files.Entry) bool { return !e.IsDir && e.Name == name })
	if i < 0 {
		return files.Entry{}, files.ErrNotFound
	}
	return s[dir][i], nil
}

// Open serves "content of <path>" for any path the tree lists as a file.
func (s stubRemote) Open(_ context.Context, _ string, remote string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("content of " + remote)), nil
}

func (s stubRemote) About(context.Context, string) (files.Usage, error) {
	return files.Usage{Used: 12992283648, Total: 21474836480}, nil
}

// countingEnsurer records whether a file pod was asked for.
type countingEnsurer struct {
	stubEnsurer
	calls *int
}

func (c countingEnsurer) Ensure(ctx context.Context, id string) (string, error) {
	*c.calls++
	return c.stubEnsurer.Ensure(ctx, id)
}

func TestRcloneAboutParsesUsedAndTotal(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/operations/about" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"total":21474836480,"used":12992283648,"free":8482552832}`))
	}))
	defer srv.Close()

	u, err := files.Rclone{HTTP: srv.Client()}.About(context.Background(), strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Used != 12992283648 || u.Total != 21474836480 || got["fs"] != "/home/user" {
		t.Errorf("About = %+v, request %v", u, got)
	}
}

func TestRcloneAboutServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "about not supported", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := (files.Rclone{HTTP: srv.Client()}).About(context.Background(), strings.TrimPrefix(srv.URL, "http://")); err == nil {
		t.Fatal("a failing about call must be an error")
	}
}

func TestUsageOfAHomeWhoseSizeIsNotEnforcedHasNoNumbersAndAsksNoOne(t *testing.T) {
	calls := 0
	l := files.NewLister(countingEnsurer{stubEnsurer{addr: "10.0.0.5:5572"}, &calls}, stubRemote{})
	got, err := l.Usage(context.Background(), owner, false)
	if err != nil || got != (files.HomeUsage{}) {
		t.Fatalf("Usage = %+v, %v", got, err)
	}
	if calls != 0 {
		t.Errorf("a file pod was started to measure a home whose size is not enforced")
	}
}

func TestUsageOfAnEnforcedHomeCarriesTheNumbers(t *testing.T) {
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, stubRemote{})
	got, err := l.Usage(context.Background(), owner, true)
	want := files.HomeUsage{Enforced: true, Used: 12992283648, Total: 21474836480}
	if err != nil || got != want {
		t.Fatalf("Usage = %+v, %v", got, err)
	}
}

func TestUsageOfAnEnforcedHomeWithoutAClaimIsEmptyNotAnError(t *testing.T) {
	l := files.NewLister(stubEnsurer{err: files.ErrNoHome}, stubRemote{})
	got, err := l.Usage(context.Background(), owner, true)
	if err != nil || got != (files.HomeUsage{Enforced: true}) {
		t.Fatalf("Usage = %+v, %v", got, err)
	}
}

func TestListerListsFolder(t *testing.T) {
	tree := stubRemote{
		"":         {{Name: "Data", IsDir: true}},
		"Data":     {{Name: "sub", IsDir: true}},
		"Data/sub": {{Name: "x.csv", Size: 3}},
	}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	got, err := l.List(context.Background(), owner, "Data/sub")
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "x.csv" {
		t.Fatalf("List = %+v, %v", got, err)
	}
}

// rclone skips symlinks in listings but follows one named directly, so every
// folder on the path must appear as a real folder in its parent's listing.
func TestListerRefusesFoldersNotListedInParent(t *testing.T) {
	tree := stubRemote{
		"":          {{Name: "Data", IsDir: true}, {Name: "notes.txt"}},
		"link":      {{Name: "etc", IsDir: true}},
		"link/etc":  {{Name: "passwd"}},
		"notes.txt": {},
	}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	for _, p := range []string{"link", "link/etc", "notes.txt"} {
		if _, err := l.List(context.Background(), owner, p); !errors.Is(err, files.ErrNotFound) {
			t.Errorf("List(%q) err = %v, want ErrNotFound", p, err)
		}
	}
}

func TestListerWithoutHome(t *testing.T) {
	l := files.NewLister(stubEnsurer{err: files.ErrNoHome}, stubRemote{})
	got, err := l.List(context.Background(), owner, "")
	if err != nil || got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("home root = %+v, %v; want empty", got, err)
	}
	if _, err := l.List(context.Background(), owner, "Data"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("subfolder err = %v, want ErrNotFound", err)
	}
}

func TestListerPassesPodErrors(t *testing.T) {
	l := files.NewLister(stubEnsurer{err: files.ErrHomeBusy}, stubRemote{})
	if _, err := l.List(context.Background(), owner, ""); !errors.Is(err, files.ErrHomeBusy) {
		t.Fatalf("err = %v, want ErrHomeBusy", err)
	}
}

func TestReleaseDeletesTheUsersFilePod(t *testing.T) {
	c := newClient(t, homeClaim(), filePod(corev1.PodRunning, true, "10.0.0.5"))
	p := newPods(t, c, &clock{time.Now()})
	if err := p.Release(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &corev1.Pod{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("file pod still there: %v", err)
	}
	if err := p.Release(context.Background(), owner); err != nil {
		t.Fatalf("release without a pod: %v", err)
	}
}

func TestReleaseKeepsPodThatIsNotThisUsersFilePod(t *testing.T) {
	pod := filePod(corev1.PodRunning, true, "10.0.0.5")
	pod.Labels = map[string]string{model.LabelWorkspace: "x", model.LabelOwner: owner}
	c := newClient(t, homeClaim(), pod)
	if err := newPods(t, c, &clock{time.Now()}).Release(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &corev1.Pod{}); err != nil {
		t.Fatalf("a pod without the file pod labels was deleted: %v", err)
	}
}

func TestListerReportsTruncation(t *testing.T) {
	tree := stubRemote{
		"":    {{Name: "big", IsDir: true}},
		"big": {{Name: "a"}, {Name: "b"}},
	}
	got, err := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree).List(context.Background(), owner, "big")
	if err != nil || !got.Truncated || len(got.Entries) != 2 {
		t.Fatalf("List = %+v, %v", got, err)
	}
}

func rcloneServing(t *testing.T, n int, name func(i int) string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"list":[`))
		for i := 0; i < n; i++ {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			b, _ := json.Marshal(map[string]any{"Name": name(i), "Path": name(i), "Size": 1, "IsDir": false, "ModTime": "2026-10-30T19:22:27Z"})
			_, _ = w.Write(b)
		}
		_, _ = w.Write([]byte(`]}`))
	}))
}

func TestRcloneListCapsEntryCount(t *testing.T) {
	srv := rcloneServing(t, files.MaxEntries+1, func(i int) string { return fmt.Sprintf("f%05d.txt", i) })
	defer srv.Close()
	got, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "")
	if err != nil || !got.Truncated || len(got.Entries) != files.MaxEntries {
		t.Fatalf("got %d entries, truncated %v, err %v", len(got.Entries), got.Truncated, err)
	}
}

func TestRcloneListExactlyAtCapIsComplete(t *testing.T) {
	srv := rcloneServing(t, files.MaxEntries, func(i int) string { return fmt.Sprintf("f%05d.txt", i) })
	defer srv.Close()
	got, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "")
	if err != nil || got.Truncated || len(got.Entries) != files.MaxEntries {
		t.Fatalf("got %d entries, truncated %v, err %v", len(got.Entries), got.Truncated, err)
	}
}

// Long names can exhaust the byte cap before the entry cap; that is a
// truncated listing, not a decode failure.
func TestRcloneListCapsBytes(t *testing.T) {
	long := strings.Repeat("n", 250)
	srv := rcloneServing(t, files.MaxEntries, func(i int) string { return long + fmt.Sprint(i) + "/" + long + "/" + long + "/" + long + "/" + long })
	defer srv.Close()
	got, err := files.Rclone{HTTP: srv.Client()}.List(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "/home/user", "")
	if err != nil || !got.Truncated || len(got.Entries) == 0 || len(got.Entries) >= files.MaxEntries {
		t.Fatalf("got %d entries, truncated %v, err %v", len(got.Entries), got.Truncated, err)
	}
}

func TestEnsureRefusesPodThatIsNotThisUsersFilePod(t *testing.T) {
	cases := map[string]map[string]string{
		"workspace pod": {model.LabelWorkspace: model.FilesPodPrefix + owner, model.LabelOwner: owner},
		"other owner":   {model.LabelRole: "files", model.LabelOwner: "bob-5e6f7a8b"},
		"no labels":     nil,
	}
	for name, labels := range cases {
		t.Run(name, func(t *testing.T) {
			pod := filePod(corev1.PodFailed, false, "")
			pod.Labels = labels
			c := newClient(t, homeClaim(), pod)
			addr, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
			if err == nil || addr != "" || errors.Is(err, files.ErrHomeStarting) {
				t.Fatalf("Ensure = %q, %v; want a refusal", addr, err)
			}
			if c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &corev1.Pod{}) != nil {
				t.Fatal("foreign pod was deleted")
			}
		})
	}
}

// stubStorage answers every Input call with in.
type stubStorage struct{ in podstorage.Input }

func (s stubStorage) Input(context.Context, string, string) (podstorage.Input, error) {
	return s.in, nil
}

func filePodWith(t *testing.T, in podstorage.Input) corev1.Pod {
	t.Helper()
	c := newClient(t, homeClaim())
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{in}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Ensure(context.Background(), owner)
	var pod corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &pod); err != nil {
		t.Fatal(err)
	}
	return pod
}

// Where the owner's apps must share a node, so must the file pod; the layout
// decides, the file pod has no rule of its own.
func TestFilePodFollowsTheLayoutsCoLocation(t *testing.T) {
	a := filePodWith(t, podstorage.Input{CoLocate: true}).Spec.Affinity
	if a == nil || a.PodAffinity == nil || len(a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("affinity = %+v", a)
	}
	term :=
		a.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0]
	if term.TopologyKey != "kubernetes.io/hostname" || term.LabelSelector.MatchLabels[model.LabelOwner] != owner {
		t.Errorf("affinity term = %+v", term)
	}
	if got := filePodWith(t, podstorage.Input{}).Spec.Affinity; got != nil {
		t.Errorf("affinity without co-location = %+v, want none", got)
	}
}

// The file pod lists group folders too, read-only, with the folders' gids as
// supplemental groups and no fsGroup.
func TestFilePodMountsGroupFoldersReadOnly(t *testing.T) {
	folder := model.SharedFolder{
		Spec:   model.SharedFolderSpec{Group: "research"},
		Status: model.SharedFolderStatus{ClaimName: "folder-research", GID: 5001, Prepared: true},
	}
	p := filePodWith(t, podstorage.Input{Folders: []model.SharedFolder{folder}})
	sc := p.Spec.SecurityContext
	if sc.FSGroup != nil || len(sc.SupplementalGroups) != 1 || sc.SupplementalGroups[0] != 5001 {
		t.Errorf("pod security context = %+v", sc)
	}
	var folderMount *corev1.VolumeMount
	for i, m := range p.Spec.Containers[0].VolumeMounts {
		if m.MountPath == "/shared/research" {
			folderMount = &p.Spec.Containers[0].VolumeMounts[i]
		}
	}
	if folderMount == nil || !folderMount.ReadOnly || folderMount.SubPath != "data" {
		t.Errorf("folder mount = %+v", folderMount)
	}
}

// Two owners whose slugs collide would get the same home claim name; the
// claim's owner annotation keeps one from reading the other's home.
func TestEnsureRefusesHomeOfAnotherOwner(t *testing.T) {
	for name, ann := range map[string]map[string]string{
		"other owner":   {model.AnnotationOwnerID: "mallory-cccccccc"},
		"no annotation": nil,
	} {
		t.Run(name, func(t *testing.T) {
			claim := homeClaim()
			claim.Annotations = ann
			c := newClient(t, claim)
			_, err := newPods(t, c, &clock{time.Now()}).Ensure(context.Background(), owner)
			if !errors.Is(err, files.ErrHomeConflict) {
				t.Fatalf("err = %v, want ErrHomeConflict", err)
			}
			var pods corev1.PodList
			_ = c.List(context.Background(), &pods)
			if len(pods.Items) != 0 {
				t.Fatal("file pod created for another owner's home")
			}
		})
	}
}

// The file pod goes where the home claim is: the namespace the call names,
// not the default one.
func TestEnsureUsesTheNamespaceOfTheCall(t *testing.T) {
	const other = "javo-alice-1a2b3c4d"
	claim := homeClaim()
	claim.Namespace = other
	c := newClient(t, claim)
	ctx := files.WithNamespace(context.Background(), other)

	if _, err := newPods(t, c, &clock{time.Now()}).Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v, want ErrHomeStarting", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: other, Name: model.FilesPodPrefix + owner}, &corev1.Pod{}); err != nil {
		t.Fatalf("no file pod in %s: %v", other, err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: model.FilesPodPrefix + owner}, &corev1.Pod{}); err == nil {
		t.Fatalf("file pod created in the default namespace %s", ns)
	}
}

func TestReapSweepsEveryNamespaceItIsGiven(t *testing.T) {
	const other = "javo-alice-1a2b3c4d"
	old := filePod(corev1.PodRunning, true, "10.0.0.5")
	old.Namespace = other
	old.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	c := newClient(t, old)
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{},
		ReapIn: func(context.Context) ([]string, error) { return []string{ns, other}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(old), &corev1.Pod{}); err == nil {
		t.Fatal("idle file pod in the other namespace survived")
	}
}

func oldFilePodIn(namespace string) *corev1.Pod {
	p := filePod(corev1.PodRunning, true, "10.0.0.5")
	p.Namespace = namespace
	p.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	return p
}

func gone(t *testing.T, c client.Client, p *corev1.Pod) bool {
	t.Helper()
	return apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKeyFromObject(p), &corev1.Pod{}))
}

// One namespace that cannot be read must not stop idle pods being cleaned up
// everywhere else.
func TestReapGoesOnPastANamespaceThatFails(t *testing.T) {
	broken, fine := "javo-broken", "javo-fine"
	old := oldFilePodIn(fine)
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(old).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
			var lo client.ListOptions
			lo.ApplyOptions(opts)
			if lo.Namespace == broken {
				return errors.New("boom")
			}
			return c.List(ctx, l, opts...)
		}}).Build()
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{},
		ReapIn: func(context.Context) ([]string, error) { return []string{broken, fine}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Reap(context.Background()); err == nil {
		t.Error("Reap hid the failure")
	}
	if !gone(t, c, old) {
		t.Fatal("idle pod in the readable namespace survived")
	}
}

// With a namespace per person the sweep is one cluster-wide call, not one per
// person, and it leaves namespaces the controller does not manage alone.
func TestReapAcrossAllNamespacesIsOneCall(t *testing.T) {
	a, b, stray := oldFilePodIn("javo-a"), oldFilePodIn("javo-b"), oldFilePodIn("stray")
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	var lists int
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(a, b, stray).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
			lists++
			return c.List(ctx, l, opts...)
		}}).Build()
	p, err := files.NewPods(files.PodsConfig{Client: c, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{}, ReapAcrossNamespaces: true,
		ReapIn: func(context.Context) ([]string, error) { return []string{"javo-a", "javo-b"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Reap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lists != 1 {
		t.Errorf("%d list calls, want 1", lists)
	}
	if !gone(t, c, a) || !gone(t, c, b) {
		t.Error("idle pods in managed namespaces survived")
	}
	if gone(t, c, stray) {
		t.Error("pod in an unmanaged namespace was deleted")
	}
}

// --- object reads ---

// The client logs in to rclone with the user and password the address
// carries, for remote control calls and object reads alike.
func TestRcloneSendsTheAddressCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "javo" || p != "pw-1" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"list":[]}`))
			return
		}
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	r := files.Rclone{HTTP: srv.Client()}
	addr := "javo:pw-1@" + strings.TrimPrefix(srv.URL, "http://")
	if _, err := r.List(context.Background(), addr, "/home/user", ""); err != nil {
		t.Errorf("list: %v", err)
	}
	rc, err := r.Open(context.Background(), addr, "a.txt")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rc.Close()
	if body, _ := io.ReadAll(rc); string(body) != "hello" {
		t.Errorf("open read %q", body)
	}
	// The password never shows in an error.
	_, err = r.List(context.Background(), "javo:pw-1@127.0.0.1:1", "/home/user", "")
	if err == nil || strings.Contains(err.Error(), "pw-1") {
		t.Errorf("unreachable rclone: %v, want an error without the password", err)
	}
}

func TestRcloneOpenStreamsTheObject(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()
	rc, err := files.Rclone{HTTP: srv.Client()}.Open(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "jobs/a b/out.html")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "hello" || gotMethod != http.MethodGet || gotPath != "/[/home/user]/jobs/a b/out.html" {
		t.Fatalf("got %q via %s %q", body, gotMethod, gotPath)
	}
}

func TestRcloneOpenMissingObjectIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	_, err := files.Rclone{HTTP: srv.Client()}.Open(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "nope.txt")
	if !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListerOpensAFileInTheHome(t *testing.T) {
	mod := time.Date(2026, 10, 30, 19, 22, 27, 0, time.UTC)
	tree := stubRemote{
		"":     {{Name: "jobs", IsDir: true}},
		"jobs": {{Name: "out.html", Size: 12, ModTime: mod}},
	}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	rc, info, err := l.Open(context.Background(), owner, "jobs/out.html")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	body, _ := io.ReadAll(rc)
	if string(body) != "content of jobs/out.html" || info.Name != "out.html" || info.Size != 12 || !info.ModTime.Equal(mod) {
		t.Fatalf("got %q, %+v", body, info)
	}
}

// rclone skips symlinks in listings but serves one named directly, so only a
// name the parent's listing shows as a file may be opened.
func TestListerOpenRefusesWhatTheListingDoesNotShowAsAFile(t *testing.T) {
	tree := stubRemote{
		"":     {{Name: "jobs", IsDir: true}},
		"jobs": {{Name: "sub", IsDir: true}, {Name: "ok.txt", Size: 1}},
	}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	for _, rel := range []string{"jobs/link", "jobs/sub", "jobs/nope/x.txt", "", "jobs"} {
		if _, _, err := l.Open(context.Background(), owner, rel); !errors.Is(err, files.ErrNotFound) {
			t.Errorf("Open(%q) err = %v, want ErrNotFound", rel, err)
		}
	}
}

func TestListerOpenWithoutHome(t *testing.T) {
	l := files.NewLister(stubEnsurer{err: files.ErrNoHome}, stubRemote{})
	if _, _, err := l.Open(context.Background(), owner, "a.txt"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListerOpenRefusesFilesOverTheCap(t *testing.T) {
	tree := stubRemote{"": {{Name: "big.bin", Size: files.MaxOpenBytes + 1}, {Name: "edge.bin", Size: files.MaxOpenBytes}}}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	if _, _, err := l.Open(context.Background(), owner, "big.bin"); !errors.Is(err, files.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	rc, _, err := l.Open(context.Background(), owner, "edge.bin")
	if err != nil {
		t.Fatalf("file exactly at the cap: %v", err)
	}
	rc.Close()
}

// cutRemote lists only the first entry of every folder and says so, like a
// folder past files.MaxEntries; every other call sees the whole tree.
type cutRemote struct{ stubRemote }

func (c cutRemote) List(ctx context.Context, addr, fs, remote string) (files.Listing, error) {
	l, err := c.stubRemote.List(ctx, addr, fs, remote)
	if err == nil && len(l.Entries) > 1 {
		l.Entries, l.Truncated = l.Entries[:1], true
	}
	return l, err
}

// A job on a short cron fills its runs folder past the listing cap; a file or
// folder beyond the cut still exists.
func TestListerFindsNamesBeyondATruncatedListing(t *testing.T) {
	tree := cutRemote{stubRemote{
		"":              {{Name: "jobs", IsDir: true}},
		"jobs":          {{Name: "run-1", IsDir: true}, {Name: "run-2", IsDir: true}},
		"jobs/run-2":    {{Name: "a.log"}, {Name: "output.html", Size: 7}},
		"jobs/run-2/sx": {},
	}}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, tree)
	rc, info, err := l.Open(context.Background(), owner, "jobs/run-2/output.html")
	if err != nil {
		t.Fatalf("Open beyond the cut: %v", err)
	}
	rc.Close()
	if info.Name != "output.html" || info.Size != 7 {
		t.Fatalf("info = %+v", info)
	}
	got, err := l.List(context.Background(), owner, "jobs/run-2")
	if err != nil || !got.Truncated || len(got.Entries) != 1 {
		t.Fatalf("List of a folder beyond the cut = %+v, %v", got, err)
	}
	if _, _, err := l.Open(context.Background(), owner, "jobs/run-2/missing.html"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("missing file err = %v, want ErrNotFound", err)
	}
}

func TestRcloneStatReportsAnEntryOrNotFound(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/stat" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["remote"] == "nope" {
			_, _ = w.Write([]byte(`{"item":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"item":{"Path":"jobs/run-2","Name":"run-2","Size":4096,"ModTime":"2026-10-30T19:22:27Z","IsDir":true}}`))
	}))
	defer srv.Close()
	r := files.Rclone{HTTP: srv.Client()}
	addr := strings.TrimPrefix(srv.URL, "http://")
	e, err := r.Stat(context.Background(), addr, "/home/user", "jobs/run-2")
	if err != nil || e.Name != "run-2" || !e.IsDir || got["fs"] != "/home/user" || got["remote"] != "jobs/run-2" {
		t.Fatalf("Stat = %+v, %v (request %v)", e, err, got)
	}
	if _, err := r.Stat(context.Background(), addr, "/home/user", "nope"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("missing err = %v, want ErrNotFound", err)
	}
}

// FindFile lists the folder with a filter for one exact name, so the answer
// does not depend on the folder's size. Glob characters in the name are
// escaped; a listing that returns anything but that file is not a match.
func TestRcloneFindFileAsksForOneEscapedName(t *testing.T) {
	var got struct {
		Remote string `json:"remote"`
		Opt    struct {
			FilesOnly bool `json:"filesOnly"`
		} `json:"opt"`
		Filter struct {
			IncludeRule []string `json:"IncludeRule"`
		} `json:"_filter"`
	}
	answer := `{"list":[{"Path":"d/x[1].txt","Name":"x[1].txt","Size":3,"ModTime":"2026-10-30T19:22:27Z","IsDir":false}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/list" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	r := files.Rclone{HTTP: srv.Client()}
	addr := strings.TrimPrefix(srv.URL, "http://")
	e, err := r.FindFile(context.Background(), addr, "d", `x[1].txt`)
	if err != nil || e.Name != "x[1].txt" || e.Size != 3 {
		t.Fatalf("FindFile = %+v, %v", e, err)
	}
	if got.Remote != "d" || !got.Opt.FilesOnly || len(got.Filter.IncludeRule) != 1 || got.Filter.IncludeRule[0] != `x\[1\].txt` {
		t.Fatalf("request = %+v", got)
	}
	answer = `{"list":[]}`
	if _, err := r.FindFile(context.Background(), addr, "d", "x1.txt"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("no match err = %v, want ErrNotFound", err)
	}
	answer = `{"list":[{"Path":"d/other.txt","Name":"other.txt","Size":3,"IsDir":false}]}`
	if _, err := r.FindFile(context.Background(), addr, "d", "x1.txt"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("different name err = %v, want ErrNotFound", err)
	}
}

func TestReapKeepsThePodOfAUserWhoIsStillActive(t *testing.T) {
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	clk := &clock{start.Add(-time.Hour)}
	alicePod := filePod(corev1.PodRunning, true, "10.0.0.5")
	alicePod.CreationTimestamp = metav1.NewTime(start.Add(-time.Hour))
	c := newClient(t, homeClaim(), alicePod)
	pods := newPods(t, c, clk)
	ctx := context.Background()
	if _, err := pods.Ensure(ctx, owner); err != nil {
		t.Fatal(err)
	}
	// Alice has not opened a folder for an hour but used the API a minute ago.
	clk.t = start.Add(-time.Minute)
	pods.Touch(owner)
	clk.t = start
	if err := pods.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Get(ctx, client.ObjectKey{Namespace: ns, Name: alicePod.Name}, &corev1.Pod{}) != nil {
		t.Fatal("the file pod of an active user was deleted")
	}
}

// An app pod and the file pod built from one Input mount the same home and
// folder claims at the same paths; only ReadOnly differs.
func TestFilePodAndAppPodShareStorageLayout(t *testing.T) {
	folder := func(group string, gid int64) model.SharedFolder {
		return model.SharedFolder{
			Name:   "f-" + naming.GroupDir(group),
			Spec:   model.SharedFolderSpec{Group: group},
			Status: model.SharedFolderStatus{ClaimName: "folder-" + naming.GroupDir(group), GID: gid, Prepared: true},
		}
	}
	in := podstorage.Input{
		Home:    &model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: naming.HomeClaimName(owner)}},
		Folders: []model.SharedFolder{folder("research", 5001), folder("/ops", 5002)},
		UID:     1000, GID: 100,
	}
	ws := &model.App{ID: "app-1", Namespace: ns, Name: "app", Owner: owner, Template: "t"}
	tpl := &model.Template{
		Name: "t",
		Spec: model.TemplateSpec{Kind: "t", Image: "example.com/app:1", Port: 8000, Command: []string{"app"}},
	}

	appPod, err := workspace.BuildPod(ws, tpl, "/u/a/app/", "", podstorage.Build(owner, in), sizes.Size{}, workspace.Images{})
	if err != nil {
		t.Fatal(err)
	}
	filePod := filePodWith(t, in)

	claims := func(p corev1.Pod) map[string]string { // volume name -> claim
		m := map[string]string{}
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				m[v.Name] = v.PersistentVolumeClaim.ClaimName
			}
		}
		return m
	}
	mounts := func(p corev1.Pod) map[string]string { // mount path -> volume name
		m := map[string]string{}
		for _, v := range p.Spec.Containers[0].VolumeMounts {
			if v.MountPath == "/home/user" || strings.HasPrefix(v.MountPath, "/shared/") {
				m[v.MountPath] = v.Name
			}
		}
		return m
	}
	if got, want := claims(filePod), claims(*appPod); !reflect.DeepEqual(got, want) || len(want) != 3 {
		t.Errorf("claims: file pod %v, app pod %v (want 3 equal)", got, want)
	}
	if got, want := mounts(filePod), mounts(*appPod); !reflect.DeepEqual(got, want) || len(want) != 3 {
		t.Errorf("mounts: file pod %v, app pod %v (want 3 equal)", got, want)
	}
}

// fsRecorder serves one tree and records the filesystem root it was asked for.
type fsRecorder struct {
	stubRemote
	fs []string
}

func (r *fsRecorder) List(ctx context.Context, addr, fs, remote string) (files.Listing, error) {
	r.fs = append(r.fs, fs)
	return r.stubRemote.List(ctx, addr, fs, remote)
}

func TestListGroupListsTheGroupFoldersMount(t *testing.T) {
	rec := &fsRecorder{stubRemote: stubRemote{"": {{Name: "data", IsDir: true}}, "data": {{Name: "x.csv", Size: 3}}}}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, rec)
	got, err := l.ListGroup(context.Background(), owner, "data-science", "data")
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "x.csv" {
		t.Fatalf("ListGroup = %+v, %v", got, err)
	}
	for _, fs := range rec.fs {
		if fs != "/shared/data-science" {
			t.Errorf("listed %q, want /shared/data-science", fs)
		}
	}
}

func TestListGroupRefusesFoldersNotListedInParent(t *testing.T) {
	rec := &fsRecorder{stubRemote: stubRemote{"": {{Name: "ok", IsDir: true}}, "link": {{Name: "passwd"}}}}
	l := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, rec)
	if _, err := l.ListGroup(context.Background(), owner, "data-science", "link"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListStillListsTheHome(t *testing.T) {
	rec := &fsRecorder{stubRemote: stubRemote{"": {{Name: "Data", IsDir: true}}}}
	if _, err := files.NewLister(stubEnsurer{addr: "10.0.0.5:5572"}, rec).List(context.Background(), owner, ""); err != nil {
		t.Fatal(err)
	}
	if len(rec.fs) != 1 || rec.fs[0] != "/home/user" {
		t.Errorf("listed %v, want /home/user", rec.fs)
	}
}

// dirEnsurer records the group folder a pod was asked to mount.
type dirEnsurer struct {
	stubEnsurer
	dir *string
}

func (d dirEnsurer) EnsureGroup(ctx context.Context, id, dir string) (string, error) {
	*d.dir = dir
	return d.stubEnsurer.EnsureGroup(ctx, id, dir)
}

func TestListGroupAsksForAPodThatMountsTheFolder(t *testing.T) {
	var dir string
	tree := stubRemote{"": {{Name: "a.txt"}}}
	l := files.NewLister(dirEnsurer{stubEnsurer{addr: "10.0.0.5:5572"}, &dir}, tree)
	if _, err := l.ListGroup(context.Background(), owner, "ml", ""); err != nil || dir != "ml" {
		t.Fatalf("dir %q, err %v", dir, err)
	}
}
