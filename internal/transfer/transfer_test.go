package transfer

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/model"
)

func plots() model.App {
	return model.App{
		ID: "id-1", Namespace: "javo-apps", Name: "plots", Owner: "alice-1", DisplayName: "Plots",
		Template: "streamlit", File: "plots/app.py", Size: "small", IdleStopMinutes: 60, Version: 4,
		Source:    &model.Source{URL: "https://github.com/example-org/plots", Ref: "main"},
		Sharing:   model.Sharing{Access: model.AccessRestricted, Users: []string{"bob-2"}},
		CreatedAt: time.Now(), LastActiveAt: time.Now(),
	}
}

// Export writes what a person chose for each app and nothing the install
// made: no id, owner, namespace, version, times or status.
func TestExportRoundTrip(t *testing.T) {
	tuned := plots()
	tuned.Name, tuned.DisplayName, tuned.Size, tuned.Paused = "tuned", "Tuned", "", true
	tuned.Resources = &model.AppResources{CPU: resource.MustParse("1500m"), Memory: resource.MustParse("3Gi")}
	out, err := Export([]model.App{plots(), tuned}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, leak := range []string{"id-1", "alice-1", "bob-2", "javo-apps", "version", "createdAt", "status", "lastActive", "sharing"} {
		if strings.Contains(text, leak) {
			t.Errorf("export contains %q:\n%s", leak, text)
		}
	}
	items, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Err != nil || items[1].Err != nil {
		t.Fatalf("items = %+v", items)
	}
	p := items[0].Doc.Spec
	if items[0].Doc.Metadata.Name != "plots" || p.DisplayName != "Plots" || p.Template != "streamlit" || p.File != "plots/app.py" ||
		p.Size != "small" || p.IdleStopMinutes != 60 || p.Source == nil || p.Source.URL != "https://github.com/example-org/plots" {
		t.Errorf("plots = %+v", items[0].Doc)
	}
	r := items[1].Doc.Spec
	if r.Resources == nil || r.Resources.CPU != "1500m" || r.Resources.Memory != "3Gi" || !r.Paused {
		t.Errorf("tuned = %+v", items[1].Doc)
	}
}

// A document with an unknown field or another kind is reported on its own;
// the other documents still parse.
func TestParseReportsBadDocumentsOneByOne(t *testing.T) {
	in := "apiVersion: javo.nebari.dev/v1alpha1\nkind: App\nmetadata: {name: a}\nspec: {displayName: A, template: t, imagee: x}\n" +
		"---\napiVersion: v1\nkind: Secret\nmetadata: {name: s}\n" +
		"---\napiVersion: javo.nebari.dev/v1alpha1\nkind: App\nmetadata: {name: b}\nspec: {displayName: B, template: t}\n"
	items, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].Err == nil || !strings.Contains(items[0].Err.Error(), "imagee") ||
		items[1].Err == nil || items[2].Err != nil || items[2].Doc.Spec.DisplayName != "B" {
		t.Fatalf("items = %+v", items)
	}
}

func TestParseRejectsTooManyDocuments(t *testing.T) {
	var b strings.Builder
	for i := range MaxDocuments + 1 {
		fmt.Fprintf(&b, "---\napiVersion: javo.nebari.dev/v1alpha1\nkind: App\nmetadata: {name: a%d}\nspec: {displayName: A, template: t}\n", i)
	}
	if _, err := Parse([]byte(b.String())); !errors.Is(err, ErrTooMany) {
		t.Fatalf("err = %v, want ErrTooMany", err)
	}
}

// A YAML alias bomb is refused quickly instead of expanding.
func TestParseRefusesAnAliasBomb(t *testing.T) {
	bomb := `apiVersion: javo.nebari.dev/v1alpha1
kind: App
metadata: {name: x}
spec:
  a: &a ["x","x","x","x","x","x","x","x","x"]
  b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
  c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
  d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
  e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
  f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
  g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
  h: &h [*g,*g,*g,*g,*g,*g,*g,*g,*g]
  i: &i [*h,*h,*h,*h,*h,*h,*h,*h,*h]
`
	start := time.Now()
	items, err := Parse([]byte(bomb))
	if time.Since(start) > time.Second {
		t.Fatalf("parsing took %v", time.Since(start))
	}
	if err == nil && (len(items) != 1 || items[0].Err == nil) {
		t.Fatalf("an alias bomb parsed: %+v", items)
	}
}

func TestToAppMakesTheCallerTheOwner(t *testing.T) {
	d := Doc{Spec: Spec{DisplayName: "Plots", Template: "streamlit", Resources: &Resources{CPU: "1", Memory: "2Gi"}}}
	a, err := d.ToApp()
	if err != nil || a.Owner != "" || a.DisplayName != "Plots" || a.Resources.CPU.String() != "1" {
		t.Fatalf("ToApp = %+v, %v", a, err)
	}
	d.Spec.Resources.CPU = "lots"
	if _, err := d.ToApp(); err == nil {
		t.Fatal("a CPU amount that does not parse was accepted")
	}
}

func TestParseRefusesBrokenYAML(t *testing.T) {
	if _, err := Parse([]byte("a: [unclosed")); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
}
