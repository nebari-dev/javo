package model_test

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/model"
)

// wantInvalid fails unless err is a *model.ValidationError with an error on
// field whose message holds every one of subs.
func wantInvalid(t *testing.T, err error, field string, subs ...string) {
	t.Helper()
	var ve *model.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *model.ValidationError on %s", err, field)
	}
	for _, f := range ve.Errors {
		if f.Field != field {
			continue
		}
		ok := true
		for _, s := range subs {
			ok = ok && strings.Contains(f.Message, s)
		}
		if ok {
			return
		}
	}
	t.Fatalf("no error on %s mentioning %q, got: %v", field, subs, err)
}

func valid(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("rejected: %v", err)
	}
}

// ---- apps

func newApp(edit func(*model.App)) *model.App {
	a := &model.App{Name: "lab", Owner: "alice-aaaaaaaa", Template: "lab"}
	if edit != nil {
		edit(a)
	}
	return a
}

func TestAppOwnerIsImmutable(t *testing.T) {
	old := newApp(func(a *model.App) { a.Owner = "u-alice" })
	next := newApp(func(a *model.App) { a.Owner = "u-bob" })
	wantInvalid(t, next.ValidateUpdate(old), "spec.owner", "owner is immutable")
	valid(t, old.ValidateUpdate(old))
}

func TestAppOwnerMustBeDNSLabel(t *testing.T) {
	for _, owner := range []string{"local:alice", "Alice", "-alice", "alice_1", strings.Repeat("a", 64), ""} {
		wantInvalid(t, newApp(func(a *model.App) { a.Owner = owner }).Validate(), "spec.owner")
	}
	valid(t, newApp(nil).Validate())
}

func TestAppRequiresTemplate(t *testing.T) {
	wantInvalid(t, newApp(func(a *model.App) { a.Template = "" }).Validate(), "spec.template", "template")
}

func TestPrivateSharingCannotListPeople(t *testing.T) {
	a := newApp(func(a *model.App) {
		a.Sharing = model.Sharing{Access: model.AccessPrivate, Users: []string{"bob-1a2b3c4d"}}
	})
	wantInvalid(t, a.Validate(), "spec.sharing", "private apps cannot list people")
}

func TestSharingAccessEnum(t *testing.T) {
	err := newApp(func(a *model.App) { a.Sharing = model.Sharing{Access: "Everyone"} }).Validate()
	wantInvalid(t, err, "spec.sharing.access", "Private", "Restricted", "SignedIn", "Public")
}

func TestSharingListsAreBounded(t *testing.T) {
	many := func(n int) []string { return make([]string, n) }
	wantInvalid(t, newApp(func(a *model.App) {
		a.Sharing = model.Sharing{Access: model.AccessRestricted, Users: many(201)}
	}).Validate(), "spec.sharing.users")
	wantInvalid(t, newApp(func(a *model.App) {
		a.Sharing = model.Sharing{Access: model.AccessRestricted, Groups: many(51)}
	}).Validate(), "spec.sharing.groups")
}

// An app saved without sharing, or with a Go-built Sharing that names no
// access, reads back as owner only.
func TestAppDefaultsSharingToPrivate(t *testing.T) {
	a := newApp(nil)
	a.ApplyDefaults()
	if a.Sharing.Access != model.AccessPrivate || len(a.Sharing.Users)+len(a.Sharing.Groups) != 0 {
		t.Fatalf("sharing = %+v, want Private and nobody", a.Sharing)
	}
	valid(t, a.Validate())
}

func TestAppCommandImageAndPortAreBounded(t *testing.T) {
	for name, c := range map[string]struct {
		edit  func(*model.App)
		field string
	}{
		"long-command": {func(a *model.App) { a.Command = strings.Repeat("a", model.MaxCommandLength+1) }, "spec.command"},
		"long-image":   {func(a *model.App) { a.Image = strings.Repeat("a", 513) }, "spec.image"},
		"long-size":    {func(a *model.App) { a.Size = strings.Repeat("a", 64) }, "spec.size"},
		"port-high":    {func(a *model.App) { a.Port = 65536 }, "spec.port"},
		"port-neg":     {func(a *model.App) { a.Port = -1 }, "spec.port"},
	} {
		t.Run(name, func(t *testing.T) { wantInvalid(t, newApp(c.edit).Validate(), c.field) })
	}
	valid(t, newApp(func(a *model.App) {
		a.Template, a.Image, a.Port = "container", "nginxinc/nginx-unprivileged:1", 8080
		a.Command = strings.Repeat("a", model.MaxCommandLength)
	}).Validate())
}

// A custom amount is at least 100m CPU and 256Mi memory, and an app asks
// for a named size or a custom amount, never both.
func TestAppCustomResources(t *testing.T) {
	custom := func(size, cpu, mem string) func(*model.App) {
		return func(a *model.App) {
			a.Size = size
			a.Resources = &model.AppResources{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}
		}
	}
	valid(t, newApp(custom("", "2500m", "6Gi")).Validate())
	wantInvalid(t, newApp(custom("small", "1", "1Gi")).Validate(), "spec", "size or resources")
	wantInvalid(t, newApp(custom("", "50m", "1Gi")).Validate(), "spec.resources.cpu", "100m")
	wantInvalid(t, newApp(custom("", "1", "100Mi")).Validate(), "spec.resources.memory", "256Mi")
}

// ---- templates

func newTemplate(edit func(*model.TemplateSpec)) *model.TemplateSpec {
	s := &model.TemplateSpec{DisplayName: "App", Kind: "custom", Image: "example.com/app:1", Port: 8080}
	if edit != nil {
		edit(s)
	}
	return s
}

// A template name was a Kubernetes object name and is still used as one: a
// lowercase DNS-1123 subdomain of at most 253 characters.
func TestTemplateNameIsADNSSubdomain(t *testing.T) {
	tpl := func(name string) *model.Template { return &model.Template{Name: name, Spec: *newTemplate(nil)} }
	for _, name := range []string{"lab", "lab-2", "lab.v2", strings.Repeat("a", 253)} {
		valid(t, tpl(name).Validate())
	}
	for _, name := range []string{"", "Lab", "my lab", "l\u00e4b", "-lab", "lab-", strings.Repeat("a", 254)} {
		wantInvalid(t, tpl(name).Validate(), "metadata.name")
	}
	wantInvalid(t, (&model.Template{Name: "lab", Spec: *newTemplate(func(s *model.TemplateSpec) { s.Port = 0 })}).Validate(), "spec.port")
}

