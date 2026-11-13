package docker_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/dockerhome"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
	"github.com/nebari-dev/javo/internal/workload"
)

// The tests below run homes and the file helper against a real Docker
// daemon, with the rclone image the Kubernetes file pod runs, and are
// skipped when no daemon answers or the image cannot be pulled.
//
// Other test runs share the daemon. Everything made here carries
// homesMark with a value of this process's own, and only what carries it
// is removed; none of it carries the label the other daemon tests sweep.

const (
	homesMark = "javo.homes-test"
	homesRole = "javo.homes-test.role"

	alice    = "alice-aaaaaaaa"
	bob      = "bob-bbbbbbbb"
	aliceUID = 20001
	bobUID   = 20002
	userGID  = 100
)

// homesRun marks what this run of the tests creates.
var homesRun = strings.ToLower(rand.Text())[:10]

// homesPool is where the networks of these tests take their addresses,
// away from the pool of the package's other daemon tests.
var homesPool = netip.MustParsePrefix("10.213.91.0/24")

// rcloneImage is the image of the file pod, as the chart pins it.
func rcloneImage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../deploy/helm/javo/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*image: (rclone/rclone:\S+@sha256:[0-9a-f]{64})\s*$`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("the chart's values name no rclone image pinned by digest")
	}
	return string(m[1])
}

// uids are the ids of the people of these tests.
type uids map[string]int64

func (u uids) UID(_ context.Context, owner string) (int64, int64, error) {
	uid, ok := u[owner]
	if !ok {
		return 0, 0, ids.ErrNoUID
	}
	return uid, userGID, nil
}

// rig is homes and file helpers on the daemon of this machine: a
// database, the home reconciler, a container that stands in for Javo's
// gateway, an executor that runs the helpers and a Lister over them.
type rig struct {
	ns string
	// all sees everything of this run; the others make what one part of
	// Javo makes, each under a role of its own, as servercmd wires them.
	all, homes, helpers, apps *docker.Client
	gw                        string
	db                        *store.Store
	rec                       *storage.DockerHomeReconciler
	exec                      *docker.Executor
	logins                    *files.Logins
	pods                      *files.Helpers
	lister                    *files.Lister
	now                       time.Time
	changed                   chan struct{}
}

func homesSocket(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	sockets := []string{"/var/run/docker.sock", filepath.Join(home, ".colima/default/docker.sock"), filepath.Join(home, ".docker/run/docker.sock")}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		sockets = []string{h}
	}
	for _, sock := range sockets {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := docker.New(sock).Daemon(ctx)
		cancel()
		if errors.Is(err, docker.ErrTooOld) {
			t.Skipf("the Docker daemon at %s is too old: %v", sock, err)
		}
		if err == nil {
			return sock
		}
	}
	t.Skip("no Docker daemon answers")
	return ""
}

func homesContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func newRig(t *testing.T) *rig {
	t.Helper()
	sock := homesSocket(t)
	ctx := homesContext(t)
	client := func(role string) *docker.Client {
		c := docker.New(sock)
		c.Labels = map[string]string{homesMark: homesRun}
		if role != "" {
			c.Labels[homesRole] = role
		}
		return c
	}
	r := &rig{
		ns:  "ht-" + homesRun + "-" + strings.ToLower(rand.Text())[:6],
		all: client(""), homes: client("homes"), helpers: client("files"), apps: client("apps"),
		now: time.Now(), changed: make(chan struct{}, 1),
	}
	image := rcloneImage(t)
	for _, img := range []string{docker.TestImage, image} {
		if err := docker.EnsureImage(ctx, r.all, img, false, nil); err != nil {
			t.Skipf("cannot pull %s: %v", img, err)
		}
	}
	t.Cleanup(func() { r.sweep(t) })

	// The gateway: on the daemon's default network, as Javo's is, and
	// listening on port 9090.
	gwSpec := r.spec("gateway", aliceUID)
	body, err := docker.ContainerBody(gwSpec, workload.Unit{Name: "gateway", Image: docker.TestImage, Command: []string{"sh", "-c", "mkdir /tmp/w && echo gateway > /tmp/w/index.html && httpd -p 9090 -h /tmp/w; sleep 86400"}}, docker.Inputs{})
	must(t, err)
	body.HostConfig.NetworkMode = "bridge"
	r.gw, err = client("gateway").ContainerCreate(ctx, "javo-"+r.ns+".gateway", body)
	must(t, err)
	must(t, r.all.ContainerStart(ctx, r.gw))

	r.db = storetest.New(t)
	volumes := dockerhome.Volumes{Client: r.homes, Image: docker.TestImage}
	r.rec = &storage.DockerHomeReconciler{Rows: storage.Rows{Store: r.db}, Volumes: volumes, UIDs: uids{alice: aliceUID, bob: bobUID}}
	r.logins = files.NewLogins()
	r.exec, err = docker.NewTestExecutor(ctx, docker.Options{
		Client: r.helpers, Secrets: r.logins, Gateway: r.gw, RunnerImage: docker.TestImage, Pool: homesPool, Grace: time.Second,
	})
	must(t, err)
	t.Cleanup(r.exec.Close)
	watching, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	go func() {
		_ = r.exec.Supervisor.Watch(watching, func(workload.Ref) {
			select {
			case r.changed <- struct{}{}:
			default:
			}
		})
	}()
	r.pods, err = files.NewHelpers(files.HelpersConfig{
		Exec: r.exec, Logins: r.logins, Volumes: volumes, Namespace: r.ns, Image: image,
		UIDs:    r.rec.UIDs,
		Storage: podstorage.Provisioner{Resolver: podstorage.Resolver{Homes: r.db.Homes()}, Maker: r.db.Homes()},
		Now:     func() time.Time { return r.now },
	})
	must(t, err)
	// The gateway reaches a helper from inside the gateway container.
	dial := docker.DialThrough(r.all, r.gw)
	r.lister = files.NewLister(r.pods, files.Rclone{HTTP: &http.Client{Transport: &http.Transport{DialContext: dial, DisableKeepAlives: true}}})
	return r
}

// sweep removes what this run made, and only that, and says what stays.
func (r *rig) sweep(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mark := homesMark + "=" + homesRun
	ns := docker.LabelNamespace + "=" + r.ns
	containers, err := r.all.ContainerList(ctx, mark, ns)
	must(t, err)
	for _, c := range containers {
		if err := r.all.ContainerRemove(ctx, c.ID); err != nil {
			t.Errorf("cleanup of container %v: %v", c.Names, err)
		}
	}
	networks, err := r.all.NetworkList(ctx, mark, ns)
	must(t, err)
	for _, n := range networks {
		if err := r.all.NetworkRemove(ctx, n.Name); err != nil {
			t.Errorf("cleanup of network %s: %v", n.Name, err)
		}
	}
	volumes, err := r.all.VolumeList(ctx, mark, ns)
	must(t, err)
	for _, v := range volumes {
		if err := r.all.VolumeRemove(ctx, v.Name); err != nil {
			t.Errorf("cleanup of volume %s: %v", v.Name, err)
		}
	}
	containers, _ = r.all.ContainerList(ctx, mark, ns)
	networks, _ = r.all.NetworkList(ctx, mark, ns)
	volumes, _ = r.all.VolumeList(ctx, mark, ns)
	if len(containers)+len(networks)+len(volumes) != 0 {
		t.Errorf("left on the daemon: %d containers, %d networks, %d volumes", len(containers), len(networks), len(volumes))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// spec is a workload of these tests called name, running as uid.
func (r *rig) spec(name string, uid int64) workload.Spec {
	return workload.Spec{
		Ref:       workload.Ref{Namespace: r.ns, Name: name, ID: "id-" + name, Owner: name},
		User:      workload.User{UID: uid, GID: userGID},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
	}
}

// reconcile runs the home reconciler over who's home row and returns the
// home.
func (r *rig) reconcile(t *testing.T, who string) model.Home {
	t.Helper()
	ctx := homesContext(t)
	h, err := r.db.Homes().ByOwner(ctx, r.ns, who)
	must(t, err)
	req := reconcile.Request{}
	req.Name = h.ID
	_, err = r.rec.Reconcile(ctx, req)
	must(t, err)
	h, err = r.db.Homes().Get(ctx, h.ID)
	must(t, err)
	return h
}

// home is who's home, asked for as the first look at the files asks for
// it and made by the reconciler.
func (r *rig) home(t *testing.T, who string) model.Home {
	t.Helper()
	if _, err := r.pods.Ensure(homesContext(t), who); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("Ensure for a person without a home = %v, want ErrHomeStarting", err)
	}
	h := r.reconcile(t, who)
	if !h.Status.Prepared || !meta.IsStatusConditionTrue(h.Status.Conditions, model.ConditionReady) {
		t.Fatalf("home of %s = %+v, want prepared and ready", who, h.Status)
	}
	return h
}

// serving waits until who's helper answers and returns its address with
// its login. It waits for the executor to report a change, not for time
// to pass.
func (r *rig) serving(t *testing.T, who string) string {
	t.Helper()
	ctx := homesContext(t)
	for {
		addr, err := r.pods.Ensure(ctx, who)
		if err == nil {
			return addr
		}
		if !errors.Is(err, files.ErrHomeStarting) {
			t.Fatalf("Ensure(%s) = %v", who, err)
		}
		select {
		case <-r.changed:
		case <-ctx.Done():
			st, _ := r.exec.Observe(context.Background(), r.helperRef(t, who))
			t.Fatalf("the helper of %s never answered; its state: %+v", who, st)
		}
	}
}

// helperRef is the workload that is who's file helper, as the executor
// lists it.
func (r *rig) helperRef(t *testing.T, who string) workload.Ref {
	t.Helper()
	refs, err := r.exec.List(homesContext(t))
	must(t, err)
	i := slices.IndexFunc(refs, func(ref workload.Ref) bool { return ref.Owner == who && ref.Namespace == r.ns })
	if i < 0 {
		t.Fatalf("the executor runs no helper of %s: %v", who, refs)
	}
	return refs[i]
}

// probe runs script in a container of a workload of its own, called
// name, as uid, with the volume (when not empty) mounted at /home/user,
// and returns its exit code and what it printed. during runs while the
// probe's sandbox is up, with the sandbox's address on its network.
func (r *rig) probe(t *testing.T, name string, uid int64, volume, script string) (int, string) {
	t.Helper()
	ctx := homesContext(t)
	s := r.spec(name, uid)
	u := workload.Unit{Name: "probe", Image: docker.TestImage, Command: []string{"sh", "-c", script}}
	if volume != "" {
		s.Volumes = []workload.Volume{{Name: podstorage.HomeVolume, Source: volume}}
		u.Mounts = []workload.Mount{{Volume: podstorage.HomeVolume, Path: podstorage.HomeDir}}
	}
	defer func() { must(t, docker.Teardown(context.WithoutCancel(ctx), r.apps, s.Ref, r.gw)) }()
	must(t, docker.Isolate(ctx, r.apps, s.Ref, r.gw, homesPool))
	box, err := docker.SandboxBody(s, docker.TestPause, "")
	must(t, err)
	sandbox, _ := docker.SandboxName(s.Ref)
	id, err := r.apps.ContainerCreate(ctx, sandbox, box)
	must(t, err)
	must(t, r.apps.ContainerStart(ctx, id))
	must(t, docker.Prepare(ctx, r.apps, s, docker.TestImage))
	body, err := docker.ContainerBody(s, u, docker.Inputs{})
	must(t, err)
	unit, _ := docker.ContainerName(s.Ref, u.Name)
	id, err = r.apps.ContainerCreate(ctx, unit, body)
	must(t, err)
	must(t, r.apps.ContainerStart(ctx, id))
	code, err := r.apps.ContainerWait(ctx, id)
	must(t, err)
	rc, err := r.apps.ContainerLogs(ctx, id, docker.LogOptions{})
	must(t, err)
	defer rc.Close()
	out, err := io.ReadAll(rc)
	must(t, err)
	return code, string(out)
}

func names(l files.Listing) []string {
	out := []string{}
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	slices.Sort(out)
	return out
}

// archive is a gzipped tar of files by path.
func archive(t *testing.T, tree map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for name, body := range tree {
		must(t, w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(body))}))
		_, err := w.Write([]byte(body))
		must(t, err)
	}
	must(t, w.Close())
	must(t, gz.Close())
	return &buf
}

// A home asked for is a named volume labelled as that person's, whose
// root belongs to their uid and is closed to every other uid.
func TestDaemonHomeIsAVolumeOwnedByThePerson(t *testing.T) {
	r := newRig(t)
	ctx := homesContext(t)
	h := r.home(t, alice)
	want, err := dockerhome.Name(r.ns, alice)
	must(t, err)
	if h.Status.ClaimName != want || h.Status.SizeEnforced {
		t.Fatalf("status = %+v, want the volume %s with no size enforced", h.Status, want)
	}
	vol, err := r.all.VolumeInspect(ctx, want)
	must(t, err)
	if vol.Driver != "local" || len(vol.Options) != 0 {
		t.Errorf("volume = %+v, want a plain local volume and no path of the host", vol)
	}
	if !docker.Ours(vol.Labels) || vol.Labels[docker.LabelOwner] != alice || vol.Labels[docker.LabelNamespace] != r.ns || vol.Labels[dockerhome.LabelStorage] != dockerhome.Home {
		t.Errorf("labels = %v", vol.Labels)
	}

	code, out := r.probe(t, "owner", aliceUID, want, `stat -c '%u:%g:%a' /home/user && echo hello > /home/user/mine && cat /home/user/mine`)
	if code != 0 || !strings.Contains(out, "20001:100:700") || !strings.Contains(out, "hello") {
		t.Errorf("as the owner: exit %d, %q; want a root of 20001:100 with mode 700 that the owner writes", code, out)
	}
	// Even mounted (which nothing of Javo's does), it is closed to another
	// uid.
	code, out = r.probe(t, "other", bobUID, want, `ls /home/user; echo ls=$?; cat /home/user/mine; echo cat=$?; touch /home/user/x; echo touch=$?`)
	for _, w := range []string{"ls=", "cat=", "touch="} {
		if strings.Contains(out, w+"0") {
			t.Errorf("as another uid: %q; want %s to fail (exit %d)", out, w, code)
		}
	}
	if strings.Contains(out, "hello") {
		t.Errorf("another uid read the owner's file: %q", out)
	}

	// A second look at the home hands nothing over again and keeps the files.
	r.reconcile(t, alice)
	if code, out := r.probe(t, "again", aliceUID, want, `cat /home/user/mine`); code != 0 || !strings.Contains(out, "hello") {
		t.Errorf("after a second pass: exit %d, %q", code, out)
	}
}

// The gateway lists, saves, opens, unpacks and replaces files of a home
// through the person's helper, with the real rclone, and every file is
// the person's own.
func TestDaemonFileHelperServesTheHome(t *testing.T) {
	r := newRig(t)
	ctx := homesContext(t)
	h := r.home(t, alice)
	addr := r.serving(t, alice)

	saved, err := r.lister.Save(ctx, alice, "notes.txt", []byte("hello\n"), "")
	must(t, err)
	if _, err := r.lister.Save(ctx, alice, "notes.txt", []byte("hello again\n"), saved.Version()); err != nil {
		t.Fatalf("save of the version read: %v", err)
	}
	if _, err := r.lister.Save(ctx, alice, "notes.txt", []byte("stale\n"), saved.Version()); !errors.Is(err, files.ErrChanged) {
		t.Fatalf("save of an old version = %v, want ErrChanged", err)
	}
	if _, err := r.lister.Save(ctx, alice, "nowhere/plan.md", []byte("# plan\n"), ""); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("save into a folder that is not there = %v, want ErrNotFound", err)
	}
	rc, info, err := r.lister.Open(ctx, alice, "notes.txt")
	must(t, err)
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || string(got) != "hello again\n" || info.Size != int64(len(got)) {
		t.Fatalf("opened %q (%+v), %v", got, info, err)
	}
	if _, _, err := r.lister.Open(ctx, alice, "nothing.txt"); !errors.Is(err, files.ErrNotFound) {
		t.Errorf("open of a missing file = %v", err)
	}

	up, err := r.lister.Unpack(ctx, alice, "project", archive(t, map[string]string{"a.txt": "one", "sub/b.txt": "two"}), files.UploadLimits{Bytes: 1 << 20, Entries: 100}, false)
	if err != nil || up.Files != 2 || up.Replaced {
		t.Fatalf("Unpack = %+v, %v", up, err)
	}
	// Uploaded again, the folder is replaced whole: the old one is moved
	// aside and purged.
	up, err = r.lister.Unpack(ctx, alice, "project", archive(t, map[string]string{"c.txt": "three"}), files.UploadLimits{Bytes: 1 << 20, Entries: 100}, true)
	if err != nil || up.Files != 1 || !up.Replaced {
		t.Fatalf("second Unpack = %+v, %v", up, err)
	}
	if _, err := r.lister.Save(ctx, alice, "project/plan.md", []byte("# plan\n"), ""); err != nil {
		t.Fatalf("save into the uploaded folder: %v", err)
	}
	l, err := r.lister.List(ctx, alice, "project")
	if err != nil || !slices.Equal(names(l), []string{"c.txt", "plan.md"}) {
		t.Fatalf("project holds %v, %v", names(l), err)
	}
	l, err = r.lister.List(ctx, alice, "")
	if err != nil || !slices.Equal(names(l), []string{"notes.txt", "project"}) {
		t.Fatalf("home holds %v, %v; want no staging or old folder left", names(l), err)
	}

	// What the helper wrote is the owner's, as their apps find it.
	code, out := r.probe(t, "reader", aliceUID, h.Status.ClaimName, `cd /home/user && cat notes.txt project/plan.md project/c.txt && stat -c '%u:%g' notes.txt project/c.txt project`)
	if code != 0 || !strings.Contains(out, "hello again") || !strings.Contains(out, "# plan") || !strings.Contains(out, "three") || strings.Count(out, "20001:100") != 3 {
		t.Errorf("as the owner's app: exit %d, %q", code, out)
	}

	// The helper's password is its own and is nowhere a workload, a log
	// or a label shows it.
	u, err := url.Parse("http://" + addr)
	must(t, err)
	pass, _ := u.User.Password()
	if len(pass) < 43 {
		t.Fatalf("password of %d characters", len(pass))
	}
	ref := r.helperRef(t, alice)
	containers, err := r.all.ContainerList(ctx, docker.Selector(ref)...)
	must(t, err)
	if len(containers) != 2 {
		t.Errorf("the helper is %d containers, want its sandbox and rclone", len(containers))
	}
	for _, c := range containers {
		for k, v := range c.Labels {
			if strings.Contains(v, pass) {
				t.Errorf("label %s of %v holds the password", k, c.Names)
			}
		}
		rc, err := r.all.ContainerLogs(ctx, c.ID, docker.LogOptions{})
		must(t, err)
		printed, _ := io.ReadAll(rc)
		rc.Close()
		if bytes.Contains(printed, []byte(pass)) {
			t.Errorf("%v printed the password", c.Names)
		}
	}
	// Without the password rclone answers nothing.
	open := files.Rclone{HTTP: &http.Client{Transport: &http.Transport{DialContext: docker.DialThrough(r.all, r.gw), DisableKeepAlives: true}}}
	if _, err := open.List(ctx, u.Host, podstorage.HomeDir, ""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a call without the login = %v, want it refused", err)
	}
	if _, err := open.List(ctx, "javo:wrong@"+u.Host, podstorage.HomeDir, ""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("a call with a wrong password = %v, want it refused", err)
	}
}

// A link in a home that leads out of it (to the helper's own files, or
// above the home) is never listed, opened or written through.
func TestDaemonFileHelperServesNoLinkOutOfTheHome(t *testing.T) {
	r := newRig(t)
	ctx := homesContext(t)
	h := r.home(t, alice)
	code, out := r.probe(t, "linker", aliceUID, h.Status.ClaimName,
		`cd /home/user && echo real > real.txt && mkdir dir && ln -s /etc/passwd pw && ln -s .. up && ln -s /etc etc && ln -s /etc dir/etc && ln -s /proc/self/environ env`)
	if code != 0 {
		t.Fatalf("making the links: exit %d, %q", code, out)
	}
	r.serving(t, alice)

	l, err := r.lister.List(ctx, alice, "")
	if err != nil || !slices.Equal(names(l), []string{"dir", "real.txt"}) {
		t.Fatalf("home lists %v, %v; want no link", names(l), err)
	}
	for _, rel := range []string{"pw", "env", "etc/passwd", "up/user/real.txt", "dir/etc/passwd"} {
		rc, _, err := r.lister.Open(ctx, alice, rel)
		if !errors.Is(err, files.ErrNotFound) {
			t.Errorf("Open(%s) = %v, want ErrNotFound", rel, err)
		}
		if rc != nil {
			rc.Close()
		}
	}
	for _, rel := range []string{"up", "etc", "dir/etc", "pw"} {
		if l, err := r.lister.List(ctx, alice, rel); !errors.Is(err, files.ErrNotFound) {
			t.Errorf("List(%s) = %v, %v; want ErrNotFound", rel, names(l), err)
		}
	}
	for _, rel := range []string{"pw", "etc/x.txt", "up/x.txt", "dir/etc/x.txt"} {
		if _, err := r.lister.Save(ctx, alice, rel, []byte("x\n"), ""); err == nil {
			t.Errorf("Save(%s) wrote through a link", rel)
		}
	}
	if _, err := r.lister.Unpack(ctx, alice, "etc/new", archive(t, map[string]string{"a": "b"}), files.UploadLimits{Bytes: 1 << 10, Entries: 10}, false); err == nil {
		t.Error("Unpack wrote through a link")
	}
	// The real file beside them is served.
	rc, _, err := r.lister.Open(ctx, alice, "real.txt")
	must(t, err)
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "real\n" {
		t.Errorf("real.txt = %q", got)
	}
}

// One person's helper holds only that person's home: another person's
// files are not in it, and their volume is not mounted.
func TestDaemonFileHelperHoldsOnlyItsOwnersHome(t *testing.T) {
	r := newRig(t)
	ctx := homesContext(t)
	r.home(t, alice)
	r.home(t, bob)
	r.serving(t, alice)
	r.serving(t, bob)
	_, err := r.lister.Save(ctx, alice, "secret.txt", []byte("alice's\n"), "")
	must(t, err)

	if l, err := r.lister.List(ctx, bob, ""); err != nil || len(l.Entries) != 0 {
		t.Errorf("bob's home lists %v, %v; want it empty", names(l), err)
	}
	if rc, _, err := r.lister.Open(ctx, bob, "secret.txt"); !errors.Is(err, files.ErrNotFound) {
		if rc != nil {
			rc.Close()
		}
		t.Errorf("bob opening alice's file = %v, want ErrNotFound", err)
	}
	// Inside bob's helper there is nothing of alice's anywhere; the same
	// search finds the file in alice's own.
	search := []string{"sh", "-c", `find / -path /proc -prune -o -path /sys -prune -o -name secret.txt -print 2>/dev/null | grep -q .`}
	for who, want := range map[string]int{alice: 0, bob: 1} {
		unit, err := docker.ContainerName(r.helperRef(t, who), "rclone")
		must(t, err)
		if code, err := r.all.Exec(ctx, unit, search); err != nil || code != want {
			t.Errorf("the search for alice's file in the helper of %s: exit %d, %v; want %d", who, code, err, want)
		}
	}
	// And bob's helper runs as bob, alice's as alice.
	for who, uid := range map[string]string{alice: "20001", bob: "20002"} {
		unit, err := docker.ContainerName(r.helperRef(t, who), "rclone")
		must(t, err)
		if code, err := r.all.Exec(ctx, unit, []string{"sh", "-c", `[ "$(id -u):$(id -g)" = "` + uid + `:100" ] && ! touch /etc/x 2>/dev/null && ! touch /x 2>/dev/null`}); err != nil || code != 0 {
			t.Errorf("the helper of %s does not run as %s:100 on a root it cannot write (exit %d, %v)", who, uid, code, err)
		}
	}
	// A home's row that names another person's volume starts nothing.
	bobs, err := r.db.Homes().ByOwner(ctx, r.ns, bob)
	must(t, err)
	must(t, r.pods.Release(ctx, bob))
	bobs.Status.ClaimName, err = dockerhome.Name(r.ns, alice)
	must(t, err)
	_, err = r.db.Homes().Update(ctx, bobs, bobs.Version)
	must(t, err)
	if _, err := r.pods.Ensure(ctx, bob); !errors.Is(err, files.ErrHomeConflict) {
		t.Errorf("Ensure for a row that names alice's volume = %v, want ErrHomeConflict", err)
	}
	if st, err := r.exec.Observe(ctx, workload.Ref{Namespace: r.ns, Name: "files--" + bob}); err != nil || st != nil {
		t.Errorf("a helper was started on alice's volume: %+v, %v", st, err)
	}
}

// The helper is on a network of its own: it reaches nothing outside, no
// other workload reaches it, and only the gateway does.
func TestDaemonFileHelperIsIsolated(t *testing.T) {
	r := newRig(t)
	ctx := homesContext(t)
	r.home(t, alice)
	r.serving(t, alice)
	ref := r.helperRef(t, alice)
	network, err := docker.NetworkName(ref)
	must(t, err)
	sandbox, err := docker.SandboxName(ref)
	must(t, err)
	unit, err := docker.ContainerName(ref, "rclone")
	must(t, err)

	n, err := r.all.NetworkInspect(ctx, network)
	must(t, err)
	if !n.Internal || len(n.Containers) != 2 {
		t.Errorf("network internal %v with %d containers; want an internal network of the helper and the gateway only", n.Internal, len(n.Containers))
	}
	box, err := r.all.ContainerInspect(ctx, sandbox)
	must(t, err)
	ip := box.IP(network)
	if ip == "" || len(box.NetworkSettings.Networks) != 1 {
		t.Fatalf("the helper is on %d networks, address %q", len(box.NetworkSettings.Networks), ip)
	}

	// The gateway reaches it.
	if code, err := r.all.Exec(ctx, r.gw, []string{"nc", "-z", "-w", "2", ip, "5572"}); err != nil || code != 0 {
		t.Fatalf("the gateway cannot reach the helper (exit %d, %v)", code, err)
	}
	// Another workload, on its own network with the same gateway, does
	// not: by address or by name.
	_, out := r.probe(t, "neighbour", bobUID, "", `nc -z -w 2 `+ip+` 5572; echo ip=$?; nc -z -w 2 `+sandbox+` 5572; echo name=$?; wget -q -T 2 -O - http://`+ip+`:5572/ ; echo http=$?`)
	for _, w := range []string{"ip=", "name=", "http="} {
		if !strings.Contains(out, w) || strings.Contains(out, w+"0") {
			t.Errorf("from another workload: %q; want %s to fail", out, w)
		}
	}

	// The helper reaches the gateway on its own network, which shows
	// that its nc works, and nothing else: not the gateway's address on
	// the daemon's default network, not the host, not the internet.
	gw, err := r.all.ContainerInspect(ctx, r.gw)
	must(t, err)
	inside, bridge := gw.IP(network), gw.IP("bridge")
	if inside == "" || bridge == "" {
		t.Fatalf("the gateway's addresses: %q on the helper's network, %q on the default one", inside, bridge)
	}
	connects := func(from, target string) bool {
		t.Helper()
		code, err := r.all.Exec(ctx, from, []string{"sh", "-c", "nc -z -w 3 " + target})
		must(t, err)
		return code == 0
	}
	if !connects(unit, inside+" 9090") {
		t.Fatal("the helper cannot reach the gateway on its own network")
	}
	if !connects(unit, "127.0.0.1 5572") {
		t.Fatal("the helper cannot reach its own port")
	}
	outside := []string{bridge + " 9090"}
	if g := strings.Split(bridge, "."); len(g) == 4 {
		// The host's address on the default network.
		outside = append(outside, strings.Join(append(g[:3], "1"), ".")+" 22")
	}
	if connects(r.gw, "1.1.1.1 443") {
		outside = append(outside, "1.1.1.1 443")
	} else {
		t.Log("this machine has no way to the internet: not checked from the helper")
	}
	for _, target := range outside {
		if connects(unit, target) {
			t.Errorf("the helper connected to %s", target)
		}
	}
	// Its routes are its own network's and no default one.
	if code, err := r.all.Exec(ctx, unit, []string{"sh", "-c", "ip route | grep -q . && ! ip route | grep -q '^default'"}); err != nil || code != 0 {
		t.Errorf("the helper's routes are not its network's alone (exit %d, %v)", code, err)
	}
}

