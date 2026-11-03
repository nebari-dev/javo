package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/command"
	"github.com/nebari-dev/javo/internal/jobs/engines/papermill"
	"github.com/nebari-dev/javo/internal/jobs/runner"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
)

type call struct {
	argv []string
	dir  string
	env  []string
}

type harness struct {
	home, term string
	calls      []call
	exec       func(n int, argv []string, dir string, out io.Writer) (int, error)
}

func (h *harness) cfg(t *testing.T, runID string) runner.Config {
	t.Helper()
	reg := registry.New[engine.Engine]()
	reg.Register(papermill.New())
	reg.Register(command.New())
	return runner.Config{
		Home: h.home, RunID: runID, TerminationLog: h.term, Registry: reg, Log: io.Discard,
		Exec: func(_ context.Context, argv []string, dir string, env []string, out io.Writer) (int, error) {
			h.calls = append(h.calls, call{argv, dir, env})
			return h.exec(len(h.calls)-1, argv, dir, out)
		},
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := t.TempDir()
	return &harness{home: filepath.Join(d, "home"), term: filepath.Join(d, "termination-log")}
}

func (h *harness) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(h.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) termination(t *testing.T) (m struct {
	Reason, Message, OutputPath string
	Outputs                     []string
}, raw []byte) {
	t.Helper()
	raw, err := os.ReadFile(h.term)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("termination message is not JSON: %v: %q", err, raw)
	}
	return m, raw
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestMissingSourceFailsWithSourceMissingAndLeavesOnlyResultAndLog(t *testing.T) {
	h := newHarness(t)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	code := runner.Run(t.Context(), h.cfg(t, "run-1"), runner.Spec{
		Definition: "sales", Engine: "papermill", Source: "reports/sales.ipynb", Kernel: "python3",
	})
	if code != runner.ExitSourceMissing {
		t.Fatalf("exit = %d, want %d", code, runner.ExitSourceMissing)
	}
	if len(h.calls) != 0 {
		t.Errorf("Exec called %d times", len(h.calls))
	}
	m, _ := h.termination(t)
	if m.Reason != "SourceMissing" || !strings.Contains(m.Message, "reports/sales.ipynb") {
		t.Errorf("termination = %+v", m)
	}
	runDir := filepath.Join(h.home, "jobs", "sales", "run-1")
	if got := names(t, runDir); !slices.Equal(got, []string{"result.json", "run.log"}) {
		t.Errorf("run folder = %v", got)
	}
}

func TestSourceOutsideHomeIsMissing(t *testing.T) {
	h := newHarness(t)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	if err := os.WriteFile(filepath.Join(filepath.Dir(h.home), "secret.ipynb"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "papermill", Source: "../secret.ipynb"})
	if code != runner.ExitSourceMissing || len(h.calls) != 0 {
		t.Errorf("exit = %d, calls = %d", code, len(h.calls))
	}
}

func TestEngineBinaryNotFoundIsATemplateError(t *testing.T) {
	h := newHarness(t)
	h.write(t, "a.ipynb", "{}")
	h.exec = func(int, []string, string, io.Writer) (int, error) {
		return 0, &exec.Error{Name: "papermill", Err: exec.ErrNotFound}
	}
	code := runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "papermill", Source: "a.ipynb", Kernel: "python3"})
	if code != runner.ExitTemplateError {
		t.Fatalf("exit = %d, want %d", code, runner.ExitTemplateError)
	}
	if m, _ := h.termination(t); m.Reason != "EngineMissing" || !strings.Contains(m.Message, "papermill") {
		t.Errorf("termination = %+v", m)
	}
}

func TestUnknownEngineIsATemplateError(t *testing.T) {
	h := newHarness(t)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	code := runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "nope"})
	if code != runner.ExitTemplateError {
		t.Fatalf("exit = %d", code)
	}
	if m, _ := h.termination(t); m.Reason != "EngineMissing" {
		t.Errorf("reason = %s", m.Reason)
	}
}