// A template names one of the fixed categories its card is grouped under;
// none means Other.
func TestTemplateCategoryEnum(t *testing.T) {
	for _, c := range []string{"", "notebooks", "dashboards", "web", "agents", "jobs", "other"} {
		valid(t, newTemplate(func(s *model.TemplateSpec) { s.Category = c }).Validate())
	}
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.Category = "games" }).Validate(), "spec.category", "notebooks", "other")
	s := newTemplate(nil)
	s.ApplyDefaults()
	if s.Category != model.CategoryOther {
		t.Fatalf("category = %q, want %q", s.Category, model.CategoryOther)
	}
}

func TestTemplateKindEnum(t *testing.T) {
	for _, kind := range []string{"vscode", "rstudio", "bokeh", "custom", "fastapi", "container", "claude-code", "pi", "herdr", "pluto", "terminal"} {
		valid(t, newTemplate(func(s *model.TemplateSpec) { s.Kind = kind }).Validate())
	}
	err := newTemplate(func(s *model.TemplateSpec) { s.Kind = "quarto" }).Validate()
	all := []string{"jupyterlab", "streamlit", "vscode", "marimo", "pluto", "rstudio", "panel", "bokeh", "dash", "gradio", "voila", "custom", "fastapi", "container", "claude-code", "pi", "herdr", "terminal"}
	wantInvalid(t, err, "spec.kind", all...)
	// Pluto.jl is an app kind, not a job engine.
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) {
		s.Kind, s.Jobs = "jupyterlab", &model.TemplateJobs{Engines: []string{"pluto"}}
	}).Validate(), "spec.jobs.engines[0]")
}

func TestProxyHeadersDenyList(t *testing.T) {
	for _, key := range []string{"cookie", "Authorization", "HOST", "x-forwarded-for"} {
		err := newTemplate(func(s *model.TemplateSpec) { s.ProxyHeaders = map[string]string{key: "x"} }).Validate()
		wantInvalid(t, err, "spec.proxyHeaders", "proxyHeaders may not set Authorization, Cookie, Host or X-Forwarded-For")
	}
	valid(t, newTemplate(func(s *model.TemplateSpec) {
		s.ProxyHeaders = map[string]string{"X-Script-Name": "{{.BaseURL}}"}
	}).Validate())
}

func TestTemplateDefaults(t *testing.T) {
	s := newTemplate(func(s *model.TemplateSpec) { s.Kind, s.Port = "jupyterlab", 8888 })
	s.ApplyDefaults()
	if s.BasePath != "Prefix" || s.AppLogin != "Disabled" || s.MaxSharing != model.AccessPrivate ||
		s.UserCommand != model.UserCommandNone || s.ReadinessPath != "{{.BaseURL}}" {
		t.Fatalf("defaults not applied: %+v", s)
	}
	if s.Jobs != nil {
		t.Fatalf("jobs = %+v, want nil (jobs not allowed)", s.Jobs)
	}
	valid(t, s.Validate())
	s2 := newTemplate(func(s *model.TemplateSpec) { s.BasePath, s.MaxSharing = "Strip", model.AccessPublic })
	s2.ApplyDefaults()
	if s2.BasePath != "Strip" || s2.MaxSharing != model.AccessPublic {
		t.Fatalf("defaults overwrote set values: %+v", s2)
	}
}

func TestAppAndJobNeedAName(t *testing.T) {
	wantInvalid(t, newApp(func(a *model.App) { a.Name = "" }).Validate(), "metadata.name", "required")
	wantInvalid(t, newJob(func(j *model.Job) { j.Name = "" }).Validate(), "metadata.name", "required")
}

func TestTemplateImageAndPortAreValidated(t *testing.T) {
	for name, c := range map[string]struct {
		image string
		port  int32
		field string
	}{
		"empty image": {"", 8888, "spec.image"},
		"port 0":      {"example.com/lab:1", 0, "spec.port"},
		"port 65536":  {"example.com/lab:1", 65536, "spec.port"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTemplate(func(s *model.TemplateSpec) { s.Kind, s.Image, s.Port = "jupyterlab", c.image, c.port })
			wantInvalid(t, s.Validate(), c.field)
		})
	}
}

// A template's environment names a folder of the platform's bundled
// environments, so it is one plain name; an image a person names runs as it
// is, with neither an environment nor the pod files.
func TestTemplateEnvironmentIsAPlainNameOnAPlatformImage(t *testing.T) {
	for _, name := range []string{"streamlit", "jobs-r", "a"} {
		valid(t, newTemplate(func(s *model.TemplateSpec) { s.Environment = name }).Validate())
	}
	for _, name := range []string{"../etc", "a/b", "Streamlit", "-x", ".", strings.Repeat("a", 64)} {
		wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.Environment = name }).Validate(), "spec.environment")
	}
	// The platform's pixi image runs a template with an environment and no
	// image of its own.
	valid(t, newTemplate(func(s *model.TemplateSpec) { s.Image, s.Environment = "", "streamlit" }).Validate())
	if got := newTemplate(func(s *model.TemplateSpec) { s.Image, s.Environment = "", "streamlit" }).RunImage("example.com/pixi:1"); got != "example.com/pixi:1" {
		t.Fatalf("RunImage = %q, want the pixi image", got)
	}
	if got := newTemplate(func(s *model.TemplateSpec) { s.Environment = "streamlit" }).RunImage("example.com/pixi:1"); got != "example.com/app:1" {
		t.Fatalf("RunImage = %q, want the template's own image", got)
	}
	userImage := func(s *model.TemplateSpec) { s.Image, s.UserImage = "", true }
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { userImage(s); s.Environment = "streamlit" }).Validate(), "spec.environment")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { userImage(s); s.PodFiles = true }).Validate(), "spec.podFiles")
}

