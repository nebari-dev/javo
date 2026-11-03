package apps_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestListSizes(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)})
	w := f.Do("GET", "/api/sizes", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	type sizeJSON struct {
		Name, DisplayName string
		CPU               float64 `json:"cpu"`
		Memory            int64   `json:"memory"`
		Default           bool    `json:"default"`
	}
	got := apitest.Decode[struct{ Items []sizeJSON }](t, w).Items
	want := []sizeJSON{
		{Name: "small", DisplayName: "Small", CPU: 1, Memory: 2 << 30, Default: true},
		{Name: "medium", DisplayName: "Medium", CPU: 0.5, Memory: 8 << 30},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("sizes = %+v", got)
	}
}

// With no sizes configured the list is empty, not null.
func TestListSizesEmpty(t *testing.T) {
	f := apitest.New(t, apitest.Options{})
	w := f.Do("GET", "/api/sizes", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), `{"items":[],"custom":{"allowed":false,`) {
		t.Fatalf("status %d: %q", w.Code, w.Body)
	}
}

// An app created without a size is stored at the default size, so a later
// change of the default does not restart it.
func TestCreateStoresTheChosenOrDefaultSize(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, gatewaytest.Template("lab", "jupyterlab"))
	alice := f.Session("alice-aaaaaaaa")
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Plain","template":"lab"}`, alice)
	if w.Code != http.StatusCreated || apitest.Decode[apitest.WorkspaceJSON](t, w).Size != "small" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredSize("alice-aaaaaaaa", "plain"); got != "small" {
		t.Fatalf("stored size %q", got)
	}
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Big","template":"lab","size":"medium"}`, alice)
	if w.Code != http.StatusCreated || apitest.Decode[apitest.WorkspaceJSON](t, w).Size != "medium" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredSize("alice-aaaaaaaa", "big"); got != "medium" {
		t.Fatalf("stored size %q", got)
	}
}

