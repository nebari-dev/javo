package storage_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestStorageOnAKindLikeClusterIsOneMachineAndUnsized(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage(), apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.SizeEnforced = false }))
	v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "")
	if v.Mode != "perUser" || v.ModeLabel != reasons.Msg(reasons.MsgHomeLabelPerUser) {
		t.Errorf("mode %q label %q", v.Mode, v.ModeLabel)
	}
	// Nothing here limits the person or asks anything of them: only the
	// app page's note on unsaved folders.
	if !slices.Equal(v.Codes(), []string{string(reasons.ScratchPaths)}) {
		t.Errorf("notices = %v, want only %s", v.Codes(), reasons.ScratchPaths)
	}
	if v.SizeEnforced || v.Used != nil {
		t.Errorf("sizeEnforced %v used %v: an unenforced size shows no numbers", v.SizeEnforced, v.Used)
	}
	if v.Size != 20<<30 {
		t.Errorf("size = %d", v.Size)
	}
	if !slices.Equal(v.ScratchPaths, []string{"~/.cache", "/scratch"}) {
		t.Errorf("scratch paths %v", v.ScratchPaths)
	}
}

// Per-person disks on several machines is how the cluster works, not news:
// an app that cannot fit says so on its own page.
func TestStorageOnSeveralMachinesWithoutSharedStorageSaysNothing(t *testing.T) {
	t.Parallel()
	cs := apitest.ClusterStorage(func(cs *model.ClusterStorage) { cs.Status.Nodes, cs.Status.Choice.SingleNode = 3, false })
	f := apitest.New(t, apitest.Options{}, cs, apitest.HomeOf(apitest.AliceID))
	if codes := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "").Codes(); slices.Contains(codes, "ModePerUserMultiNode") {
		t.Fatalf("notices = %v, want no notice about sharing one machine", codes)
	}
}

func TestStorageOnSharedStorageSaysAppsRunAnywhere(t *testing.T) {
	t.Parallel()
	cs := apitest.ClusterStorage(func(cs *model.ClusterStorage) { cs.Status.Choice.Mode = model.HomeModeShared })
	h := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.Mode, h.Status.SizeEnforced = model.HomeModeShared, true })
	f := apitest.New(t, apitest.Options{}, cs, h)
	v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "")
	if v.ModeLabel != reasons.Msg(reasons.MsgHomeLabelShared) || !slices.Equal(v.Codes(), []string{string(reasons.ScratchPaths)}) {
		t.Fatalf("label %q notices %v", v.ModeLabel, v.Codes())
	}
}

func TestStorageBeforeTheFirstHomeUsesTheClustersChoice(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage())
	v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "")
	if v.Mode != "perUser" || v.ModeLabel == "" {
		t.Fatalf("got %+v", v)
	}
}

func TestStorageWithoutAnInspectedClusterStillAnswers(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	if w := f.Do("GET", "/api/storage", "", f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

func TestAScheduledMoveShowsItsDateFromTheMomentItIsSet(t *testing.T) {
	t.Parallel()
	at := metav1.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	cs := apitest.ClusterStorage(func(cs *model.ClusterStorage) {
		cs.Status.Choice.Mode = model.HomeModeShared
		cs.Spec.Migration = model.MigrationSpec{StartAt: &at, To: model.HomeModeShared}
	})
	f := apitest.New(t, apitest.Options{}, cs, apitest.HomeOf(apitest.AliceID)) // alice's home is still on a private disk
	v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "")
	var banner string
	for _, n := range v.Notices {
		if n.Code == string(reasons.MigrationScheduled) {
			banner = n.Text
		}
	}
	for _, want := range []string{"2 Nov 2026, 22:00 UTC", "shared storage", "minutes"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner %q does not say %q", banner, want)
		}
	}
	if !slices.Equal(v.Codes(), []string{string(reasons.MigrationScheduled), string(reasons.ScratchPaths)}) {
		t.Errorf("notices = %v, want the move and the app page note", v.Codes())
	}
}

func TestAScheduleForAnotherModeIsNotShown(t *testing.T) {
	t.Parallel()
	at := metav1.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	cs := apitest.ClusterStorage(func(cs *model.ClusterStorage) {
		cs.Spec.Migration = model.MigrationSpec{StartAt: &at, To: model.HomeModeShared} // cluster chose perUser since
	})
	f := apitest.New(t, apitest.Options{}, cs, apitest.HomeOf(apitest.AliceID))
	if v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), ""); slices.Contains(v.Codes(), string(reasons.MigrationScheduled)) {
		t.Fatalf("notices = %v", v.Codes())
	}
}