func TestTemplateEnumsAndDefaults(t *testing.T) {
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.MaxSharing = "Anyone" }).Validate(), "spec.maxSharing", "Private", "Public")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.UserCommand = "Sometimes" }).Validate(), "spec.userCommand")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.BasePath = "Rewrite" }).Validate(), "spec.basePath")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.AppLogin = "Maybe" }).Validate(), "spec.appLogin")
}

func TestTemplateSamplePathMustBeAbsolute(t *testing.T) {
	bad := newTemplate(func(s *model.TemplateSpec) {
		s.Kind, s.Sample = "streamlit", &model.TemplateSample{Path: "samples/x", Title: "X"}
	})
	wantInvalid(t, bad.Validate(), "spec.sample.path")
	valid(t, newTemplate(func(s *model.TemplateSpec) { s.Kind = "streamlit" }).Validate())

	// A sample from an image the template runs is a folder: no image, no port.
	for name, sm := range map[string]*model.TemplateSample{
		"sample-no-path": {Title: "X"},
		"sample-image":   {Path: "/x", Title: "X", Image: "example.com/x:1"},
		"sample-port":    {Path: "/x", Title: "X", Port: 8080},
	} {
		t.Run(name, func(t *testing.T) {
			wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.Sample = sm }).Validate(), "spec.sample")
		})
	}
}

// A template whose sample needs a typed command must say which command, or
// the one-click sample could not start.
func TestRequiredCommandSampleNeedsACommand(t *testing.T) {
	mk := func(cmd string) *model.TemplateSpec {
		return newTemplate(func(s *model.TemplateSpec) {
			s.UserCommand = model.UserCommandRequired
			s.Sample = &model.TemplateSample{Path: "/opt/sample", Title: "Clock", Command: cmd}
		})
	}
	wantInvalid(t, mk("").Validate(), "spec.sample", "sample")
	valid(t, mk("python app.py --port={{.Port}}").Validate())
}

func TestUserImageTemplateHasNoImageOrJobsAndItsSampleNamesAnImage(t *testing.T) {
	mk := func(edit func(*model.TemplateSpec)) *model.TemplateSpec {
		s := &model.TemplateSpec{
			DisplayName: "Container image", Kind: "container", Port: 8080, UserImage: true,
			UserCommand: model.UserCommandOptional, BasePath: "Strip",
		}
		edit(s)
		return s
	}
	valid(t, mk(func(*model.TemplateSpec) {}).Validate())
	valid(t, mk(func(s *model.TemplateSpec) {
		s.Sample = &model.TemplateSample{Title: "X", Image: "example.com/x:1", Port: 8080}
	}).Validate())
	for name, c := range map[string]struct {
		edit  func(*model.TemplateSpec)
		field string
	}{
		"img-and-image": {func(s *model.TemplateSpec) { s.Image = "example.com/app:1" }, "spec.image"},
		"img-and-jobs":  {func(s *model.TemplateSpec) { s.Jobs = &model.TemplateJobs{} }, "spec.jobs"},
		"img-sample-path": {func(s *model.TemplateSpec) {
			s.Sample = &model.TemplateSample{Path: "/x", Title: "X", Image: "example.com/x:1"}
		}, "spec.sample"},
		"img-sample-no-image": {func(s *model.TemplateSpec) { s.Sample = &model.TemplateSample{Title: "X"} }, "spec.sample"},
		"img-sample-image-long": {func(s *model.TemplateSpec) {
			s.Sample = &model.TemplateSample{Title: "X", Image: strings.Repeat("a", 513)}
		}, "spec.sample.image"},
		"img-sample-port-high": {func(s *model.TemplateSpec) {
			s.Sample = &model.TemplateSample{Title: "X", Image: "example.com/x:1", Port: 65536}
		}, "spec.sample.port"},
	} {
		t.Run(name, func(t *testing.T) { wantInvalid(t, mk(c.edit).Validate(), c.field) })
	}
}

func TestTemplateCrossFieldRules(t *testing.T) {
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.JobOnly = true }).Validate(), "spec.jobOnly", "jobOnly needs jobs")
	valid(t, newTemplate(func(s *model.TemplateSpec) { s.JobOnly, s.Jobs = true, &model.TemplateJobs{} }).Validate())
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.APIGuidePaths = []string{"/etc/x.md"} }).Validate(),
		"spec.apiGuidePaths", "apiGuidePaths needs apiToken")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) {
		s.APIToken, s.APIGuidePaths = true, []string{"relative.md"}
	}).Validate(), "spec.apiGuidePaths[0]")
	valid(t, newTemplate(func(s *model.TemplateSpec) {
		s.APIToken, s.APIGuidePaths = true, []string{"/etc/x.md"}
	}).Validate())
}

// ---- jobs

func newJob(edit func(*model.Job)) *model.Job {
	j := &model.Job{
		Name: "daily", Owner: "alice-aaaaaaaa",
		Spec: model.JobSpec{
			Template: "lab", Engine: "papermill", Source: model.JobSource{Path: "reports/daily.ipynb"},
			// keepRuns as ApplyDefaults leaves it; Validate rejects 0.
			History: model.JobHistory{KeepRuns: 20},
		},
	}
	if edit != nil {
		edit(j)
	}
	return j
}

func TestJobOwnerIsImmutable(t *testing.T) {
	old := newJob(func(j *model.Job) { j.Owner = "u-alice" })
	next := newJob(func(j *model.Job) { j.Owner = "u-bob" })
	wantInvalid(t, next.ValidateUpdate(old), "spec.owner", "owner is immutable")
}

// The owner names the person's home claim and goes into label values, as for
// an app, so it must be an Identity name (a DNS-1123 label).
func TestJobOwnerMustBeDNSLabel(t *testing.T) {
	for _, owner := range []string{"local:alice", "alice@example.com", "Alice", "-alice", strings.Repeat("a", 64)} {
		wantInvalid(t, newJob(func(j *model.Job) { j.Owner = owner }).Validate(), "spec.owner")
	}
	valid(t, newJob(func(j *model.Job) { j.Owner = "alice-1a2b3c4d" }).Validate())
}