func TestCreateWithUnknownSizeIs422AndCreatesNothing(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, gatewaytest.Template("lab", "jupyterlab"))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Huge","template":"lab","size":"huge"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.size"); !ok || msg != reasons.Msg(reasons.MsgSizeUnavailable) {
		t.Fatalf("errors %s", w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestStartSampleUsesTheDefaultSize(t *testing.T) {
	tpl := gatewaytest.Template("lab", "jupyterlab")
	tpl.Spec.Sample = &model.TemplateSample{Path: "/opt/sample", Title: "Tour"}
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, tpl)
	w := f.Do("POST", "/api/samples/lab", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredSize("alice-aaaaaaaa", apitest.Decode[apitest.WorkspaceJSON](t, w).Name); got != "small" {
		t.Fatalf("stored size %q", got)
	}
}

func TestChangeSize(t *testing.T) {
	ws := gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app")
	ws.Size = "small"
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, ws, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")

	w := f.Do("PUT", "/api/workspaces/sales-app/size", `{"size":"medium"}`, alice)
	if w.Code != http.StatusOK || apitest.Decode[apitest.WorkspaceJSON](t, w).Size != "medium" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredSize("alice-aaaaaaaa", "sales-app"); got != "medium" {
		t.Fatalf("stored size %q", got)
	}

	w = f.Do("PUT", "/api/workspaces/sales-app/size", `{"size":"huge"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.size"); !ok {
		t.Fatalf("errors %s", w.Body)
	}
	if got := f.StoredSize("alice-aaaaaaaa", "sales-app"); got != "medium" {
		t.Fatalf("unknown size stored: %q", got)
	}

	if w := f.Do("PUT", "/api/workspaces/sales-app/size", `{"size":"small"}`, f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound {
		t.Fatalf("other user's app: status %d", w.Code)
	}
}

// An app stored without a size (made before sizes existed) shows the
// default size it runs at.
func TestWorkspaceWithoutSizeShowsTheDefault(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app"))
	w := f.Do("GET", "/api/workspaces/sales-app", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK || apitest.Decode[apitest.WorkspaceJSON](t, w).Size != "small" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

// A running app whose spec changed (for example its size) is restarting
// until the controller catches up, so the page keeps polling.
func TestRunningAppWithUnobservedChangeShowsStarting(t *testing.T) {
	ws := gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app")
	ws.Status.Phase = "Running"
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, ws)
	// The controller saw the app as created; the size changes after that.
	app := f.GetWorkspace("alice-aaaaaaaa", "sales-app")
	if _, err := f.Db.Apps().UpdateSpec(context.Background(), gatewaytest.Actor, app.ID, app.Version, func(a *model.App) error {
		a.Size = "medium"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := f.Do("GET", "/api/workspaces/sales-app", "", f.Session("alice-aaaaaaaa"))
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w).Phase; got != "Starting" {
		t.Fatalf("phase %q, want Starting: %s", got, w.Body)
	}

	ws.Paused = true
	f = apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, ws)
	w = f.Do("GET", "/api/workspaces/sales-app", "", f.Session("alice-aaaaaaaa"))
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w).Phase; got != "Stopping" {
		t.Fatalf("stopping app: phase %q, want Stopping", got)
	}
}

// A stopped app whose pod is still going away reports Stopping, so the page
// shows the change at once and keeps polling until it is Stopped.
func TestPausedAppStillRunningShowsStopping(t *testing.T) {
	ws := gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app")
	ws.Paused, ws.Status.Phase = true, "Running"
	f := apitest.New(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, ws)
	w := f.Do("GET", "/api/workspaces/sales-app", "", f.Session("alice-aaaaaaaa"))
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w).Phase; got != "Stopping" {
		t.Fatalf("phase %q, want Stopping: %s", got, w.Body)
	}
}

// Each size says whether some machine can hold it, and why not when none can.
func TestListSizesMarksSizesNoMachineCanHold(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.FitCatalog(t, "small")}, apitest.SingleNode())
	got := apitest.ListFits(t, f)
	want := map[string]apitest.FitJSON{
		"small":  {Name: "small", Default: true, Fits: true},
		"medium": {Name: "medium", Fits: true},
		"large":  {Name: "large", Reason: apitest.TooBigOnSingleNode, Short: reasons.Msg(reasons.MsgSizeTooBigShort)},
		"burst":  {Name: "burst", Fits: true},
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %+v, want %+v", name, got[name], w)
		}
	}
}

// With no nodes to read, nothing is known and every size is offered.
func TestListSizesWithoutNodesOffersAll(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.FitCatalog(t, "small")})
	for name, s := range apitest.ListFits(t, f) {
		if !s.Fits || s.Reason != "" {
			t.Errorf("%s = %+v", name, s)
		}
	}
}

// A configured default no machine can hold gives way to the largest size
// that fits, in the list and for apps created without a size.
func TestDefaultThatDoesNotFitFallsBackToTheLargestThatFits(t *testing.T) {
	f := apitest.New(t, apitest.Options{Sizes: apitest.FitCatalog(t, "large")}, apitest.SingleNode(), gatewaytest.Template("lab", "jupyterlab"))
	got := apitest.ListFits(t, f)
	if got["large"].Default || !got["medium"].Default {
		t.Fatalf("default not moved to medium: %+v", got)
	}
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Plain","template":"lab"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if s := f.StoredSize("alice-aaaaaaaa", "plain"); s != "medium" {
		t.Fatalf("stored size %q, want medium", s)
	}
}

// Creating at, or changing to, a size no machine can hold is refused on the
// size field with the reason; a size marked allowUnschedulable is not.
func TestSizeNoMachineCanHoldIsRefused(t *testing.T) {
	ws := gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app")
	ws.Size = "small"
	f := apitest.New(t, apitest.Options{Sizes: apitest.FitCatalog(t, "small")}, apitest.SingleNode(), ws, gatewaytest.Template("lab", "jupyterlab"), gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")

	w := f.Do("POST", "/api/workspaces", `{"displayName":"Huge","template":"lab","size":"large"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create: status %d: %s", w.Code, w.Body)
	}
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.size"); msg != apitest.TooBigOnSingleNode {
		t.Fatalf("create: errors %s", w.Body)
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("created")
	}

	w = f.Do("PUT", "/api/workspaces/sales-app/size", `{"size":"large"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("change: status %d: %s", w.Code, w.Body)
	}
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.size"); msg != apitest.TooBigOnSingleNode {
		t.Fatalf("change: errors %s", w.Body)
	}
	if s := f.StoredSize("alice-aaaaaaaa", "sales-app"); s != "small" {
		t.Fatalf("stored size %q", s)
	}

	if w := f.Do("PUT", "/api/workspaces/sales-app/size", `{"size":"burst"}`, alice); w.Code != http.StatusOK {
		t.Fatalf("allowUnschedulable size: status %d: %s", w.Code, w.Body)
	}
}

// A GPU size lists its GPU count; it fits only a cluster with a machine that
// has the GPU and the labels, and is refused like any too-big size otherwise.
func TestGPUSizeListedAndFitChecked(t *testing.T) {
	type gpuJSON struct {
		Name   string `json:"name"`
		GPU    int64  `json:"gpu"`
		Fits   bool   `json:"fits"`
		Reason string `json:"reason"`
	}
	list := func(f *apitest.Fixture) map[string]gpuJSON {
		w := f.Do("GET", "/api/sizes", "", f.Session("alice-aaaaaaaa"))
		out := map[string]gpuJSON{}
		for _, s := range apitest.Decode[struct{ Items []gpuJSON }](t, w).Items {
			out[s.Name] = s
		}
		return out
	}

	gpuNode := apitest.SingleNode()
	gpuNode.Name, gpuNode.Labels = "gpu-1", map[string]string{"pool": "gpu"}
	gpuNode.Status.Allocatable["nvidia.com/gpu"] = resource.MustParse("1")
	got := list(apitest.New(t, apitest.Options{Sizes: apitest.GpuCatalog(t)}, apitest.SingleNode(), gpuNode))
	if got["gpu"] != (gpuJSON{Name: "gpu", GPU: 1, Fits: true}) || got["small"] != (gpuJSON{Name: "small", Fits: true}) {
		t.Fatalf("with a GPU node: %+v", got)
	}

	f := apitest.New(t, apitest.Options{Sizes: apitest.GpuCatalog(t)}, apitest.SingleNode(), gatewaytest.Template("lab", "jupyterlab"))
	noGPU := reasons.Msg(reasons.MsgSizeNoMachine, "4 CPU, 8 GB and 1 nvidia.com/gpu on a machine labelled pool=gpu")
	if g := list(f)["gpu"]; g.Fits || g.Reason != noGPU {
		t.Fatalf("without a GPU node: %+v", g)
	}
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Train","template":"lab","size":"gpu"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create: status %d: %s", w.Code, w.Body)
	}
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.size"); msg != noGPU {
		t.Fatalf("create: errors %s", w.Body)
	}
}
