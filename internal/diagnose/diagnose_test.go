package diagnose

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

// failedApp is alice's app "sales", failed at condition typ with code and
// the evidence lines the controller stored in the condition's message.
func failedApp(typ string, code reasons.Code, evidence ...string) *model.App {
	app := &model.App{Name: "sales", Owner: "alice-aaaaaaaa", Template: "streamlit", Size: "small"}
	app.Status.Phase = "Failed"
	for _, t := range []string{model.ConditionScheduled, model.ConditionHomeReady, model.ConditionImageReady, model.ConditionStarted, model.ConditionReady} {
		c := metav1.Condition{Type: t, Status: metav1.ConditionTrue, Reason: string(reasons.Ready)}
		if t == typ {
			c.Status, c.Reason, c.Message = metav1.ConditionFalse, string(code), strings.Join(evidence, "\n")
		}
		app.Status.Conditions = append(app.Status.Conditions, c)
	}
	return app
}

func runningApp() *model.App {
	app := failedApp("", "")
	app.Status.Phase = "Running"
	return app
}

func prevLog(n int, text string) LogLine {
	return LogLine{Container: "app", Previous: true, N: n, Text: text}
}

func event(name, reason, msg string) corev1.Event {
	return corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: name}, Reason: reason, Message: msg, Type: "Warning",
		LastTimestamp: metav1.NewTime(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))}
}

func size(name, mem string) sizes.Size {
	return sizes.Size{Name: name, CPU: resource.MustParse("1"), Memory: resource.MustParse(mem)}
}