func TestCommandOnlyForCommandEngine(t *testing.T) {
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Command = []string{"python"} }).Validate(),
		"spec.command", "command is required for engine command and not allowed otherwise")
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Engine = "command" }).Validate(), "spec.command")
	valid(t, newJob(func(j *model.Job) {
		j.Spec.Engine, j.Spec.Command = "command", []string{"python", "etl/run.py"}
	}).Validate())
}

// A bash job is one script, run by bash; it has no file.
func TestBashJobIsOneScript(t *testing.T) {
	valid(t, newJob(func(j *model.Job) {
		j.Spec.Engine, j.Spec.Command = "bash", []string{"echo a\necho \"$JAVO_PARAM_X\""}
		j.Spec.Source = model.JobSource{}
	}).Validate())
	for _, cmd := range [][]string{nil, {"echo a", "echo b"}} {
		wantInvalid(t, newJob(func(j *model.Job) {
			j.Spec.Engine, j.Spec.Command = "bash", cmd
			j.Spec.Source = model.JobSource{}
		}).Validate(), "spec.command", "engine bash takes one script")
	}
}

// A command job may run without a file; every other engine runs one.
func TestSourceNeededExceptForCommand(t *testing.T) {
	valid(t, newJob(func(j *model.Job) {
		j.Spec.Engine, j.Spec.Command = "command", []string{"bash", "-c", "date"}
		j.Spec.Source = model.JobSource{}
	}).Validate())
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Source = model.JobSource{} }).Validate(),
		"spec.source.path", "source.path is required for every engine but command")
}

func TestJobEngineEnum(t *testing.T) {
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Engine = "pluto" }).Validate(), "spec.engine", "papermill", "marimo", "quarto", "command")
}

func TestKernelOnlyForPapermill(t *testing.T) {
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Engine, j.Spec.Kernel = "marimo", "python3" }).Validate(),
		"spec.kernel", "kernel is only for engine papermill")
	valid(t, newJob(func(j *model.Job) { j.Spec.Kernel = "python3" }).Validate())
}

func TestScheduleNeedsTimeZone(t *testing.T) {
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Schedule = &model.JobSchedule{Cron: "0 6 * * 1-5"} }).Validate(),
		"spec.schedule.timeZone")
	j := newJob(func(j *model.Job) {
		j.Spec.Schedule = &model.JobSchedule{Cron: "0 6 * * 1-5", TimeZone: "Europe/Berlin"}
	})
	j.ApplyDefaults()
	valid(t, j.Validate())
	if j.Spec.Schedule.Concurrency != "Forbid" {
		t.Fatalf("concurrency default = %q, want Forbid", j.Spec.Schedule.Concurrency)
	}
}

// The cron and the zone are read with the parser Kubernetes uses, so a
// schedule that would never fire is refused when saved.
func TestScheduleCronAndZoneMustParse(t *testing.T) {
	withSchedule := func(cron, zone string) *model.Job {
		return newJob(func(j *model.Job) { j.Spec.Schedule = &model.JobSchedule{Cron: cron, TimeZone: zone} })
	}
	wantInvalid(t, withSchedule("0 25 * * *", "Europe/Berlin").Validate(), "spec.schedule.cron")
	wantInvalid(t, withSchedule("", "Europe/Berlin").Validate(), "spec.schedule.cron")
	wantInvalid(t, withSchedule("CRON_TZ=UTC 0 6 * * *", "Europe/Berlin").Validate(), "spec.schedule.cron")
	wantInvalid(t, withSchedule("0 6 * * *", "Mars/Base").Validate(), "spec.schedule.timeZone")
	wantInvalid(t, withSchedule("0 6 * * *", "Local").Validate(), "spec.schedule.timeZone")
}

func TestScheduleConcurrencyEnum(t *testing.T) {
	j := newJob(func(j *model.Job) {
		j.Spec.Schedule = &model.JobSchedule{Cron: "0 6 * * *", TimeZone: "UTC", Concurrency: "Sometimes"}
	})
	wantInvalid(t, j.Validate(), "spec.schedule.concurrency", "Allow", "Forbid", "Replace")
}

func TestParameterDefaultMatchesType(t *testing.T) {
	with := func(p model.JobParameter) *model.Job {
		return newJob(func(j *model.Job) { j.Spec.Parameters = []model.JobParameter{p} })
	}
	wantInvalid(t, with(model.JobParameter{Name: "n", Type: "integer", Default: "abc"}).Validate(), "spec.parameters[0].default", "default must be an integer")
	wantInvalid(t, with(model.JobParameter{Name: "b", Type: "boolean", Default: "yes"}).Validate(), "spec.parameters[0].default", "default must be true or false")
	wantInvalid(t, with(model.JobParameter{Name: "x", Type: "number", Default: "1.2.3"}).Validate(), "spec.parameters[0].default", "default must be a number")
	wantInvalid(t, with(model.JobParameter{Name: "1bad", Type: "string"}).Validate(), "spec.parameters[0].name")
	wantInvalid(t, with(model.JobParameter{Name: "s", Type: "list"}).Validate(), "spec.parameters[0].type", "string", "integer", "number", "boolean")
	valid(t, with(model.JobParameter{Name: "n", Type: "integer", Default: "42"}).Validate())
	valid(t, with(model.JobParameter{Name: "x", Type: "number", Default: "-0.5"}).Validate())
	valid(t, with(model.JobParameter{Name: "b", Type: "boolean", Default: "true"}).Validate())
	valid(t, with(model.JobParameter{Name: "s", Type: "string", Default: "anything"}).Validate())
}

func TestJobRunAndHistoryBounds(t *testing.T) {
	zero, big := int32(0), int32(21)
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.Run.Retries = 6 }).Validate(), "spec.run.retries")
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.History.KeepRuns = 201 }).Validate(), "spec.history.keepRuns")
	// Without ApplyDefaults, keepRuns 0 would mean keep no run history.
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.History.KeepRuns = 0 }).Validate(), "spec.history.keepRuns", "between 1 and 200")
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.History.KeepSucceeded = &big }).Validate(), "spec.history.keepSucceeded")
	wantInvalid(t, newJob(func(j *model.Job) { j.Spec.History.KeepFailed = &big }).Validate(), "spec.history.keepFailed")
	valid(t, newJob(func(j *model.Job) { j.Spec.History.KeepSucceeded = &zero }).Validate())
}

