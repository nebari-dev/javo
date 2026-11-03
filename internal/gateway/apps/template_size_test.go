package apps_test

import (
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// webTemplate is a template whose sample needs cpu and mem.
func webTemplate(cpu, mem string) *model.Template {
	tpl := gatewaytest.Template("web", "custom")
	tpl.Spec.DefaultResources = &model.TemplateAmount{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}
	return tpl
}

// An app made without a size runs at its template's default amount: as a
// custom amount for a person who may ask for one, else at the smallest size
// that holds it, else at the default size. A size the person picks wins.
func TestCreateWithoutASizeUsesTheTemplatesDefault(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, cpu, mem string
		groups         []string
		body           string
		wantSize       string
		wantCPU        string
		wantMem        string
	}{
		{"custom amount", "250m", "512Mi", []string{"ds"}, ``, "", "250m", "512Mi"},
		{"no custom amounts: the smallest size that holds it", "2", "4Gi", nil, ``, "medium", "", ""},
		{"over the custom limit, and no size holds it: the default size", "5", "4Gi", []string{"ds"}, ``, "small", "", ""},
		{"too big for every size: the default size", "8", "32Gi", nil, ``, "small", "", ""},
		{"a picked size wins", "250m", "512Mi", []string{"ds"}, `,"size":"medium"`, "medium", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := apitest.New(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode(), webTemplate(c.cpu, c.mem))
			w := f.Do("POST", "/api/workspaces", `{"displayName":"Web","template":"web"`+c.body+`}`, f.Session("bob-bbbbbbbb", c.groups...))
			if w.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			ws := f.GetWorkspace("bob-bbbbbbbb", "web")
			if c.wantCPU == "" {
				if ws.Resources != nil || ws.Size != c.wantSize {
					t.Fatalf("stored size %q, resources %+v; want size %q", ws.Size, ws.Resources, c.wantSize)
				}
				return
			}
			if ws.Size != "" || ws.Resources == nil || !ws.Resources.CPU.Equal(resource.MustParse(c.wantCPU)) || !ws.Resources.Memory.Equal(resource.MustParse(c.wantMem)) {
				t.Fatalf("stored size %q, resources %+v", ws.Size, ws.Resources)
			}
		})
	}
}

// The templates list names each template's default amount, so New app can
// show it chosen.
func TestTemplatesShowTheDefaultResources(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, webTemplate("250m", "512Mi"))
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))
	got := apitest.Decode[struct {
		Items []struct {
			DefaultResources *struct {
				CPU    float64 `json:"cpu"`
				Memory int64   `json:"memory"`
			}
		}
	}](t, w).Items
	if len(got) != 1 || got[0].DefaultResources == nil || got[0].DefaultResources.CPU != 0.25 || got[0].DefaultResources.Memory != 512<<20 {
		t.Fatalf("templates: %s", w.Body)
	}
}