func TestPapermillCellErrorFailsOnceAndStillRendersHTML(t *testing.T) {
	h := newHarness(t)
	h.write(t, "a.ipynb", "{}")
	failed, err := os.ReadFile("../engines/papermill/testdata/error-cell.out.ipynb")
	if err != nil {
		t.Fatal(err)
	}
	h.exec = func(n int, argv []string, dir string, out io.Writer) (int, error) {
		if n == 0 {
			os.WriteFile(filepath.Join(dir, "output.ipynb"), failed, 0o644)
			io.WriteString(out, "KeyError: 'region'\n")
			return 1, nil
		}
		os.WriteFile(filepath.Join(dir, "output.html"), []byte("<html>"), 0o644)
		return 0, nil
	}
	code := runner.Run(t.Context(), h.cfg(t, "r1"), runner.Spec{Definition: "d", Engine: "papermill", Source: "a.ipynb", Kernel: "python3"})
	if code != runner.ExitUserError {
		t.Fatalf("exit = %d, want %d", code, runner.ExitUserError)
	}
	if len(h.calls) != 2 {
		t.Fatalf("steps run = %d, want 2", len(h.calls))
	}
	runDir := filepath.Join(h.home, "jobs", "d", "r1")
	if h.calls[0].dir != runDir {
		t.Errorf("working dir = %s", h.calls[0].dir)
	}
	var res struct {
		Outcome, Reason, Message string
		Outputs                  []string
		StartedAt, FinishedAt    time.Time
	}
	b, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "Failed" || res.Reason != "CellError" || !strings.Contains(res.Message, "Cell") {
		t.Errorf("result = %+v", res)
	}
	slices.Sort(res.Outputs)
	if !slices.Equal(res.Outputs, []string{"output.html", "output.ipynb"}) {
		t.Errorf("outputs = %v", res.Outputs)
	}
	if res.FinishedAt.Before(res.StartedAt) || res.StartedAt.IsZero() {
		t.Errorf("times = %v %v", res.StartedAt, res.FinishedAt)
	}
	m, _ := h.termination(t)
	if m.Reason != "CellError" || m.OutputPath != "jobs/d/r1" {
		t.Errorf("termination = %+v", m)
	}
	// The status and the UI link these files, so the message names them.
	slices.Sort(m.Outputs)
	if !slices.Equal(m.Outputs, []string{"output.html", "output.ipynb"}) {
		t.Errorf("termination outputs = %v", m.Outputs)
	}
	if log, _ := os.ReadFile(filepath.Join(runDir, "run.log")); !strings.Contains(string(log), "KeyError") {
		t.Errorf("run.log = %q", log)
	}
}

func TestSuccessSnapshotsSourceAndWritesTypedParams(t *testing.T) {
	h := newHarness(t)
	h.write(t, "a.ipynb", `{"cells":[]}`)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	code := runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{
		Definition: "d", Engine: "papermill", Source: "a.ipynb", Kernel: "python3",
		Params: []runner.Param{
			{Name: "n", Type: "integer", Value: "4"},
			{Name: "rate", Type: "number", Value: "0.5"},
			{Name: "on", Type: "boolean", Value: "true"},
			{Name: "who", Type: "string", Value: "4"},
		},
	})
	if code != runner.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	runDir := filepath.Join(h.home, "jobs", "d", "r")
	if b, _ := os.ReadFile(filepath.Join(runDir, "input.ipynb")); string(b) != `{"cells":[]}` {
		t.Errorf("snapshot = %q", b)
	}
	b, _ := os.ReadFile(filepath.Join(runDir, "params.yaml"))
	var got map[string]any
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"n": float64(4), "on": true, "rate": 0.5, "who": "4"}
	if !maps.Equal(got, want) {
		t.Errorf("params.yaml = %q parsed as %#v, want %#v", b, got, want)
	}
	if got := h.calls[0].argv[1]; got != filepath.Join(runDir, "input.ipynb") {
		t.Errorf("engine source = %s", got)
	}
	if m, _ := h.termination(t); m.Reason != "Succeeded" {
		t.Errorf("termination = %+v", m)
	}
}

func TestInvalidParameterNeverRunsTheEngine(t *testing.T) {
	h := newHarness(t)
	h.write(t, "a.ipynb", "{}")
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	code := runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{
		Definition: "d", Engine: "papermill", Source: "a.ipynb", Kernel: "python3",
		Params: []runner.Param{{Name: "n", Type: "integer", Value: "4x"}},
	})
	if code != runner.ExitParams || len(h.calls) != 0 {
		t.Fatalf("exit = %d, calls = %d", code, len(h.calls))
	}
	m, _ := h.termination(t)
	if m.Reason != "ParametersInvalid" || !strings.Contains(m.Message, "n") {
		t.Errorf("termination = %+v", m)
	}
}