func TestJobDefaults(t *testing.T) {
	j := newJob(nil)
	j.ApplyDefaults()
	s := j.Spec
	h := s.History
	if s.Run.Retries != 0 || h.KeepRuns != 20 || h.KeepSucceeded == nil || *h.KeepSucceeded != 3 ||
		h.KeepFailed == nil || *h.KeepFailed != 3 || s.Notify.OnFailure == nil || !*s.Notify.OnFailure {
		t.Fatalf("defaults not applied: run=%+v history=%+v notify=%+v", s.Run, s.History, s.Notify)
	}
	// An explicit 0 and an explicit false are kept, not replaced.
	zero, off := int32(0), false
	k := newJob(func(j *model.Job) {
		j.Spec.History.KeepSucceeded, j.Spec.Notify.OnFailure = &zero, &off
	})
	k.ApplyDefaults()
	if *k.Spec.History.KeepSucceeded != 0 || *k.Spec.Notify.OnFailure {
		t.Fatal("ApplyDefaults replaced an explicit 0 or false")
	}
}

// A spec stored without run, history and notify blocks (an old manifest)
// still gets the defaults.
func TestJobDefaultsWhenBlocksOmitted(t *testing.T) {
	var spec model.JobSpec
	in := `{"template":"lab","engine":"papermill","source":{"path":"reports/daily.ipynb"}}`
	if err := json.Unmarshal([]byte(in), &spec); err != nil {
		t.Fatal(err)
	}
	j := &model.Job{Owner: "alice-aaaaaaaa", Spec: spec}
	j.ApplyDefaults()
	h := j.Spec.History
	if h.KeepRuns != 20 || *h.KeepSucceeded != 3 || *h.KeepFailed != 3 || !*j.Spec.Notify.OnFailure {
		t.Fatalf("defaults not applied: %+v %+v", h, j.Spec.Notify)
	}
}

// ---- people

func newPerson(edit func(*model.Person)) *model.Person {
	p := &model.Person{
		Name:     "alice-1a2b3c4d",
		Subjects: []model.Subject{{Issuer: "https://idp.example.com", Subject: "sub-1"}},
		Username: "alice",
	}
	if edit != nil {
		edit(p)
	}
	return p
}

func TestPersonUIDIsImmutableOnceSet(t *testing.T) {
	old := newPerson(nil)
	next := newPerson(func(p *model.Person) { p.UID = 100001 })
	valid(t, next.ValidateUpdate(old)) // the first uid is accepted
	changed := newPerson(func(p *model.Person) { p.UID = 100002 })
	wantInvalid(t, changed.ValidateUpdate(next), "spec.posix.uid", "uid is immutable")
}

func TestPersonFirstSubjectIsImmutableButListMayGrow(t *testing.T) {
	old := newPerson(nil)
	grown := newPerson(func(p *model.Person) {
		p.Subjects = append(p.Subjects, model.Subject{Issuer: "https://idp2.example.com", Subject: "sub-2"})
	})
	valid(t, grown.ValidateUpdate(old))
	swapped := newPerson(func(p *model.Person) { p.Subjects[0].Subject = "other" })
	wantInvalid(t, swapped.ValidateUpdate(old), "spec.subjects", "the first subject is immutable")
}

func TestPersonRequiresASubject(t *testing.T) {
	wantInvalid(t, newPerson(func(p *model.Person) { p.Subjects = nil }).Validate(), "spec.subjects")
	wantInvalid(t, newPerson(func(p *model.Person) { p.Subjects[0].Issuer = "" }).Validate(), "spec.subjects[0].issuer")
}

func TestPersonGroupsAreBounded(t *testing.T) {
	many := make([]string, 201)
	for i := range many {
		many[i] = "g"
	}
	wantInvalid(t, newPerson(func(p *model.Person) { p.Groups = many }).Validate(), "status.groups")
	p := newPerson(func(p *model.Person) { p.Groups = []string{"/team/Data Science", "admins"} })
	valid(t, p.Validate())
}

// ---- names

func TestAccessRankOrdersLeastToMostOpen(t *testing.T) {
	order := []string{model.AccessPrivate, model.AccessRestricted, model.AccessSignedIn, model.AccessPublic}
	for i := 1; i < len(order); i++ {
		if model.AccessRank(order[i-1]) >= model.AccessRank(order[i]) {
			t.Fatalf("%s should rank below %s", order[i-1], order[i])
		}
	}
	if model.AccessRank("bogus") >= model.AccessRank(model.AccessPrivate) {
		t.Fatal("unknown access must rank below Private so it never grants more")
	}
}

// The Helm chart cannot import Go constants, so its one copy of the group
// must match Group.
func TestHelmChartGroupMatchesGroup(t *testing.T) {
	b, err := os.ReadFile("../../deploy/helm/javo/templates/_helpers.tpl")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`define "javo\.group" -\}\}\n(\S+)\n`).FindSubmatch(b)
	if m == nil {
		t.Fatal(`no "javo.group" helper in _helpers.tpl`)
	}
	if string(m[1]) != model.Group {
		t.Fatalf("chart group %q, Go group %q", m[1], model.Group)
	}
}

// ---- length limits count characters, as the old CRD MaxLength did

