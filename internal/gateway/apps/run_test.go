package apps_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

type runJSON struct {
	Image       string   `json:"image"`
	Command     []string `json:"command"`
	Line        string   `json:"line"`
	Environment string   `json:"environment"`
}

// An app's view says what its pod runs: the image and command line the
// controller builds it with (workspace.Platform.RunOf) and its environment.
func TestAnAppShowsTheImageAndCommandItRuns(t *testing.T) {
	t.Parallel()
	tpl := apitest.CustomTemplate()
	tpl.Spec.Image, tpl.Spec.Environment = "", "custom"
	tpl.Spec.Command = []string{"/opt/javo/pod-user.sh"}
	o := apitest.Options{PublicHost: "javo.example.com", PixiImage: "example.com/pixi:1"}
	f := apitest.New(t, o, tpl)
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Clock","template":"custom","command":"python 'my app.py' --port={{.Port}}"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	app := f.GetWorkspace("alice-aaaaaaaa", "clock")
	want, err := workspace.Platform{Exposure: exposurepath.New(config.URLs{}), PublicHost: o.PublicHost, PixiImage: o.PixiImage}.RunOf(app, tpl)
	if err != nil {
		t.Fatal(err)
	}
	got := apitest.Decode[struct{ Run runJSON }](t, f.Do("GET", "/api/workspaces/clock", "", f.Session("alice-aaaaaaaa"))).Run
	if got.Image != want.Image || !slices.Equal(got.Command, want.Command) {
		t.Fatalf("run = %+v, want %+v", got, want)
	}
	if got.Line != "/opt/javo/pod-user.sh python 'my app.py' --port=8501" {
		t.Errorf("line = %q", got.Line)
	}
	if got.Environment != "The template's environment custom" {
		t.Errorf("environment = %q", got.Environment)
	}
}

// The templates list shows each template's run before an app exists, with
// what the person picks as readable placeholders.
func TestTemplatesShowTheirRunWithPlaceholders(t *testing.T) {
	t.Parallel()
	st := gatewaytest.Template("st", "streamlit")
	st.Spec.Command = []string{"streamlit"}
	st.Spec.Args = []string{"run", "{{.File}}", "--server.baseUrlPath={{.BaseURL}}", "--server.port={{.Port}}"}
	f := apitest.New(t, apitest.Options{}, st, apitest.CustomTemplate(), apitest.ContainerTemplate())
	body := apitest.Decode[struct {
		Items []struct {
			Name string
			Run  *runJSON
		}
	}](t, f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa")))
	want := map[string]runJSON{
		"st":        {Image: "example.org/st:1", Line: "streamlit run /home/user/<your file> --server.baseUrlPath=/u/alice-aaaaaaaa/<app name>/ --server.port=8501"},
		"custom":    {Image: "example.org/custom:1", Line: "<your command>"},
		"container": {Image: "<your image>", Line: ""},
	}
	for _, it := range body.Items {
		w, ok := want[it.Name]
		if !ok {
			continue
		}
		if it.Run == nil || it.Run.Image != w.Image || it.Run.Line != w.Line {
			t.Errorf("%s: run = %+v, want image %q line %q", it.Name, it.Run, w.Image, w.Line)
		}
	}
}
