package apps_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestCreateWithATypedCommand(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate())
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Clock","template":"custom","command":"  python app.py --port={{.Port}}  "}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.LaunchJSON](t, w).Command; got != "python app.py --port={{.Port}}" {
		t.Fatalf("view command %q", got)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "clock").Command; got != "python app.py --port={{.Port}}" {
		t.Fatalf("stored command %q", got)
	}
}

func TestCreateRefusesABadCommandAndCreatesNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		template, command, want string
	}{
		"missing":     {"custom", "  ", reasons.Msg(reasons.MsgCommandRequired)},
		"too long":    {"custom", strings.Repeat("a", model.MaxCommandLength+1), reasons.Msg(reasons.MsgCommandTooLong)},
		"unclosed":    {"custom", `python "app.py`, "closing quote"},
		"pipe":        {"custom", `python app.py | tee log`, "|"},
		"variable":    {"custom", `python app.py --port=$PORT`, reasons.Msg(reasons.MsgCommandVariable)},
		"placeholder": {"custom", `python app.py --port={{.Prot}}`, reasons.Msg(reasons.MsgCommandPlaceholder)},
		"not taken":   {"lab", `python app.py`, reasons.Msg(reasons.MsgCommandNotTaken)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate(), gatewaytest.Template("lab", "jupyterlab"))
			body := `{"displayName":"Clock","template":"` + c.template + `","command":` + apitest.Quote(c.command) + `}`
			w := f.Do("POST", "/api/workspaces", body, f.Session("alice-aaaaaaaa"))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.command")
			if !ok || !strings.Contains(msg, c.want) {
				t.Fatalf("command message %q, want %q (%s)", msg, c.want, w.Body)
			}
			if f.WorkspaceCount() != 0 {
				t.Fatal("created")
			}
		})
	}
}