func TestRules(t *testing.T) {
	crashed := func() *model.App {
		return failedApp(model.ConditionStarted, reasons.AppCrashed, "container app terminated: Error, exit code 1")
	}
	cases := []struct {
		name      string
		in        Input
		code      reasons.Code
		known     bool
		ref       string // an evidence ref the finding must cite
		excerpt   string // text that excerpt must contain
		proposals []string
	}{
		{
			name: "image not found",
			in: Input{App: failedApp(model.ConditionImageReady, reasons.ImagePullFailed,
				`container app waiting: ErrImagePull: rpc error: failed to resolve reference "example.org/x:1": example.org/x:1: not found`)},
			code: reasons.ImageNotFound, known: true, ref: "condition/ImageReady", excerpt: "not found",
		},
		{
			name: "image manifest unknown",
			in: Input{App: failedApp(model.ConditionImageReady, reasons.ImagePullFailed,
				`container app waiting: ErrImagePull: manifest unknown`)},
			code: reasons.ImageNotFound, known: true, ref: "condition/ImageReady",
		},
		{
			name: "image auth failed",
			in: Input{App: failedApp(model.ConditionImageReady, reasons.ImagePullFailed,
				`container app waiting: ImagePullBackOff: pull access denied, repository does not exist or may require authorization: authentication required`)},
			code: reasons.ImageAuthFailed, known: true, ref: "condition/ImageReady", excerpt: "authentication required",
		},
		{
			name: "missing module",
			in: Input{App: crashed(), Logs: []LogLine{
				prevLog(40, "Traceback (most recent call last):"),
				prevLog(41, "ModuleNotFoundError: No module named 'plotly'"),
			}},
			code: reasons.MissingModule, known: true, ref: "log/app/prev/41", excerpt: "plotly", proposals: []string{"restart sales"},
		},
		{
			name: "port in use",
			in:   Input{App: crashed(), Logs: []LogLine{prevLog(3, "OSError: [Errno 98] Address already in use")}},
			code: reasons.PortInUse, known: true, ref: "log/app/prev/3", proposals: []string{"restart sales"},
		},
		{
			name: "home permission denied",
			in:   Input{App: crashed(), Logs: []LogLine{prevLog(7, "PermissionError: [Errno 13] Permission denied: '/home/user/x'")}},
			code: reasons.HomePermissionDenied, known: true, ref: "log/app/prev/7",
		},
		{
			name: "wrong base path",
			in: Input{
				App:    failedApp(model.ConditionReady, reasons.AppNotAnswering, "readiness probe http://:8501/u/alice/sales/ not ready for 2m0s"),
				Events: []corev1.Event{event("sales.1", "Unhealthy", "Readiness probe failed: HTTP probe failed with statuscode: 404")},
			},
			code: reasons.WrongBasePath, known: true, ref: "event/sales.1", excerpt: "404",
		},
		{
			name: "out of memory with a larger size",
			in: Input{
				App:     failedApp(model.ConditionStarted, reasons.OutOfMemory, "container app terminated: OOMKilled, exit code 137"),
				Current: size("small", "2Gi"),
				Sizes:   []sizes.Size{size("tiny", "1Gi"), size("small", "2Gi"), size("large", "32Gi"), size("medium", "8Gi")},
			},
			code: reasons.OutOfMemory, known: true, ref: "condition/Started", proposals: []string{"resize sales size=medium"},
		},
		{
			name: "out of memory proposes a size with the same GPUs",
			in: Input{
				App:     failedApp(model.ConditionStarted, reasons.OutOfMemory, "container app terminated: OOMKilled, exit code 137"),
				Current: size("small", "2Gi"),
				Sizes:   []sizes.Size{size("small", "2Gi"), withGPU(size("gpu", "8Gi")), size("large", "32Gi")},
			},
			code: reasons.OutOfMemory, known: true, ref: "condition/Started", proposals: []string{"resize sales size=large"},
		},
		{
			name: "out of memory on a GPU size keeps the GPU",
			in: Input{
				App:     failedApp(model.ConditionStarted, reasons.OutOfMemory, "container app terminated: OOMKilled, exit code 137"),
				Current: withGPU(size("gpu", "8Gi")),
				Sizes:   []sizes.Size{withGPU(size("gpu", "8Gi")), size("large", "32Gi"), withGPU(size("gpu-big", "64Gi"))},
			},
			code: reasons.OutOfMemory, known: true, ref: "condition/Started", proposals: []string{"resize sales size=gpu-big"},
		},
		{
			name: "out of memory at the largest size",
			in: Input{
				App:     failedApp(model.ConditionStarted, reasons.OutOfMemory, "container app terminated: OOMKilled, exit code 137"),
				Current: size("large", "32Gi"),
				Sizes:   []sizes.Size{size("small", "2Gi"), size("large", "32Gi")},
			},
			code: reasons.OutOfMemory, known: true, ref: "condition/Started",
		},
		{
			name: "no capacity from its condition",
			in: Input{App: func() *model.App {
				a := failedApp(model.ConditionScheduled, reasons.NoCapacity, "event FailedScheduling: 0/3 nodes are available: 3 Insufficient cpu")
				a.Status.Phase = "Starting"
				return a
			}()},
			code: reasons.NoCapacity, known: true, ref: "condition/Scheduled", excerpt: "Insufficient cpu",
		},
		{name: "home busy", in: Input{App: failedApp(model.ConditionHomeReady, reasons.HomeBusy, "x")}, code: reasons.HomeBusy, known: true, ref: "condition/HomeReady"},
		{name: "template missing", in: Input{App: failedApp(model.ConditionScheduled, reasons.TemplateMissing)}, code: reasons.TemplateMissing, known: true, ref: "condition/Scheduled"},
		{name: "template invalid", in: Input{App: failedApp(model.ConditionScheduled, reasons.TemplateInvalid, "bad")}, code: reasons.TemplateInvalid, known: true, ref: "condition/Scheduled"},
		{
			name: "crash with no recognized cause",
			in:   Input{App: crashed(), Logs: []LogLine{prevLog(1, "something odd happened")}},
			code: reasons.AppCrashed, known: false, ref: "condition/Started", proposals: []string{"restart sales"},
		},
		{
			name: "failed with no failing condition",
			in:   Input{App: func() *model.App { a := runningApp(); a.Status.Phase = "Failed"; return a }()},
			code: reasons.Unknown, known: false,
		},
	}
	e := NewEngine(DefaultRules()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := e.Diagnose(&tc.in)
			if d.Code != tc.code || d.Known != tc.known {
				t.Fatalf("got code %s known %v, want %s %v (rule %s)", d.Code, d.Known, tc.code, tc.known, d.Rule)
			}
			if _, ok := reasons.Lookup(d.Code); !ok {
				t.Errorf("code %s has no catalog entry", d.Code)
			}
			if tc.ref != "" {
				ev, ok := findRef(d.Evidence, tc.ref)
				if !ok {
					t.Fatalf("evidence %+v lacks ref %s", d.Evidence, tc.ref)
				}
				if !strings.Contains(ev.Excerpt, tc.excerpt) {
					t.Errorf("excerpt %q lacks %q", ev.Excerpt, tc.excerpt)
				}
				if _, ok := findRef(d.Bundle, tc.ref); !ok {
					t.Errorf("bundle lacks the cited ref %s", tc.ref)
				}
			}
			var got []string
			for _, p := range d.Proposals {
				s := p.Action + " " + p.Target
				for k, v := range p.Params {
					s += " " + k + "=" + v
				}
				got = append(got, s)
			}
			if strings.Join(got, ";") != strings.Join(tc.proposals, ";") {
				t.Errorf("proposals %q, want %q", got, tc.proposals)
			}
		})
	}
}

