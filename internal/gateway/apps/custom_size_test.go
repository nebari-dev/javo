package apps_test

import (
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Each person sees the sizes they may use and their own custom allowance,
// capped at the largest machine (8 CPU, about 15.6 GB here).
func TestSizesListIsPerPerson(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode())
	machineMem := resource.MustParse("16365140Ki")

	member := f.SizesFor(t, "alice-aaaaaaaa")
	if got := apitest.SizeNames(member); len(got) != 2 || got[0] != "small" || got[1] != "medium" {
		t.Errorf("member sizes = %v", got)
	}
	if member.Custom.Allowed {
		t.Errorf("member has a custom allowance: %+v", member.Custom)
	}

	ds := f.SizesFor(t, "bob-bbbbbbbb", "ds")
	if got := apitest.SizeNames(ds); len(got) != 3 || got[2] != "gpu" {
		t.Errorf("ds sizes = %v", got)
	}
	want := apitest.AllowanceJSON{Allowed: true, MaxCPU: 4, MaxMemory: machineMem.Value(), MinCPU: 0.1, MinMemory: 256 << 20, CPUStep: 0.1, MemoryStep: 256 << 20,
		Limit: "You can ask for up to 4 CPU and 15.6 GB.", Minimum: "Ask for at least 0.1 CPU and 256 MB."}
	if ds.Custom != want {
		t.Errorf("ds allowance = %+v, want %+v (16 GB capped by the machine)", ds.Custom, want)
	}

	admin := f.SizesFor(t, "root-rrrrrrrr", "javo-admins")
	if a := admin.Custom; !a.Allowed || a.MaxCPU != 8 || a.MaxMemory != machineMem.Value() {
		t.Errorf("admin allowance = %+v, want the largest machine", a)
	}
}

func TestCreateWithCustomAmount(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode(), gatewaytest.Template("lab", "jupyterlab"))
	admin := f.Session("root-rrrrrrrr", "javo-admins")

	w := f.Do("POST", "/api/workspaces", `{"displayName":"Custom","template":"lab","resources":{"cpu":2.5,"memory":6442450944}}`, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[struct {
		Size      string
		Resources *struct {
			CPU    float64 `json:"cpu"`
			Memory int64   `json:"memory"`
		}
	}](t, w)
	if got.Size != "" || got.Resources == nil || got.Resources.CPU != 2.5 || got.Resources.Memory != 6<<30 {
		t.Fatalf("view = %+v", got)
	}
	ws := f.GetWorkspace("root-rrrrrrrr", "custom")
	if ws.Size != "" || ws.Resources == nil ||
		!ws.Resources.CPU.Equal(resource.MustParse("2500m")) || !ws.Resources.Memory.Equal(resource.MustParse("6Gi")) {
		t.Fatalf("stored app = %+v", ws)
	}
}

// Every way a custom amount or a size can be refused, each with its own words.
func TestCustomAmountRefusals(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode(), gatewaytest.Template("lab", "jupyterlab"))
	create := func(body string, id string, groups ...string) (int, apitest.Problem) {
		w := f.Do("POST", "/api/workspaces", `{"displayName":"X","template":"lab",`+body+`}`, f.Session(id, groups...))
		if w.Code == http.StatusCreated {
			return w.Code, apitest.Problem{}
		}
		return w.Code, apitest.Decode[apitest.Problem](t, w)
	}
	for _, tc := range []struct {
		name, body, id string
		groups         []string
		status         int
		field, msg     string
	}{
		{"member without allowance", `"resources":{"cpu":1,"memory":1073741824}`, "alice-aaaaaaaa", nil,
			http.StatusForbidden, "", reasons.Msg(reasons.MsgCustomSizeNotAllowed)},
		{"over the group limit", `"resources":{"cpu":5,"memory":1073741824}`, "bob-bbbbbbbb", []string{"ds"},
			http.StatusUnprocessableEntity, "body.resources", "You can ask for up to 4 CPU and 16 GB."},
		{"bigger than every machine", `"resources":{"cpu":8,"memory":34359738368}`, "root-rrrrrrrr", []string{"javo-admins"},
			http.StatusUnprocessableEntity, "body.resources", apitest.TooBigOnSingleNode},
		{"too small", `"resources":{"cpu":0.05,"memory":1073741824}`, "root-rrrrrrrr", []string{"javo-admins"},
			http.StatusUnprocessableEntity, "body.resources", "Ask for at least 0.1 CPU and 256 MB."},
		{"size and amount", `"size":"small","resources":{"cpu":1,"memory":1073741824}`, "root-rrrrrrrr", []string{"javo-admins"},
			http.StatusUnprocessableEntity, "body.resources", reasons.Msg(reasons.MsgSizeOrCustom)},
		{"size of another group", `"size":"gpu"`, "alice-aaaaaaaa", nil,
			http.StatusUnprocessableEntity, "body.size", reasons.Msg(reasons.MsgSizeUnavailable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, p := create(tc.body, tc.id, tc.groups...)
			if code != tc.status {
				t.Fatalf("status %d: %+v", code, p)
			}
			if tc.field == "" {
				if p.Title != tc.msg {
					t.Fatalf("title %q, want %q", p.Title, tc.msg)
				}
				return
			}
			if msg, _ := p.FieldMessage(tc.field); msg != tc.msg {
				t.Fatalf("%s = %q, want %q (%+v)", tc.field, msg, tc.msg, p)
			}
		})
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("a refused request created an app")
	}
	if code, _ := create(`"size":"gpu"`, "bob-bbbbbbbb", "ds"); code != http.StatusCreated {
		t.Fatalf("ds member and the gpu size: status %d", code)
	}
}

// Change size moves an app between a named size and a custom amount.
func TestChangeToAndFromCustomAmount(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("sales-app", "root-rrrrrrrr", "Sales app")
	ws.Size = "small"
	f := apitest.New(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode(), ws, gatewaytest.Template("streamlit", "streamlit"))
	admin := f.Session("root-rrrrrrrr", "javo-admins")

	if w := f.Do("PUT", "/api/workspaces/sales-app/size", `{"resources":{"cpu":3,"memory":4294967296}}`, admin); w.Code != http.StatusOK {
		t.Fatalf("to custom: status %d: %s", w.Code, w.Body)
	}
	got := f.GetWorkspace("root-rrrrrrrr", "sales-app")
	if got.Size != "" || got.Resources == nil || !got.Resources.CPU.Equal(resource.MustParse("3")) {
		t.Fatalf("to custom: app %+v", got)
	}
	if w := f.Do("PUT", "/api/workspaces/sales-app/size", `{"size":"medium"}`, admin); w.Code != http.StatusOK {
		t.Fatalf("to medium: status %d: %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace("root-rrrrrrrr", "sales-app"); got.Size != "medium" || got.Resources != nil {
		t.Fatalf("to medium: app %+v", got)
	}
	w := f.Do("PUT", "/api/workspaces/sales-app/size", `{}`, admin)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("neither: status %d", w.Code)
	}
}