func TestKeepRunsPrunesOldestSiblingsButNotTheCurrentRun(t *testing.T) {
	h := newHarness(t)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	base := time.Now().Add(-time.Hour)
	for i, n := range []string{"old-1", "old-2", "old-3"} {
		h.write(t, filepath.Join("jobs", "d", n, "result.json"), "{}")
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(filepath.Join(h.home, "jobs", "d", n), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	code := runner.Run(t.Context(), h.cfg(t, "current"), runner.Spec{
		Definition: "d", Engine: "command", Command: []string{"true"}, KeepRuns: 2,
	})
	if code != runner.ExitOK {
		t.Fatalf("exit = %d", code)
	}
	got := names(t, filepath.Join(h.home, "jobs", "d"))
	slices.Sort(got)
	if want := []string{"current", "old-2", "old-3"}; !slices.Equal(got, want) {
		t.Errorf("run folders = %v, want %v", got, want)
	}
}

func TestTerminationMessageIsBoundedValidJSON(t *testing.T) {
	h := newHarness(t)
	h.exec = func(_ int, _ []string, _ string, out io.Writer) (int, error) {
		io.WriteString(out, strings.Repeat("é", 10*1024)+"\n")
		return 3, nil
	}
	code := runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "command", Command: []string{"x"}})
	if code != runner.ExitUserError {
		t.Fatalf("exit = %d", code)
	}
	m, raw := h.termination(t)
	if len(raw) > 4096 {
		t.Errorf("termination message is %d bytes", len(raw))
	}
	if m.Reason != "CommandFailed" || !strings.HasPrefix(m.Message, "The command exited with code 3") {
		t.Errorf("termination = %.80q", m.Message)
	}
}

func TestCommandEngineGetsParamsInTheEnvironment(t *testing.T) {
	h := newHarness(t)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{
		Definition: "d", Engine: "command", Command: []string{"echo"},
		Params: []runner.Param{{Name: "n", Type: "integer", Value: "4"}},
	})
	if !slices.Contains(h.calls[0].env, "JAVO_PARAM_N=4") {
		t.Errorf("env lacks JAVO_PARAM_N=4")
	}
}

func TestDefaultExecRunsArgvWithoutAShell(t *testing.T) {
	var out strings.Builder
	code, err := runner.DefaultExec(t.Context(), []string{"sh", "-c", `echo "$0"; exit 3`, "a b;c"}, t.TempDir(), nil, &out)
	if err != nil || code != 3 || out.String() != "a b;c\n" {
		t.Errorf("code = %d, err = %v, out = %q", code, err, out.String())
	}
	_, err = runner.DefaultExec(t.Context(), []string{"javo-no-such-binary"}, t.TempDir(), nil, io.Discard)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound", err)
	}
}

func TestUnsafeDefinitionOrRunIDNeverPrunesOutsideTheDefinitionFolder(t *testing.T) {
	cases := []struct{ name, def, runID string }{
		{"empty definition", "", "r"},
		{"dotdot definition", "..", "r"},
		{"nested definition", "a/b", "r"},
		{"empty run id", "d", ""},
		{"dotdot run id", "d", ".."},
		{"nested run id", "d", "a/b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
			for _, p := range []string{"jobs/other/run-1", "jobs/other/run-2", "jobs/other/run-3", "docs/keep-1", "docs/keep-2", "docs/keep-3"} {
				h.write(t, filepath.Join(p, "f"), "x")
			}
			code := runner.Run(t.Context(), h.cfg(t, c.runID), runner.Spec{
				Definition: c.def, Engine: "command", Command: []string{"true"}, KeepRuns: 1,
			})
			if code != runner.ExitTemplateError {
				t.Errorf("exit = %d, want %d", code, runner.ExitTemplateError)
			}
			if len(h.calls) != 0 {
				t.Errorf("Exec called %d times", len(h.calls))
			}
			m, _ := h.termination(t)
			if m.Reason != "TemplateInvalid" {
				t.Errorf("termination = %+v", m)
			}
			for _, d := range []string{"jobs/other", "docs"} {
				if got := names(t, filepath.Join(h.home, d)); len(got) != 3 {
					t.Errorf("%s = %v, want 3 folders untouched", d, got)
				}
			}
		})
	}
}