func TestLengthLimitsCountCharactersNotBytes(t *testing.T) {
	r := func(n int) string { return strings.Repeat("é", n) }
	type tc struct {
		name  string
		field string
		build func(n int) error
		limit int
	}
	for _, c := range []tc{
		{"app command", "spec.command", func(n int) error { return newApp(func(a *model.App) { a.Command = r(n) }).Validate() }, model.MaxCommandLength},
		{"sample title", "spec.sample.title", func(n int) error {
			return newTemplate(func(s *model.TemplateSpec) { s.Sample = &model.TemplateSample{Path: "/x", Title: r(n)} }).Validate()
		}, 63},
		{"sample path", "spec.sample.path", func(n int) error {
			return newTemplate(func(s *model.TemplateSpec) { s.Sample = &model.TemplateSample{Path: "/" + r(n-1), Title: "X"} }).Validate()
		}, 1024},
		{"sample command", "spec.jobs.sample.command", func(n int) error {
			return newTemplate(func(s *model.TemplateSpec) {
				s.Jobs = &model.TemplateJobs{Sample: &model.TemplateJobSample{Title: "X", Command: r(n)}}
			}).Validate()
		}, 2048},
		{"job sample title", "spec.jobs.sample.title", func(n int) error {
			return newTemplate(func(s *model.TemplateSpec) {
				s.Jobs = &model.TemplateJobs{Sample: &model.TemplateJobSample{Title: r(n), Command: "x"}}
			}).Validate()
		}, 63},
		{"api guide path", "spec.apiGuidePaths[0]", func(n int) error {
			return newTemplate(func(s *model.TemplateSpec) { s.APIToken, s.APIGuidePaths = true, []string{"/" + r(n-1)} }).Validate()
		}, 1024},
	} {
		t.Run(c.name, func(t *testing.T) {
			valid(t, c.build(c.limit))
			wantInvalid(t, c.build(c.limit+1), c.field)
		})
	}
}

// ---- API access

func TestAPIAccessEnums(t *testing.T) {
	wantInvalid(t, newApp(func(a *model.App) { a.APIAccess = "Admin" }).Validate(), "spec.apiAccess", "None", "ReadWrite")
	for _, ok := range []string{"", model.APIAccessNone, model.APIAccessRead, model.APIAccessReadWrite} {
		valid(t, newApp(func(a *model.App) { a.APIAccess = ok }).Validate())
	}
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.MaxAPIAccess = "Admin" }).Validate(), "spec.maxApiAccess", "None", "ReadWrite")
	valid(t, newTemplate(func(s *model.TemplateSpec) { s.MaxAPIAccess = model.APIAccessRead }).Validate())
}

// An app's API access is its own choice or its template's default, never
// above the template's cap; an unknown value grants nothing.
func TestAPIAccessOfAnApp(t *testing.T) {
	tpl := func(apiToken bool, max string) *model.Template {
		return &model.Template{Spec: model.TemplateSpec{APIToken: apiToken, MaxAPIAccess: max}}
	}
	cases := []struct {
		name, own string
		t         *model.Template
		want      string
	}{
		{"plain default", "", tpl(false, ""), model.APIAccessNone},
		{"agent default", "", tpl(true, ""), model.APIAccessReadWrite},
		{"agent default capped", "", tpl(true, model.APIAccessRead), model.APIAccessRead},
		{"own choice", model.APIAccessRead, tpl(false, ""), model.APIAccessRead},
		{"own choice off", model.APIAccessNone, tpl(true, ""), model.APIAccessNone},
		{"own choice capped", model.APIAccessReadWrite, tpl(true, model.APIAccessRead), model.APIAccessRead},
		{"cap none", model.APIAccessReadWrite, tpl(true, model.APIAccessNone), model.APIAccessNone},
		{"unknown cap", model.APIAccessReadWrite, tpl(true, "Admin"), model.APIAccessNone},
		{"unknown own", "Admin", tpl(true, ""), model.APIAccessNone},
	}
	for _, c := range cases {
		a := newApp(func(a *model.App) { a.APIAccess = c.own })
		if got := model.APIAccessOf(a, c.t); got != c.want {
			t.Errorf("%s: APIAccessOf = %q, want %q", c.name, got, c.want)
		}
	}
	if got := model.DefaultAPIAccess(tpl(true, model.APIAccessRead)); got != model.APIAccessRead {
		t.Errorf("DefaultAPIAccess = %q, want the cap", got)
	}
	if got := model.MaxAPIAccess(tpl(false, "")); got != model.APIAccessReadWrite {
		t.Errorf("MaxAPIAccess of an unset cap = %q, want ReadWrite", got)
	}
}

// ---- apps from a Git repository

func repoApp(edit func(*model.Source)) *model.App {
	return newApp(func(a *model.App) {
		a.Source = &model.Source{URL: "https://github.com/example-org/site"}
		if edit != nil {
			edit(a.Source)
		}
	})
}

func TestSourceURLMustBePlainHTTPS(t *testing.T) {
	valid(t, repoApp(func(s *model.Source) { s.Path = "web" }).Validate())
	valid(t, repoApp(func(s *model.Source) { s.Ref = "v1.2.0" }).Validate())
	for name, url := range map[string]string{
		"empty":       "",
		"http":        "http://github.com/example-org/site",
		"ssh":         "ssh://git@github.com/example-org/site",
		"scp style":   "git@github.com:example-org/site",
		"file":        "file:///etc/passwd",
		"no host":     "https:///example-org/site",
		"credentials": "https://alice:pw@github.com/example-org/site",
		"user only":   "https://alice@github.com/example-org/site",
		"query":       "https://github.com/example-org/site?a=1",
		"empty query": "https://github.com/example-org/site?",
		"fragment":    "https://github.com/example-org/site#main",
		"too long":    "https://example.com/" + strings.Repeat("a", 2048),
	} {
		t.Run(name, func(t *testing.T) {
			wantInvalid(t, repoApp(func(s *model.Source) { s.URL = url }).Validate(), "spec.source.url")
		})
	}
}

func TestSourceURLErrorsNeverEchoCredentials(t *testing.T) {
	err := repoApp(func(s *model.Source) { s.URL = "https://alice:s3cret-pw@github.com/example-org/site" }).Validate()
	if err == nil || strings.Contains(err.Error(), "s3cret-pw") {
		t.Fatalf("error must exist and not repeat the password: %v", err)
	}
}

func TestSourcePathStaysInsideTheClone(t *testing.T) {
	for _, p := range []string{"../x", "a/../../x", "/abs", "a/..", strings.Repeat("a", 1025)} {
		wantInvalid(t, repoApp(func(s *model.Source) { s.Path = p }).Validate(), "spec.source.path")
	}
	for _, p := range []string{"web", "a/b/c", "a..b", "./web"} {
		valid(t, repoApp(func(s *model.Source) { s.Path = p }).Validate())
	}
}

