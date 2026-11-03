package templates_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
	"github.com/nebari-dev/javo/internal/templates"
)

var ctx = context.Background()

func file(name, image string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(`apiVersion: javo.nebari.dev/v1alpha1
kind: WorkspaceTemplate
metadata:
  name: ` + name + `
spec:
  displayName: ` + name + `
  kind: streamlit
  image: ` + image + `
  port: 8501
  command: ["streamlit"]
`)}
}

func names(t *testing.T, s *store.Store) map[string]string {
	t.Helper()
	l, err := s.Templates().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, tpl := range l {
		out[tpl.Name] = tpl.Source
	}
	return out
}

func TestSyncInstallsTheFilesAsBuiltinTemplates(t *testing.T) {
	s := storetest.New(t)
	fsys := fstest.MapFS{"streamlit.yaml": file("streamlit", "example.org/streamlit:1")}
	if err := templates.Sync(ctx, s, fsys); err != nil {
		t.Fatal(err)
	}
	got, err := s.Templates().Get(ctx, "streamlit")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != store.SourceBuiltin || got.Spec.Image != "example.org/streamlit:1" || got.Spec.Port != 8501 {
		t.Fatalf("template = %+v", got)
	}
	// The defaults a saved template always has are applied.
	if got.Spec.BasePath != "Prefix" || got.Spec.MaxSharing != model.AccessPrivate {
		t.Errorf("defaults missing: %+v", got.Spec)
	}
}