func TestARunningAndAFailedMoveHaveTheirOwnBanner(t *testing.T) {
	t.Parallel()
	for phase, want := range map[model.MigrationPhase]reasons.NoticeCode{
		model.MigrationWaiting: reasons.MigrationRunning,
		model.MigrationCopying: reasons.MigrationRunning,
		model.MigrationFailed:  reasons.NoticeMigrationFailed,
	} {
		h := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.Migration = &model.HomeMigration{Phase: phase} })
		f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage(), h)
		if v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), ""); !slices.Contains(v.Codes(), string(want)) {
			t.Errorf("phase %s: notices = %v, want %s", phase, v.Codes(), want)
		}
	}
}

func TestUsageIsFetchedOnlyWhenAskedForAndOnlyWhereTheSizeIsEnforced(t *testing.T) {
	t.Parallel()
	enforced := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.SizeEnforced = true })
	u := &apitest.StubUsage{Got: files.HomeUsage{Enforced: true, Used: 13 << 30, Total: 20 << 30}}
	f := apitest.New(t, apitest.Options{Usage: u}, apitest.ClusterStorage(), enforced)
	alice := f.Session(apitest.AliceID)

	if v := apitest.GetStorage(t, f, alice, ""); v.Used != nil || u.Calls != 0 {
		t.Fatalf("without ?usage=true a file pod is not started: used %v, calls %d", v.Used, u.Calls)
	}
	v := apitest.GetStorage(t, f, alice, "?usage=true")
	if !v.SizeEnforced || v.Used == nil || *v.Used != 13<<30 || v.Size != 20<<30 {
		t.Fatalf("got %+v", v)
	}
}

func TestUsageThatCannotBeReadYetIsLeftOut(t *testing.T) {
	t.Parallel()
	enforced := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.SizeEnforced = true })
	u := &apitest.StubUsage{Got: files.HomeUsage{}}
	f := apitest.New(t, apitest.Options{Usage: &apitest.FailingUsage{StubUsage: u, Err: files.ErrHomeStarting}}, apitest.ClusterStorage(), enforced)
	w := f.Do("GET", "/api/storage?usage=true", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusOK || apitest.Decode[apitest.StorageView](t, w).Used != nil {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}

func TestUsageIsReadInTheCallersNamespace(t *testing.T) {
	t.Parallel()
	ns := "javo-" + apitest.AliceID
	home := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.SizeEnforced = true })
	home.Namespace = ns
	u := &apitest.StubUsage{Got: files.HomeUsage{Enforced: true, Used: 1 << 30, Total: 20 << 30}}
	f := apitest.New(t, apitest.Options{Usage: u, Placement: placementperuser.New(config.Namespaces{Prefix: "javo"})},
		apitest.ClusterStorage(), home, apitest.ManagedNS(ns))
	if v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "?usage=true"); v.Used == nil || u.Ns != ns {
		t.Fatalf("used %v, usage asked in namespace %q, want %q", v.Used, u.Ns, ns)
	}
}

func TestUsageErrorsAreLoggedUnlessTheHomeIsJustStarting(t *testing.T) {
	enforced := apitest.HomeOf(apitest.AliceID, func(h *model.Home) { h.Status.SizeEnforced = true })
	for _, tc := range []struct {
		err    error
		logged bool
	}{
		{files.ErrHomeStarting, false},
		{files.ErrHomeBusy, false},
		{files.ErrHomeConflict, true},
		{errors.New("rclone down"), true},
	} {
		var buf bytes.Buffer
		old := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		f := apitest.New(t, apitest.Options{Usage: apitest.FailingUsage{StubUsage: &apitest.StubUsage{}, Err: tc.err}}, apitest.ClusterStorage(), enforced)
		w := f.Do("GET", "/api/storage?usage=true", "", f.Session(apitest.AliceID))
		slog.SetDefault(old)
		if w.Code != http.StatusOK || apitest.Decode[apitest.StorageView](t, w).Used != nil {
			t.Fatalf("%v: got %d %s", tc.err, w.Code, w.Body)
		}
		if got := strings.Contains(buf.String(), tc.err.Error()); got != tc.logged {
			t.Errorf("%v: logged = %v, want %v (%q)", tc.err, got, tc.logged, buf.String())
		}
	}
}