func TestCancelledContextStillRecordsResultAndTerminationMessage(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	h.exec = func(int, []string, string, io.Writer) (int, error) {
		cancel() // SIGTERM arrives mid-run; the engine is killed and reports -1
		return -1, nil
	}
	code := runner.Run(ctx, h.cfg(t, "run-1"), runner.Spec{Definition: "d", Engine: "command", Command: []string{"sleep", "100"}})
	if code == runner.ExitOK {
		t.Fatalf("exit = %d, want a failure", code)
	}
	m, _ := h.termination(t)
	if m.Reason != "RunInterrupted" {
		t.Errorf("termination = %+v, want reason RunInterrupted", m)
	}
	runDir := filepath.Join(h.home, "jobs", "d", "run-1")
	b, err := os.ReadFile(filepath.Join(runDir, "result.json"))
	if err != nil || !strings.Contains(string(b), `"RunInterrupted"`) {
		t.Errorf("result.json = %q, err %v", b, err)
	}
}

func TestCancelledTwoStepPapermillRunIsInterruptedNotCommandFailed(t *testing.T) {
	h := newHarness(t)
	h.write(t, "a.ipynb", "{}")
	ctx, cancel := context.WithCancel(t.Context())
	h.exec = func(n int, _ []string, _ string, _ io.Writer) (int, error) {
		if n == 0 {
			cancel() // SIGTERM arrives during the execute step
			return -1, nil
		}
		return 0, context.Canceled // exec.CommandContext cannot start on a cancelled context
	}
	runner.Run(ctx, h.cfg(t, "r1"), runner.Spec{Definition: "d", Engine: "papermill", Source: "a.ipynb", Kernel: "python3"})
	if m, _ := h.termination(t); m.Reason != "RunInterrupted" {
		t.Errorf("termination = %+v, want reason RunInterrupted", m)
	}
	if len(h.calls) != 1 {
		t.Errorf("steps started = %d, want 1 (no step starts after SIGTERM)", len(h.calls))
	}
}

func TestDefaultExecCancelKillsBackgroundedChildrenAndReturns(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(300*time.Millisecond, cancel)
	var out strings.Builder
	start := time.Now()
	runner.DefaultExec(ctx, []string{"sh", "-c", "sleep 30 & echo $!; wait"}, t.TempDir(), nil, &out)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("DefaultExec returned after %v; a child holding stdout blocked it", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatalf("child pid output = %q", out.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("backgrounded child %d still alive after cancel", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The runner writes the termination message and DeriveRun reads it; a
// mismatch would show every failed run as Lost.
func TestTerminationMessageReadsBackAsTheRunsFailure(t *testing.T) {
	h := newHarness(t)
	h.exec = func(_ int, _ []string, _ string, out io.Writer) (int, error) {
		io.WriteString(out, "boom\n")
		return 3, nil
	}
	runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "command", Command: []string{"x"}})
	_, raw := h.termination(t)

	job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}}}
	pod := corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3, Message: string(raw)}}}}}}
	run := jobstatus.DeriveRun(job, []corev1.Pod{pod}, nil, metav1.Now().Time)
	if run.Outcome != "Failed" || run.Reason != "CommandFailed" {
		t.Errorf("run = %+v, want Failed/CommandFailed", run)
	}
}

// /dev/full answers every write with ENOSPC, like a full home volume.
func TestFullDiskOnResultJSONFailsWithOutputWriteFailed(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("no /dev/full on this OS")
	}
	h := newHarness(t)
	cfg := h.cfg(t, "r")
	// The command engine runs in the home folder; result.json is written in
	// the run folder, so that is where the full disk goes.
	h.exec = func(int, []string, string, io.Writer) (int, error) {
		return 0, os.Symlink("/dev/full", filepath.Join(cfg.Home, "jobs", "d", "r", "result.json"))
	}
	code := runner.Run(t.Context(), cfg, runner.Spec{Definition: "d", Engine: "command", Command: []string{"x"}})
	if code != runner.ExitOutputWrite {
		t.Errorf("exit = %d, want %d", code, runner.ExitOutputWrite)
	}
	if m, _ := h.termination(t); m.Reason != "OutputWriteFailed" {
		t.Errorf("termination = %+v, want reason OutputWriteFailed", m)
	}
}