func TestSourceBuildAndRefAreBounded(t *testing.T) {
	wantInvalid(t, repoApp(func(s *model.Source) { s.Ref = strings.Repeat("a", 256) }).Validate(), "spec.source.ref")
	wantInvalid(t, repoApp(func(s *model.Source) {
		s.Build = &model.SourceBuild{Command: strings.Repeat("a", 1025)}
	}).Validate(), "spec.source.build.command")
	wantInvalid(t, repoApp(func(s *model.Source) { s.Build = &model.SourceBuild{Output: "../out"} }).Validate(), "spec.source.build.output")
	wantInvalid(t, repoApp(func(s *model.Source) { s.Build = &model.SourceBuild{Output: "/out"} }).Validate(), "spec.source.build.output")
	valid(t, repoApp(func(s *model.Source) {
		s.Build = &model.SourceBuild{Command: "npm ci && npm run build", Output: "dist"}
	}).Validate())
}

func TestPreviewsNeedAGitHubRepoAndAReasonableMax(t *testing.T) {
	on := func(max int32) func(*model.Source) {
		return func(s *model.Source) { s.Previews = &model.Previews{Enabled: true, Max: max} }
	}
	valid(t, repoApp(on(10)).Validate())
	valid(t, repoApp(on(0)).Validate()) // 0 means the default, 3
	wantInvalid(t, repoApp(on(11)).Validate(), "spec.source.previews.max")
	wantInvalid(t, repoApp(on(-1)).Validate(), "spec.source.previews.max")
	wantInvalid(t, repoApp(func(s *model.Source) {
		on(3)(s)
		s.URL = "https://gitlab.example.com/example-org/site"
	}).Validate(), "spec.source.previews")
	for _, u := range []string{
		"https://github.com/a%3Fper_page=1/site",
		"https://github.com/../user",
		"https://github.com/a/..",
		"https://github.com:8443/a/b",
	} {
		wantInvalid(t, repoApp(func(s *model.Source) { on(3)(s); s.URL = u }).Validate(), "spec.source.previews")
	}
	// Switched off, previews may sit on any host.
	valid(t, repoApp(func(s *model.Source) {
		s.URL = "https://gitlab.example.com/example-org/site"
		s.Previews = &model.Previews{}
	}).Validate())
}

func TestAppDefaultsPreviewMax(t *testing.T) {
	a := repoApp(func(s *model.Source) { s.Previews = &model.Previews{Enabled: true} })
	a.ApplyDefaults()
	if a.Source.Previews.Max != 3 {
		t.Fatalf("default max = %d, want 3", a.Source.Previews.Max)
	}
	b := repoApp(func(s *model.Source) { s.Previews = &model.Previews{Enabled: true, Max: 7} })
	b.ApplyDefaults()
	if b.Source.Previews.Max != 7 {
		t.Fatalf("max 7 became %d", b.Source.Previews.Max)
	}
}

func TestPreviewParentAndNumberAreImmutable(t *testing.T) {
	old := newApp(func(a *model.App) { a.ParentID, a.PullNumber = "p-1", 4 })
	valid(t, old.ValidateUpdate(old))
	moved := newApp(func(a *model.App) { a.ParentID, a.PullNumber = "p-2", 4 })
	wantInvalid(t, moved.ValidateUpdate(old), "spec.parentId", "immutable")
	renumbered := newApp(func(a *model.App) { a.ParentID, a.PullNumber = "p-1", 5 })
	wantInvalid(t, renumbered.ValidateUpdate(old), "spec.pullNumber", "immutable")
	orphaned := newApp(nil)
	wantInvalid(t, orphaned.ValidateUpdate(old), "spec.parentId", "immutable")
}

func TestGitHubRepo(t *testing.T) {
	for url, want := range map[string][2]string{
		"https://github.com/example-org/site":      {"example-org", "site"},
		"https://github.com/example-org/site.git":  {"example-org", "site"},
		"https://github.com/example-org/site/":     {"example-org", "site"},
		"https://GitHub.com/example-org/my.site":   {"example-org", "my.site"},
		"https://github.com/example-org/site.git/": {"example-org", "site"},
	} {
		owner, repo, ok := model.GitHubRepo(url)
		if !ok || owner != want[0] || repo != want[1] {
			t.Errorf("GitHubRepo(%q) = %q, %q, %v; want %q, %q", url, owner, repo, ok, want[0], want[1])
		}
	}
	for _, url := range []string{
		"https://gitlab.example.com/a/b",
		"https://github.com/example-org",
		"https://github.com/example-org/site/tree/main",
		"https://github.com/",
		"https://github.com.evil.example/a/b",
		"git@github.com:example-org/site",
		"",
		"https://github.com/../user",
		"https://github.com/./user",
		"https://github.com/a%3Fper_page=1/site",
		"https://github.com/a%20b/c",
		"https://github.com/a/..",
		"https://github.com/a/.",
		"https://github.com/a/..git",
		"https://github.com/a/b%2Fc",
		"https://github.com/a/b%3Fx=1",
		"https://github.com/-a/site",
		"https://github.com/a_b/site",
		"https://github.com/" + strings.Repeat("a", 40) + "/site",
		"https://github.com/a/" + strings.Repeat("b", 101),
		"https://github.com:8443/a/b",
		"https://github.com:443/a/b",
	} {
		if owner, repo, ok := model.GitHubRepo(url); ok {
			t.Errorf("GitHubRepo(%q) = %q, %q, true; want false", url, owner, repo)
		}
	}
}