// Placement reads the pods of the one node, and only in the namespaces that
// hold apps: the gateway is not granted to read pods anywhere else, so a pod
// of another workload on the node is not counted (the figure is approximate).
func TestPlacementListsOnlyThePodsOfTheOwnersNodeInAppNamespaces(t *testing.T) {
	t.Parallel()
	var unscoped, clusterWide int
	funcs := &interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, l client.ObjectList, opts ...client.ListOption) error {
		if _, ok := l.(*corev1.PodList); ok {
			var o client.ListOptions
			o.ApplyOptions(opts)
			if o.Namespace == "" {
				clusterWide++
			}
			if o.FieldSelector == nil || o.FieldSelector.Empty() {
				unscoped++
			}
		}
		return c.List(ctx, l, opts...)
	}}
	system := apitest.AppPod("dns", "node-a", "", "", "2")
	system.Namespace, system.Labels = "kube-system", nil
	f := apitest.PlacementFixture(t, apitest.Options{Funcs: funcs}, append(apitest.PlacementObjs(model.HomeModePerUser, 2), system)...)
	unscoped, clusterWide = 0, 0 // the fixture's own setup is not the gateway's
	_, v := apitest.GetPlacement(t, f, "?template=small")
	if clusterWide != 0 {
		t.Errorf("%d pod lists across every namespace", clusterWide)
	}
	if unscoped > 1 {
		t.Errorf("%d pod lists beyond the owner's own and one node's", unscoped)
	}
	if !v.CoLocated || v.Node != "node-a" || v.FreeCPU != 5 {
		t.Fatalf("got %+v, want 5 CPUs free on node-a (the kube-system pod is not read)", v)
	}
}

// With one namespace per person, bob's pods in his namespace hold requests on
// the node; a pod in a namespace javo does not manage is not read.
func TestPlacementCountsEveryAppNamespaceWhenEachPersonHasOne(t *testing.T) {
	t.Parallel()
	aliceNS, bobNS := "javo-"+apitest.AliceID, "javo-"+apitest.BobID
	var objs []any
	for _, o := range apitest.PlacementObjs(model.HomeModePerUser, 2) {
		switch o := o.(type) {
		case *corev1.Pod:
			o.Namespace = aliceNS
			if o.Labels[model.LabelOwner] == apitest.BobID {
				o.Namespace = bobNS
			}
		case *model.Home:
			o.Namespace = aliceNS
		case *model.App:
			o.Namespace = aliceNS

		}
		objs = append(objs, o)
	}
	system := apitest.AppPod("dns", "node-a", "", "", "2")
	system.Namespace, system.Labels = "kube-system", nil
	objs = append(objs, system, apitest.ManagedNS(aliceNS), apitest.ManagedNS(bobNS))
	f := apitest.PlacementFixture(t, apitest.Options{Placement: placementperuser.New(config.Namespaces{Prefix: "javo"})}, objs...)
	_, v := apitest.GetPlacement(t, f, "?template=small")
	if !v.CoLocated || v.Node != "node-a" || v.FreeCPU != 5 || !slices.Equal(v.RunningApps, []string{"Forecast", "Sales"}) {
		t.Fatalf("got %+v, want 5 CPUs free (bob's pod counted, kube-system not read)", v)
	}
}

func TestStorageListsTheCallersGroupFoldersAndWhyOneIsNotReady(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Groups: apitest.StubGroups{"/org/data-science", "/org/ml"}}, apitest.ClusterStorage(), apitest.HomeOf(apitest.AliceID),
		apitest.Folder("/org/data-science", "ds", true, ""),
		apitest.Folder("/org/ml", "ml", false, reasons.GroupHasNoId),
		apitest.Folder("/org/finance", "fin", true, ""))
	v := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "")
	if len(v.Folders) != 2 {
		t.Fatalf("folders = %+v", v.Folders)
	}
	ds, ml := v.Folders[0], v.Folders[1] // ordered by directory: data-science, ml
	if ds.Group != "/org/data-science" || ds.Dir != "data-science" || !ds.Ready || ds.Reason != nil {
		t.Errorf("data-science = %+v", ds)
	}
	if ml.Dir != "ml" || ml.Ready || ml.Reason == nil || ml.Reason.Code != string(reasons.GroupHasNoId) {
		t.Errorf("ml = %+v", ml)
	}
}

// --- admin ---