func TestSyncTwiceChangesNothingAndANewImageIsPickedUp(t *testing.T) {
	s := storetest.New(t)
	fsys := fstest.MapFS{"streamlit.yaml": file("streamlit", "example.org/streamlit:1")}
	for range 2 {
		if err := templates.Sync(ctx, s, fsys); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Templates().Get(ctx, "streamlit")
	if got.Version != 1 {
		t.Fatalf("version after two identical syncs = %d, want 1", got.Version)
	}
	fsys["streamlit.yaml"] = file("streamlit", "example.org/streamlit:2")
	if err := templates.Sync(ctx, s, fsys); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Templates().Get(ctx, "streamlit")
	if got.Spec.Image != "example.org/streamlit:2" || got.Version != 2 {
		t.Fatalf("after the image changed: %+v", got)
	}
}

func TestSyncRemovesDisabledBuiltin(t *testing.T) {
	s := storetest.New(t)
	fsys := fstest.MapFS{"streamlit.yaml": file("streamlit", "i:1"), "panel.yaml": file("panel", "i:1")}
	if err := templates.Sync(ctx, s, fsys); err != nil {
		t.Fatal(err)
	}
	delete(fsys, "panel.yaml")
	if err := templates.Sync(ctx, s, fsys); err != nil {
		t.Fatal(err)
	}
	got := names(t, s)
	if _, ok := got["panel"]; ok || got["streamlit"] != store.SourceBuiltin {
		t.Fatalf("templates after disabling panel = %v", got)
	}
}

func TestSyncKeepsAdminTemplates(t *testing.T) {
	s := storetest.New(t)
	admin := model.Template{Name: "mine", Source: store.SourceAdmin, Spec: model.TemplateSpec{
		DisplayName: "Mine", Kind: "streamlit", Image: "example.org/mine:1", Port: 8501}}
	admin.Spec.ApplyDefaults()
	if _, err := s.Templates().Put(ctx, model.Actor{Person: "alice"}, admin, 0); err != nil {
		t.Fatal(err)
	}
	// A file that reuses an admin template's name does not replace it.
	fsys := fstest.MapFS{"mine.yaml": file("mine", "example.org/other:1"), "panel.yaml": file("panel", "i:1")}
	if err := templates.Sync(ctx, s, fsys); err != nil {
		t.Fatal(err)
	}
	delete(fsys, "mine.yaml")
	delete(fsys, "panel.yaml")
	if err := templates.Sync(ctx, s, fsys); err != nil {
		t.Fatal(err)
	}
	got, err := s.Templates().Get(ctx, "mine")
	if err != nil || got.Source != store.SourceAdmin || got.Spec.Image != "example.org/mine:1" || got.Version != 1 {
		t.Fatalf("admin template = %+v, %v", got, err)
	}
	if _, ok := names(t, s)["panel"]; ok {
		t.Fatal("builtin panel survived its file")
	}
}

func TestSyncRefusesABadFileBeforeChangingAnything(t *testing.T) {
	s := storetest.New(t)
	if err := templates.Sync(ctx, s, fstest.MapFS{"streamlit.yaml": file("streamlit", "i:1")}); err != nil {
		t.Fatal(err)
	}
	bad := file("panel", "i:1")
	bad.Data = []byte(strings.Replace(string(bad.Data), "kind: streamlit", "kind: nonsense", 1))
	err := templates.Sync(ctx, s, fstest.MapFS{"panel.yaml": bad})
	if err == nil || !strings.Contains(err.Error(), "panel.yaml") {
		t.Fatalf("err = %v, want one naming panel.yaml", err)
	}
	if got := names(t, s); len(got) != 1 || got["streamlit"] == "" {
		t.Fatalf("a refused sync changed the table: %v", got)
	}
}

func TestSyncRefusesAFileWithAnInvalidName(t *testing.T) {
	s := storetest.New(t)
	err := templates.Sync(ctx, s, fstest.MapFS{"streamlit.yaml": file("streamlit", "i:1"), "Panel.yaml": file("Panel", "i:1")})
	if err == nil || !strings.Contains(err.Error(), "Panel.yaml") {
		t.Fatalf("err = %v, want one naming Panel.yaml", err)
	}
	if got := names(t, s); len(got) != 0 {
		t.Fatalf("a refused sync changed the table: %v", got)
	}
}

func TestSyncRefusesAnUnknownField(t *testing.T) {
	s := storetest.New(t)
	f := file("panel", "i:1")
	f.Data = append(f.Data, []byte("  nonsenseField: 1\n")...)
	if err := templates.Sync(ctx, s, fstest.MapFS{"panel.yaml": f}); err == nil {
		t.Fatal("a misspelled field was accepted")
	}
}

// The templates the chart ships all load: the strict reader and the model
// validation accept every file, and each is named like its file.
func TestTheChartsTemplatesLoad(t *testing.T) {
	s := storetest.New(t)
	dir := os.DirFS("../../deploy/helm/javo/files/templates")
	if err := templates.Sync(ctx, s, dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("../../deploy/helm/javo/files/templates")
	if err != nil {
		t.Fatal(err)
	}
	// A second start changes nothing, also for templates with resource quantities.
	if err := templates.Sync(ctx, s, dir); err != nil {
		t.Fatal(err)
	}
	l, err := s.Templates().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range l {
		if tpl.Version != 1 {
			t.Errorf("%s was rewritten by the second sync (version %d)", tpl.Name, tpl.Version)
		}
		// Each shipped template names its own category; Other is for the rest.
		if tpl.Spec.Category == model.CategoryOther {
			t.Errorf("%s names no category", tpl.Name)
		}
	}
	got := names(t, s)
	if len(got) != len(entries) {
		t.Fatalf("%d files, %d templates", len(entries), len(got))
	}
	if _, err := s.Templates().Get(ctx, "jupyterlab"); errors.Is(err, store.ErrNotFound) {
		t.Fatal("jupyterlab is not installed")
	}
}

// Gateways that start together (a rollout) all run Sync against one database.
// Every one of them must succeed, and the table must end up as the files say.
func TestSyncRunsSafelyInParallel(t *testing.T) {
	s := storetest.New(t)
	for _, image := range []string{"example.org/streamlit:1", "example.org/streamlit:2"} {
		fsys := fstest.MapFS{"streamlit.yaml": file("streamlit", image), "panel.yaml": file("panel", image)}
		const replicas = 8
		errs := make(chan error, replicas)
		start := make(chan struct{})
		for range replicas {
			go func() {
				<-start
				errs <- templates.Sync(ctx, s, fsys)
			}()
		}
		close(start)
		for range replicas {
			if err := <-errs; err != nil {
				t.Fatalf("parallel sync with %s: %v", image, err)
			}
		}
		for _, name := range []string{"streamlit", "panel"} {
			got, err := s.Templates().Get(ctx, name)
			if err != nil || got.Spec.Image != image {
				t.Fatalf("%s after parallel sync = %+v, %v", name, got, err)
			}
		}
	}
}