func TestTemplateFromRepoAndBuild(t *testing.T) {
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.FromRepo = "Sometimes" }).Validate(), "spec.fromRepo")
	for _, v := range []string{"", model.FromRepoNone, model.FromRepoOptional, model.FromRepoRequired} {
		valid(t, newTemplate(func(s *model.TemplateSpec) { s.FromRepo = v }).Validate())
	}
	// A file in the repo: fromRepo Required keeps fileExtensions.
	valid(t, newTemplate(func(s *model.TemplateSpec) {
		s.FromRepo, s.FileExtensions = model.FromRepoRequired, []string{".py"}
	}).Validate())

	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) {
		s.Build = &model.TemplateBuild{Image: "example.com/node:22"}
	}).Validate(), "spec.build.serveDir")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) {
		s.Build = &model.TemplateBuild{Image: "example.com/node:22", ServeDir: "site"}
	}).Validate(), "spec.build.serveDir", "absolute")
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) {
		s.Build = &model.TemplateBuild{ServeDir: "/usr/share/nginx/html"}
	}).Validate(), "spec.build.image")
	valid(t, newTemplate(func(s *model.TemplateSpec) {
		s.Kind = "static"
		s.Build = &model.TemplateBuild{Image: "example.com/node:22", ServeDir: "/usr/share/nginx/html"}
	}).Validate())
	valid(t, newTemplate(func(s *model.TemplateSpec) { s.Kind = "static" }).Validate())
}

func TestTemplateBuildServerConfigNeedsItsFolderAndParses(t *testing.T) {
	build := func(conf, dir string) func(*model.TemplateSpec) {
		return func(s *model.TemplateSpec) {
			s.Build = &model.TemplateBuild{Image: "example.com/node:22", ServeDir: "/srv/www", ServerConfig: conf, ServerConfigDir: dir}
		}
	}
	valid(t, newTemplate(build("", "")).Validate())
	valid(t, newTemplate(build("root {{.ServeDir}}; listen {{.Port}};", "/etc/nginx/conf.d")).Validate())
	valid(t, newTemplate(build("listen {{.Port}};", "/etc/my conf; $(id)")).Validate())
	wantInvalid(t, newTemplate(build("listen {{.Port}};", "")).Validate(), "spec.build.serverConfigDir")
	wantInvalid(t, newTemplate(build("", "/etc/nginx/conf.d")).Validate(), "spec.build.serverConfig")
	wantInvalid(t, newTemplate(build("listen {{.Port", "/etc/nginx/conf.d")).Validate(), "spec.build.serverConfig", "template")
	wantInvalid(t, newTemplate(build("listen {{.Nope}};", "/etc/nginx/conf.d")).Validate(), "spec.build.serverConfig")
	wantInvalid(t, newTemplate(build("listen 1;", "conf.d")).Validate(), "spec.build.serverConfigDir", "absolute")
}

// ifFile reaches the build step's shell, so it is a plain file name.
func TestTemplateBuildIfFileIsAPlainFileName(t *testing.T) {
	build := func(f string) func(*model.TemplateSpec) {
		return func(s *model.TemplateSpec) {
			s.Build = &model.TemplateBuild{Image: "example.com/node:22", ServeDir: "/srv/www", IfFile: f}
		}
	}
	for _, ok := range []string{"", "package.json", "go.mod", "Makefile", "build_site-1.sh"} {
		valid(t, newTemplate(build(ok)).Validate())
	}
	for _, bad := range []string{".", "..", "web/package.json", "/package.json", "a b", "$(id)", `x"`, strings.Repeat("a", 256)} {
		wantInvalid(t, newTemplate(build(bad)).Validate(), "spec.build.ifFile")
	}
}

func TestTemplateBuildRendersServerConfig(t *testing.T) {
	b := &model.TemplateBuild{ServeDir: "/srv/www", ServerConfig: "listen {{.Port}}; root {{.ServeDir}};"}
	got, err := b.RenderServerConfig(8080)
	if err != nil || got != "listen 8080; root /srv/www;" {
		t.Fatalf("rendered %q, %v", got, err)
	}
}

func TestTemplateBuildOutputDefaultsToDist(t *testing.T) {
	s := newTemplate(func(s *model.TemplateSpec) {
		s.Build = &model.TemplateBuild{Image: "example.com/node:22", ServeDir: "/srv"}
	})
	s.ApplyDefaults()
	if s.FromRepo != model.FromRepoNone || s.Build.Output != "dist" {
		t.Fatalf("defaults: fromRepo %q, output %q", s.FromRepo, s.Build.Output)
	}
}

// A template that serves kernels installs a picked environment beside its
// own, so it needs an environment of its own.
func TestServesKernelsNeedsAnEnvironment(t *testing.T) {
	wantInvalid(t, newTemplate(func(s *model.TemplateSpec) { s.ServesKernels = true }).Validate(), "spec.servesKernels", "environment")
	valid(t, newTemplate(func(s *model.TemplateSpec) { s.ServesKernels, s.Image, s.Environment = true, "", "jupyterlab" }).Validate())
}

// An app gets its owner's Nebi token when its template asks for one, as API
// tokens: also when it is shared (anyone it is shared with acts as the
// owner in Nebi), never as a pull request preview, which runs code the
// owner has not necessarily written.
func TestNebiTokenFor(t *testing.T) {
	tpl := func(nebi bool) *model.Template { return &model.Template{Spec: model.TemplateSpec{NebiToken: nebi}} }
	cases := []struct {
		name string
		app  model.App
		t    *model.Template
		want bool
	}{
		{"asked for", model.App{}, tpl(true), true},
		{"shared", model.App{Sharing: model.Sharing{Access: model.AccessSignedIn}}, tpl(true), true},
		{"not asked for", model.App{}, tpl(false), false},
		{"no template", model.App{}, nil, false},
		{"preview", model.App{ParentID: "p-1"}, tpl(true), false},
	}
	for _, c := range cases {
		if got := model.NebiTokenFor(&c.app, c.t); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A template's default amount is what its apps run at when the person picks
// no size, so it follows the custom amount minimum.
func TestTemplateDefaultResourcesFollowTheCustomMinimum(t *testing.T) {
	amount := func(cpu, mem string) func(*model.TemplateSpec) {
		return func(s *model.TemplateSpec) {
			s.DefaultResources = &model.TemplateAmount{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}
		}
	}
	valid(t, newTemplate(amount("250m", "512Mi")).Validate())
	wantInvalid(t, newTemplate(amount("50m", "512Mi")).Validate(), "spec.defaultResources.cpu")
	wantInvalid(t, newTemplate(amount("250m", "128Mi")).Validate(), "spec.defaultResources.memory")
}