func TestTheAdminReportIsRefusedToEveryoneElse(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage(apitest.HetznerLike))
	if w := f.Do("GET", "/api/admin/storage", "", f.Session(apitest.BobID)); w.Code != http.StatusForbidden {
		t.Errorf("GET /api/admin/storage as bob: %d, want 403", w.Code)
	}
}

func TestTheAdminReportListsFactsWithProvenanceAttentionFirst(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage(apitest.HetznerLike, func(cs *model.ClusterStorage) {
		cs.Status.Probe = &model.ProbeStatus{StorageClassName: "longhorn", Result: model.ProbeFailed, FailedStep: "check"}
	}))
	w := f.Do("GET", "/api/admin/storage", "", apitest.AdminSession(f))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	v := apitest.Decode[apitest.AdminView](t, w)
	if len(v.Rows) < 4 || v.Rows[0].Notice.Code != string(reasons.AdminProbeFailed) {
		t.Fatalf("rows = %+v", v.Rows)
	}
	seenOK := false
	for _, r := range v.Rows {
		if !r.Attention {
			seenOK = true
		} else if seenOK {
			t.Errorf("attention row %s after a row that needs none", r.Fact)
		}
		if r.Fact == "shared" && r.Provenance != "known" {
			t.Errorf("shared provenance = %q", r.Provenance)
		}
	}
}

// --- placement ---

func TestPlacementIgnoresTheOwnersPodsInOtherNamespaces(t *testing.T) {
	t.Parallel()
	objs := apitest.PlacementObjs(model.HomeModePerUser, 2)
	other := apitest.AppPod("p0", "node-b", apitest.AliceID, "elsewhere", "1")
	other.Namespace = "aaa-other-team"
	f := apitest.PlacementFixture(t, apitest.Options{}, append(objs, other)...)
	_, v := apitest.GetPlacement(t, f, "?template=small")
	if !v.CoLocated || v.Node != "node-a" || v.FreeCPU != 5 || !slices.Equal(v.RunningApps, []string{"Forecast", "Sales"}) {
		t.Fatalf("got %+v", v)
	}
}

func TestPlacementSaysWhereAnAppWouldRunNextToTheOthers(t *testing.T) {
	t.Parallel()
	f := apitest.PlacementFixture(t, apitest.Options{}, apitest.PlacementObjs(model.HomeModePerUser, 2)...)
	_, v := apitest.GetPlacement(t, f, "?template=small")
	if !v.CoLocated || v.Node != "node-a" || !v.Fits || v.FreeCPU != 5 {
		t.Fatalf("got %+v", v)
	}
	if !slices.Equal(v.RunningApps, []string{"Forecast", "Sales"}) {
		t.Errorf("running apps = %v, want display names", v.RunningApps)
	}
	if _, big := apitest.GetPlacement(t, f, "?template=huge"); big.Fits || !big.CoLocated {
		t.Fatalf("a 6 CPU template does not fit: %+v", big)
	}
}

func TestPlacementIsOnlyMeaningfulForPerUserHomesOnSeveralMachines(t *testing.T) {
	t.Parallel()
	for name, objs := range map[string][]any{
		"shared storage": apitest.PlacementObjs(model.HomeModeShared, 2),
		"one machine":    apitest.PlacementObjs(model.HomeModePerUser, 1),
	} {
		f := apitest.PlacementFixture(t, apitest.Options{}, objs...)
		if _, v := apitest.GetPlacement(t, f, "?template=small"); v.CoLocated || v.Node != "" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func TestPlacementUsesTheSizeWhenOneIsPicked(t *testing.T) {
	t.Parallel()
	// The size catalog is empty in this fixture, so an unknown size is refused.
	f := apitest.PlacementFixture(t, apitest.Options{}, apitest.PlacementObjs(model.HomeModePerUser, 2)...)
	if w := f.Do("GET", "/api/storage/placement?template=small&size=nope", "", f.Session(apitest.AliceID)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/storage/placement?template=nope", "", f.Session(apitest.AliceID)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown template: %d %s", w.Code, w.Body)
	}
}

func TestTheAdminReportStartsWithWhereFilesAre(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage())
	v := apitest.Decode[apitest.AdminView](t, f.Do("GET", "/api/admin/storage", "", apitest.AdminSession(f)))
	if !strings.HasPrefix(v.Summary, "Files are stored on this machine's disk") {
		t.Fatalf("summary = %q", v.Summary)
	}
}