func findRef(evs []Evidence, ref string) (Evidence, bool) {
	for _, e := range evs {
		if e.Ref == ref {
			return e, true
		}
	}
	return Evidence{}, false
}

func TestHealthyAppsGetNoRules(t *testing.T) {
	e := NewEngine(DefaultRules()...)
	d := e.Diagnose(&Input{App: runningApp(), Logs: []LogLine{prevLog(1, "ModuleNotFoundError: No module named 'x'")}})
	if d.Code != reasons.Ready || !d.Known || len(d.Proposals) != 0 || d.Rule != "" {
		t.Fatalf("running app: %+v", d.Finding)
	}
	stopped := runningApp()
	stopped.Paused, stopped.Status.Phase = true, "Stopped"
	if d := e.Diagnose(&Input{App: stopped}); d.Code != reasons.Stopped || !d.Known || len(d.Proposals) != 0 {
		t.Fatalf("stopped app: %+v", d.Finding)
	}
}

func TestBundleHoldsEverythingSeenOnceAndCutsExcerpts(t *testing.T) {
	long := strings.Repeat("é", 2000)
	in := &Input{
		App:    failedApp(model.ConditionStarted, reasons.AppCrashed, "container app terminated: Error, exit code 1"),
		Events: []corev1.Event{event("e1", "BackOff", "Back-off restarting failed container"), event("e1", "BackOff", "duplicate name")},
		Logs:   []LogLine{prevLog(1, long), {Container: "app", N: 1, Text: "current run"}},
	}
	d := NewEngine(DefaultRules()...).Diagnose(in)
	seen := map[string]bool{}
	for _, e := range d.Bundle {
		if seen[e.Ref] {
			t.Errorf("ref %s twice", e.Ref)
		}
		seen[e.Ref] = true
		if n := utf8.RuneCountInString(e.Excerpt); n > MaxExcerpt {
			t.Errorf("%s excerpt has %d runes", e.Ref, n)
		}
		if !utf8.ValidString(e.Excerpt) {
			t.Errorf("%s excerpt split a rune", e.Ref)
		}
	}
	for _, ref := range []string{"condition/Started", "event/e1", "log/app/prev/1", "log/app/cur/1"} {
		if !seen[ref] {
			t.Errorf("bundle lacks %s: %v", ref, seen)
		}
	}
}

type fixedRule struct{ id, code string }

func (r fixedRule) ID() string { return r.id }
func (r fixedRule) Match(*Input) (Finding, bool) {
	return Finding{Code: reasons.Code(r.code)}, true
}

func TestRulesRunInRegistrationOrder(t *testing.T) {
	in := &Input{App: failedApp(model.ConditionStarted, reasons.AppCrashed)}
	d := NewEngine(fixedRule{"first", "NoCapacity"}, fixedRule{"second", "HomeBusy"}).Diagnose(in)
	if d.Rule != "first" || d.Code != reasons.NoCapacity {
		t.Fatalf("got %s %s, want the first rule", d.Rule, d.Code)
	}
}

func withGPU(s sizes.Size) sizes.Size {
	s.GPU, s.GPUResource = 1, sizes.DefaultGPUResource
	return s
}