// A helper nobody uses is stopped, and Release stops one at once. All of
// it goes (containers, network, its own volumes) and the home stays.
func TestDaemonFileHelperIsReapedWhenIdleAndReleased(t *testing.T) {
	r := newRig(t)
	ctx := homesContext(t)
	h := r.home(t, alice)
	r.serving(t, alice)
	_, err := r.lister.Save(ctx, alice, "kept.txt", []byte("kept\n"), "")
	must(t, err)
	ref := r.helperRef(t, alice)
	network, _ := docker.NetworkName(ref)
	gone := func(when string) {
		t.Helper()
		containers, err := r.all.ContainerList(ctx, docker.Selector(ref)...)
		must(t, err)
		volumes, err := r.all.VolumeList(ctx, docker.Selector(ref)...)
		must(t, err)
		_, err = r.all.NetworkInspect(ctx, network)
		if len(containers) != 0 || len(volumes) != 0 || !errors.Is(err, docker.ErrNotFound) {
			t.Fatalf("%s: %d containers, %d volumes, network %v; want all of the helper gone", when, len(containers), len(volumes), err)
		}
		if _, err := r.all.VolumeInspect(ctx, h.Status.ClaimName); err != nil {
			t.Fatalf("%s: the home volume: %v", when, err)
		}
	}

	// Used a moment ago, it stays.
	r.now = r.now.Add(9 * time.Minute)
	r.lister.Touch(alice)
	r.now = r.now.Add(9 * time.Minute)
	must(t, r.pods.Reap(ctx))
	if st, err := r.exec.Observe(ctx, ref); err != nil || st == nil || st.Phase != "Running" {
		t.Fatalf("a helper in use was stopped: %+v, %v", st, err)
	}
	// Idle for long enough, it goes.
	r.now = r.now.Add(2 * time.Minute)
	must(t, r.pods.Reap(ctx))
	gone("after the reap")

	// The next look at the files starts a new one, with a new password,
	// on the same home.
	r.serving(t, alice)
	rc, _, err := r.lister.Open(ctx, alice, "kept.txt")
	must(t, err)
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "kept\n" {
		t.Errorf("kept.txt = %q after the helper was replaced", got)
	}
	must(t, r.lister.Release(ctx, alice))
	gone("after Release")
	if code, out := r.probe(t, "after", aliceUID, h.Status.ClaimName, `cat /home/user/kept.txt`); code != 0 || !strings.Contains(out, "kept") {
		t.Errorf("the home after its helper went: exit %d, %q", code, out)
	}
}