func TestTerminationLogWriteErrorGoesToStderr(t *testing.T) {
	h := newHarness(t)
	h.term = filepath.Join(h.term, "missing-dir", "termination-log")
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "command", Command: []string{"x"}})
	runner.Fail(h.term, "TemplateInvalid", "bad")
	w.Close()
	os.Stderr = old
	got, _ := io.ReadAll(r)
	for _, want := range []string{"termination message (Succeeded", "termination message (TemplateInvalid: bad)"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("stderr = %q, want it to contain %q", got, want)
		}
	}
}

// A command job runs where the person's files are, like an app's typed
// command; its log and result still go to the run folder.
func TestCommandEngineRunsInTheHomeFolder(t *testing.T) {
	h := newHarness(t)
	h.exec = func(int, []string, string, io.Writer) (int, error) { return 0, nil }
	runner.Run(t.Context(), h.cfg(t, "r"), runner.Spec{Definition: "d", Engine: "command", Command: []string{"ls"}})
	if len(h.calls) != 1 || h.calls[0].dir != h.home {
		t.Fatalf("%d calls, first in %v, want one in %s", len(h.calls), func() any {
			if len(h.calls) > 0 {
				return h.calls[0].dir
			}
			return nil
		}(), h.home)
	}
	if got := names(t, filepath.Join(h.home, "jobs", "d", "r")); !slices.Equal(got, []string{"params.yaml", "result.json", "run.log"}) {
		t.Errorf("run folder = %v", got)
	}
}

// An image that carries pod-user.sh gets every step run through it, so the
// engine (and a typed command's shell) can look up the pod's uid.
func TestWithPodUserRunsStepsThroughTheImagesScript(t *testing.T) {
	d := t.TempDir()
	script := filepath.Join(d, "pod-user.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho through-pod-user\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := runner.WithPodUser(script, runner.DefaultExec)

	var out strings.Builder
	code, err := run(t.Context(), []string{"sh", "-c", "echo step; exit 3"}, d, nil, &out)
	if err != nil || code != 3 || out.String() != "through-pod-user\nstep\n" {
		t.Errorf("code = %d, err = %v, out = %q", code, err, out.String())
	}
	// A missing engine is still reported as missing, not as the script's failure.
	_, err = run(t.Context(), []string{"javo-no-such-binary"}, d, nil, io.Discard)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound", err)
	}
}

// A program the pod's environment provides is on the PATH only once
// pod-user.sh has activated the environment: it is found there, not in the
// runner's own PATH.
func TestWithPodUserFindsProgramsOfThePodsEnvironment(t *testing.T) {
	d := t.TempDir()
	envBin := filepath.Join(d, "env", "bin")
	if err := os.MkdirAll(envBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envBin, "papermill-stub"), []byte("#!/bin/sh\necho from-env\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(d, "pod-user.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nPATH="+envBin+":$PATH\nexport PATH\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	code, err := runner.WithPodUser(script, runner.DefaultExec)(t.Context(), []string{"papermill-stub"}, d, os.Environ(), &out)
	if err != nil || code != 0 || out.String() != "from-env\n" {
		t.Errorf("code = %d, err = %v, out = %q", code, err, out.String())
	}
}

func TestWithPodUserRunsStepsDirectlyWhenTheImageHasNoScript(t *testing.T) {
	run := runner.WithPodUser(filepath.Join(t.TempDir(), "pod-user.sh"), runner.DefaultExec)
	var out strings.Builder
	code, err := run(t.Context(), []string{"sh", "-c", "echo step"}, t.TempDir(), nil, &out)
	if err != nil || code != 0 || out.String() != "step\n" {
		t.Errorf("code = %d, err = %v, out = %q", code, err, out.String())
	}
}

func TestCopyFileCreatesWithGivenMode(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "src")
	if err := os.WriteFile(src, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(d, "dst")
	if err := runner.CopyFile(src, dst, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	info, _ := os.Stat(dst)
	if string(b) != "hi" || info.Mode().Perm() != 0o755 {
		t.Errorf("content %q mode %v", b, info.Mode().Perm())
	}
}