// The command of a syntax error says where the parser stopped.
func TestBadCommandMessageQuotesTheParser(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate())
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Clock","template":"custom","command":"python \"app.py"}`, f.Session("alice-aaaaaaaa"))
	msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.command")
	if !strings.HasPrefix(msg, strings.SplitN(reasons.Msg(reasons.MsgCommandInvalid, "X"), "X", 2)[0]) || !strings.Contains(msg, "1:8") {
		t.Fatalf("message %q", msg)
	}
}

// Both ways to start a sample give it the template's sample command.
func TestSampleRunsWithTheSampleCommand(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate())
	alice := f.Session("alice-aaaaaaaa")
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Demo","template":"custom","sample":true}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/samples/custom", "", alice); w.Code != http.StatusCreated {
		t.Fatalf("one-click: status %d: %s", w.Code, w.Body)
	}
	for _, short := range []string{"demo", "custom-sample"} {
		if got := f.GetWorkspace("alice-aaaaaaaa", short).Command; got != apitest.SampleCommand {
			t.Fatalf("%s: command %q", short, got)
		}
	}
	// A command typed with the sample is kept.
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Mine","template":"custom","sample":true,"command":"python sample.py"}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "mine").Command; got != "python sample.py" {
		t.Fatalf("command %q", got)
	}
}

// An image sample starts the sample's image, port and command, both from the
// one-click card and from New app; there are no sample files to copy.
func TestImageSampleRunsTheSampleImage(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ContainerSample())
	alice := f.Session("alice-aaaaaaaa")
	if w := f.Do("POST", "/api/samples/container", "", alice); w.Code != http.StatusCreated {
		t.Fatalf("one-click: status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Demo","template":"container","sample":true}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	for _, short := range []string{"container-sample", "demo"} {
		s := f.GetWorkspace("alice-aaaaaaaa", short)
		if s.Image != "example.com/toolbox:1" || s.Port != 9000 || s.Command != "serve --port={{.Port}}" || s.Sample || s.File != "" {
			t.Fatalf("%s: spec %+v", short, s)
		}
	}
	// What is typed with the sample is kept.
	body := `{"displayName":"Mine","template":"container","sample":true,"image":"example.com/other:2","port":7000,"command":"run"}`
	if w := f.Do("POST", "/api/workspaces", body, alice); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if s := f.GetWorkspace("alice-aaaaaaaa", "mine"); s.Image != "example.com/other:2" || s.Port != 7000 || s.Command != "run" {
		t.Fatalf("typed: spec %+v", s)
	}
}

// The sample image meets the same registry check as a typed one.
func TestImageSampleFromAForbiddenRegistryIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Images: config.ContainerImagesCfg{AllowedRegistries: []string{"registry.example.org"}}}, apitest.ContainerSample())
	w := f.Do("POST", "/api/samples/container", "", f.Session("alice-aaaaaaaa"))
	want := reasons.Msg(reasons.MsgImageRegistryNotAllowed, "example.com", "registry.example.org")
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.image"); w.Code != http.StatusUnprocessableEntity || msg != want {
		t.Fatalf("status %d, image message %q, want %q", w.Code, msg, want)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

// New app fills the image fields from an image sample, so the view names its
// image and the port it serves on (the template's when the sample names none).
func TestImageSampleViewNamesImageAndPort(t *testing.T) {
	t.Parallel()
	noPort := apitest.ContainerSample()
	noPort.Name = "box"
	noPort.Spec.Sample.Port = 0
	f := apitest.New(t, apitest.Options{}, apitest.ContainerSample(), noPort)
	type sampleJSON struct {
		Title, File, Image, Command string
		Port                        int32
	}
	got := map[string]*sampleJSON{}
	for _, it := range apitest.Decode[struct {
		Items []struct {
			Name   string
			Sample *sampleJSON
		}
	}](t, f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))).Items {
		got[it.Name] = it.Sample
	}
	want := sampleJSON{Title: "Toolbox", Image: "example.com/toolbox:1", Port: 9000, Command: "serve --port={{.Port}}"}
	if c := got["container"]; c == nil || *c != want {
		t.Fatalf("container sample = %+v, want %+v", c, want)
	}
	if b := got["box"]; b == nil || b.Port != 8080 {
		t.Fatalf("box sample = %+v, want the template's port 8080", b)
	}
}

func TestCreateFromAnImage(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.ContainerTemplate())
	alice := f.Session("alice-aaaaaaaa")
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Web","template":"container","image":" nginxinc/nginx-unprivileged:1.27-alpine "}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	v := apitest.Decode[apitest.LaunchJSON](t, w)
	if v.Image != "nginxinc/nginx-unprivileged:1.27-alpine" || v.Port != 8080 {
		t.Fatalf("view %+v", v)
	}
	// The default port is stored, so a later template change does not move the app.
	if s := f.GetWorkspace("alice-aaaaaaaa", "web"); s.Image != v.Image || s.Port != 8080 {
		t.Fatalf("stored %+v", s)
	}
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Api","template":"container","image":"ghcr.io/example/api:2","port":9000,"command":"--listen=:{{.Port}}"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if s := f.GetWorkspace("alice-aaaaaaaa", "api"); s.Port != 9000 || s.Command != "--listen=:{{.Port}}" {
		t.Fatalf("stored %+v", s)
	}
}

func TestCreateRefusesABadImageAndCreatesNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		images      config.ContainerImagesCfg
		body, field string
		want        string
	}{
		"missing":     {config.ContainerImagesCfg{}, `"template":"container"`, "body.image", reasons.Msg(reasons.MsgImageRequired)},
		"not a name":  {config.ContainerImagesCfg{}, `"template":"container","image":"My Image"`, "body.image", reasons.Msg(reasons.MsgImageInvalid)},
		"registry":    {config.ContainerImagesCfg{AllowedRegistries: []string{"ghcr.io/example", "registry.example.com"}}, `"template":"container","image":"nginx"`, "body.image", reasons.Msg(reasons.MsgImageRegistryNotAllowed, "docker.io", "ghcr.io/example, registry.example.com")},
		"disabled":    {config.ContainerImagesCfg{Enabled: new(bool)}, `"template":"container","image":"nginx"`, "body.template", reasons.Msg(reasons.MsgTemplateUnavailable)},
		"port range":  {config.ContainerImagesCfg{}, `"template":"container","image":"nginx","port":70000`, "body.port", reasons.Msg(reasons.MsgPortInvalid)},
		"image taken": {config.ContainerImagesCfg{}, `"template":"lab","image":"nginx"`, "body.image", reasons.Msg(reasons.MsgImageNotTaken)},
		"port taken":  {config.ContainerImagesCfg{}, `"template":"lab","port":9000`, "body.port", reasons.Msg(reasons.MsgPortNotTaken)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := apitest.New(t, apitest.Options{Images: c.images}, apitest.ContainerTemplate(), gatewaytest.Template("lab", "jupyterlab"))
			w := f.Do("POST", "/api/workspaces", `{"displayName":"Web",`+c.body+`}`, f.Session("alice-aaaaaaaa"))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage(c.field); !ok || msg != c.want {
				t.Fatalf("%s message %q, want %q (%s)", c.field, msg, c.want, w.Body)
			}
			if f.WorkspaceCount() != 0 {
				t.Fatal("created")
			}
		})
	}
}

func TestTemplatesSayWhatAppsTake(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate(), apitest.ContainerTemplate())
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))
	type tplJSON struct {
		Name, UserCommand string
		UserImage         bool
		Port              int32
		Sample            *struct{ Command string }
	}
	got := map[string]tplJSON{}
	for _, it := range apitest.Decode[struct{ Items []tplJSON }](t, w).Items {
		got[it.Name] = it
	}
	c, k := got["custom"], got["container"]
	if c.UserCommand != "Required" || c.UserImage || c.Sample == nil || c.Sample.Command != apitest.SampleCommand {
		t.Fatalf("custom = %+v", c)
	}
	if k.UserCommand != "Optional" || !k.UserImage || k.Port != 8080 {
		t.Fatalf("container = %+v", k)
	}
}

func TestChangeWhatAnAppRuns(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.CustomTemplate(), apitest.ContainerTemplate())
	alice := f.Session("alice-aaaaaaaa")
	f.Do("POST", "/api/workspaces", `{"displayName":"Clock","template":"custom","command":"python a.py"}`, alice)
	f.Do("POST", "/api/workspaces", `{"displayName":"Web","template":"container","image":"nginx"}`, alice)

	w := f.Do("PUT", "/api/workspaces/clock/launch", `{"command":"python b.py --port={{.Port}}"}`, alice)
	if w.Code != http.StatusOK || apitest.Decode[apitest.LaunchJSON](t, w).Command != "python b.py --port={{.Port}}" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "clock").Command; got != "python b.py --port={{.Port}}" {
		t.Fatalf("stored %q", got)
	}
	w = f.Do("PUT", "/api/workspaces/web/launch", `{"image":"ghcr.io/example/web:2","port":9000,"command":"--x"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if s := f.GetWorkspace("alice-aaaaaaaa", "web"); s.Image != "ghcr.io/example/web:2" || s.Port != 9000 || s.Command != "--x" {
		t.Fatalf("stored %+v", s)
	}

	// Refused changes leave the app as it was.
	w = f.Do("PUT", "/api/workspaces/clock/launch", `{"command":""}`, alice)
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.command"); w.Code != http.StatusUnprocessableEntity || msg != reasons.Msg(reasons.MsgCommandRequired) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	w = f.Do("PUT", "/api/workspaces/web/launch", `{"image":"Not An Image"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "clock").Command; got != "python b.py --port={{.Port}}" {
		t.Fatalf("refused change stored %q", got)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "web").Image; got != "ghcr.io/example/web:2" {
		t.Fatalf("refused change stored %q", got)
	}
	// Someone else's app is not found.
	if w := f.Do("PUT", "/api/workspaces/clock/launch?owner=alice-aaaaaaaa", `{"command":"x"}`, f.Session("bob-bbbbbbbb")); w.Code != http.StatusNotFound {
		t.Fatalf("bob: status %d: %s", w.Code, w.Body)
	}
}