// Storage is set when the platform is deployed; the page only reports it.
// Tests and moves run from the javo CLI.
func TestTheStoragePageCannotChangeStorage(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ClusterStorage(apitest.HetznerLike))
	for _, path := range []string{"/api/admin/storage/probe", "/api/admin/storage/migration", "/api/admin/storage/migration/retry"} {
		w := f.Do("POST", path, `{"storageClassName":"longhorn"}`, apitest.AdminSession(f))
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s as an admin: %d, want no such route", path, w.Code)
		}
	}
	if r := apitest.StoredRequests(t, f); r.Probe.Nonce != "" || r.Migration.StartAt != nil {
		t.Fatalf("the API changed the storage requests: %+v", r)
	}
}

func TestAScheduledMoveIsTheFirstNoticeEvenOnSeveralMachines(t *testing.T) {
	t.Parallel()
	at := metav1.Date(2026, 11, 2, 22, 0, 0, 0, time.UTC)
	cs := apitest.ClusterStorage(func(cs *model.ClusterStorage) {
		cs.Status.Nodes, cs.Status.Choice.SingleNode = 3, false
		cs.Status.Choice.Mode = model.HomeModeShared
		cs.Spec.Migration = model.MigrationSpec{StartAt: &at, To: model.HomeModeShared}
	})
	f := apitest.New(t, apitest.Options{}, cs, apitest.HomeOf(apitest.AliceID))
	codes := apitest.GetStorage(t, f, f.Session(apitest.AliceID), "").Codes()
	if len(codes) == 0 || codes[0] != string(reasons.MigrationScheduled) || slices.Contains(codes, "ModePerUserMultiNode") {
		t.Fatalf("notices = %v, want the move first and no notice about sharing one machine", codes)
	}
}

func TestPlacementCarriesTheCatalogNoticeForFitsAndWaits(t *testing.T) {
	t.Parallel()
	f := apitest.PlacementFixture(t, apitest.Options{}, apitest.PlacementObjs(model.HomeModePerUser, 2)...)
	_, fits := apitest.GetPlacement(t, f, "?template=small")
	if fits.Notice == nil || fits.Notice.Code != string(reasons.PlacementFits) || fits.Notice.Tone != reasons.ToneInfo {
		t.Fatalf("fits notice = %+v", fits.Notice)
	}
	for _, want := range []string{"2 apps", "node-a", "5 CPUs free", "fits"} {
		if !strings.Contains(fits.Notice.Text, want) {
			t.Errorf("text %q does not say %q", fits.Notice.Text, want)
		}
	}
	_, waits := apitest.GetPlacement(t, f, "?template=huge")
	if waits.Notice == nil || waits.Notice.Code != string(reasons.PlacementWaits) || waits.Notice.Tone != reasons.ToneWarn {
		t.Fatalf("waits notice = %+v", waits.Notice)
	}
}

func TestPlacementHasNoNoticeWhenAppsAreFreeToRunAnywhere(t *testing.T) {
	t.Parallel()
	f := apitest.PlacementFixture(t, apitest.Options{}, apitest.PlacementObjs(model.HomeModeShared, 2)...)
	if _, v := apitest.GetPlacement(t, f, "?template=small"); v.Notice != nil {
		t.Fatalf("notice = %+v", v.Notice)
	}
}

// A home the machines failed to mount reaches the admin page.
func TestTheAdminReportShowsHomesThatCannotBeMounted(t *testing.T) {
	t.Parallel()
	home := &model.Home{Namespace: gatewaytest.Namespace, Spec: model.HomeSpec{Owner: apitest.AliceID}, Status: model.HomeStatus{Conditions: []metav1.Condition{{
		Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(reasons.HomeMountFailed),
		Message: "FailedMount p: mount: bad option; you might need a /sbin/mount.<type> helper program.",
	}}}}
	f := apitest.New(t, apitest.Options{}, home, apitest.ClusterStorage(apitest.HetznerLike, func(cs *model.ClusterStorage) {
		cs.Status.Choice = &model.StorageChoice{Mode: model.HomeModeShared, HomeClass: "longhorn", SharedClass: "longhorn", Source: "auto"}
	}))
	v := apitest.Decode[apitest.AdminView](t, f.Do("GET", "/api/admin/storage", "", apitest.AdminSession(f)))
	if len(v.Rows) == 0 || v.Rows[0].Notice.Code != string(reasons.AdminSharedHomesNoNfsClient) {
		t.Fatalf("rows = %+v", v.Rows)
	}
}
