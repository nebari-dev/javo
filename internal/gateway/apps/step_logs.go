package apps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/status"
)

// StepLogRecord is one record of a startup step's live log: a container's
// log starts, one of its lines, how a run of it ended, why it waits, or
// that it restarted. Text is untrusted plain text.
type StepLogRecord struct {
	Kind string `json:"kind" enum:"start,line,exit,waiting,restart"`
	// Container is whose log this is: a container of the app's pod, or of
	// the Job pod that readies the home.
	Container string `json:"container"`
	// Time is when a line was written; absent when the cluster gave none.
	Time *time.Time `json:"time,omitempty"`
	// Text is a line, or the message of an exit or a wait.
	Text string `json:"text,omitempty"`
	// Reason is the cluster's reason for an exit or a wait.
	Reason   string `json:"reason,omitempty"`
	ExitCode *int32 `json:"exitCode,omitempty"`
	// Restarts is the container's restart count, on a restart record.
	Restarts int32 `json:"restarts,omitempty"`
}

type stepLogInput struct {
	Name  string `path:"name" maxLength:"63"`
	Step  string `path:"step" enum:"Scheduled,HomeReady,ImageReady,EnvReady,Built,Started,Ready"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
}

// StepPollEvery is how often a step's stream reads the pod again while a
// container has not started, or waits to be restarted. Tests shorten it.
var StepPollEvery = time.Second

func (s *Workspaces) registerStepLogs(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "streamWorkspaceStepLog", Method: http.MethodGet, Path: "/api/workspaces/{name}/steps/{step}/log",
		Summary: "Follow the log of the containers doing a startup step's work, one after another, as they write it " +
			"(newline-delimited JSON records; ends once every one of them has finished)",
		Tags:   []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
		Responses: map[string]*huma.Response{"200": {
			Description: "One JSON record per line; a blank line keeps an idle stream open",
			Content: map[string]*huma.MediaType{"application/x-ndjson": {
				Schema: api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[StepLogRecord](), true, "StepLogRecord"),
			}},
		}},
	}, s.stepLog)
}

func (s *Workspaces) stepLog(ctx context.Context, in *stepLogInput) (*huma.StreamResponse, error) {
	app, err := s.authorized(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
	if err != nil {
		return nil, err
	}
	if s.logs == nil {
		return nil, problem.Internal("read pod log", errors.New("no pod log reader configured"))
	}
	return &huma.StreamResponse{Body: func(hc huma.Context) {
		hc.SetHeader("Content-Type", "application/x-ndjson")
		hc.SetHeader("Cache-Control", "no-store")
		hc.SetHeader("X-Accel-Buffering", "no")
		hc.SetStatus(http.StatusOK)
		w := &lockedWriter{w: hc.BodyWriter()}
		w.flush() // send the headers now: a step may stay quiet for a while
		ctx, stop := context.WithCancel(hc.Context())
		defer stop()
		go func() {
			beat := time.NewTicker(logStreamHeartbeat)
			defer beat.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-beat.C:
					if w.write([]byte("\n")) != nil {
						stop()
					}
				}
			}
		}()
		f := &stepFollower{s: s, app: app, step: in.Step, emit: func(r StepLogRecord) error {
			b, _ := json.Marshal(r)
			return w.write(append(b, '\n'))
		}}
		f.run(ctx)
	}}, nil
}

// lockedWriter writes and flushes whole records from two goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) write(b []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(b); err != nil {
		return err
	}
	l.flush()
	return nil
}

func (l *lockedWriter) flush() {
	if f, ok := l.w.(http.Flusher); ok {
		f.Flush()
	}
}

// containerStep is the startup step a container of the app's pod does its
// work in: the app's own container starts it; the environment install, and
// on a pod with one or a build the clone, are status.InitStep's; the other
// init containers (the platform's files, the sample copy) fill the home.
func containerStep(pod *corev1.Pod, name string, init bool) string {
	switch {
	case !init:
		return model.ConditionStarted
	case name == pixienv.InstallContainer || name == pixienv.ToolsContainer || source.IsSiteStep(name):
		return status.InitStep(pod, name)
	}
	return model.ConditionHomeReady
}

// stepContainers names the containers of pod whose log is step's, in the
// order they run.
func stepContainers(pod *corev1.Pod, step string) []string {
	var out []string
	if pod == nil {
		return nil
	}
	for _, c := range pod.Spec.InitContainers {
		if containerStep(pod, c.Name, true) == step {
			out = append(out, c.Name)
		}
	}
	for _, c := range pod.Spec.Containers {
		if containerStep(pod, c.Name, false) == step {
			out = append(out, c.Name)
		}
	}
	return out
}

// logSource is one container whose log a step shows.
type logSource struct {
	pod       *corev1.Pod
	container string
}

func (l logSource) key() string { return string(l.pod.UID) + "/" + l.container }

// runState is what a step's stream has sent about one run of a container
// (a run is its restart count).
type runState struct {
	read, exitShown, waitShown bool
	// last is the time of the last line sent, so a log opened again after
	// a dropped connection does not repeat lines.
	last time.Time
}

// sourceState is what a step's stream has sent about one container.
type sourceState struct {
	started bool
	run     int32
	runs    map[int32]*runState
	done    bool
}

func (st *sourceState) of(run int32) *runState {
	if st.runs[run] == nil {
		st.runs[run] = &runState{}
	}
	return st.runs[run]
}

// stepFollower writes the log of a step's containers to emit, one after
// another in the order they run: it waits for each to start (reading the
// pod again every StepPollEvery), follows its log while it runs, says how
// each run ended and when it restarts, and moves on once it has finished
// well. It returns once every container has finished, the pod is replaced
// or gone, or the client leaves.
type stepFollower struct {
	s    *Workspaces
	app  *model.App
	step string
	emit func(StepLogRecord) error
}

func (f *stepFollower) run(ctx context.Context) {
	states := map[string]*sourceState{}
	var podUID types.UID
	for ctx.Err() == nil {
		srcs, uid, err := f.sources(ctx)
		if err != nil {
			return
		}
		if podUID != "" && uid != podUID {
			return // the pod was replaced: the client opens the new one's
		}
		podUID = uid
		pending, again := false, false
		for _, src := range srcs {
			st := states[src.key()]
			if st == nil {
				st = &sourceState{runs: map[int32]*runState{}}
				states[src.key()] = st
			}
			if st.done {
				continue
			}
			advanced, err := f.advance(ctx, src, st)
			if err != nil {
				return
			}
			if !st.done {
				// Later containers run after this one.
				pending, again = true, advanced
				break
			}
		}
		if !pending {
			return
		}
		if again {
			continue // a log ended: read the pod again right away
		}
		select {
		case <-ctx.Done():
		case <-time.After(StepPollEvery):
		}
	}
}

// advance sends what is new about one container: its start, its log (followed
// while it runs), how a run ended, a wait or a restart. advanced is true
// when it read a log, so the pod is read again without waiting.
func (f *stepFollower) advance(ctx context.Context, src logSource, st *sourceState) (advanced bool, err error) {
	cs := podlogs.ContainerStatus(src.pod, src.container)
	if cs == nil || (cs.State.Running == nil && cs.State.Terminated == nil && cs.LastTerminationState.Terminated == nil) {
		return false, nil // not started yet
	}
	if !st.started {
		st.started, st.run = true, cs.RestartCount
		if err := f.emit(StepLogRecord{Kind: "start", Container: src.container}); err != nil {
			return false, err
		}
		// A container that runs again after a crash: first the run that
		// crashed, how it ended, and the restart.
		if cs.State.Running != nil && cs.RestartCount > 0 && cs.LastTerminationState.Terminated != nil {
			prev := st.of(cs.RestartCount - 1)
			if err := f.copyLog(ctx, src, prev, true, false); err != nil {
				return false, err
			}
			if err := f.emit(exitRecord(src.container, cs.LastTerminationState.Terminated)); err != nil {
				return false, err
			}
			prev.exitShown = true
			if err := f.emit(StepLogRecord{Kind: "restart", Container: src.container, Restarts: cs.RestartCount}); err != nil {
				return false, err
			}
		}
	}
	if cs.RestartCount > st.run {
		st.run = cs.RestartCount
		if cs.State.Running != nil {
			if err := f.emit(StepLogRecord{Kind: "restart", Container: src.container, Restarts: cs.RestartCount}); err != nil {
				return false, err
			}
		}
	}
	rs := st.of(cs.RestartCount)
	switch {
	case cs.State.Running != nil:
		// Followed until the container stops (or the connection drops);
		// then the pod is read again.
		rs.read = true
		return true, f.copyLog(ctx, src, rs, false, true)
	case cs.State.Terminated != nil:
		if !rs.read {
			if err := f.copyLog(ctx, src, rs, false, false); err != nil {
				return false, err
			}
			rs.read = true
		}
		if !rs.exitShown {
			rs.exitShown = true
			if err := f.emit(exitRecord(src.container, cs.State.Terminated)); err != nil {
				return false, err
			}
		}
		// Finished well, or for good: a pod that ended restarts nothing.
		st.done = cs.State.Terminated.ExitCode == 0 || src.pod.Status.Phase == corev1.PodFailed || src.pod.Status.Phase == corev1.PodSucceeded
		return false, nil
	default:
		// Waiting to run again (CrashLoopBackOff): the kubelet serves the
		// run that just ended as the current log.
		if !rs.read {
			if err := f.copyLog(ctx, src, rs, false, false); err != nil {
				return false, err
			}
			rs.read = true
		}
		if !rs.exitShown {
			rs.exitShown = true
			if err := f.emit(exitRecord(src.container, cs.LastTerminationState.Terminated)); err != nil {
				return false, err
			}
		}
		if w := cs.State.Waiting; w != nil && !rs.waitShown {
			rs.waitShown = true
			if err := f.emit(StepLogRecord{Kind: "waiting", Container: src.container, Reason: w.Reason, Text: w.Message}); err != nil {
				return false, err
			}
		}
		return false, nil
	}
}

func exitRecord(container string, t *corev1.ContainerStateTerminated) StepLogRecord {
	code := t.ExitCode
	return StepLogRecord{Kind: "exit", Container: container, ExitCode: &code, Reason: t.Reason, Text: t.Message}
}

// copyLog sends the lines of one run of a container: the run before the
// last restart (previous), or the current one, followed while it runs. Lines
// up to rs.last were sent already.
func (f *stepFollower) copyLog(ctx context.Context, src logSource, rs *runState, previous, follow bool) error {
	opts := &corev1.PodLogOptions{Container: src.container, Timestamps: true, Previous: previous, Follow: follow}
	if !rs.last.IsZero() {
		opts.SinceTime = &metav1.Time{Time: rs.last.Truncate(time.Second)}
	}
	rc, err := podlogs.Open(ctx, f.s.logs, src.pod, opts)
	if err != nil || rc == nil {
		return ctx.Err()
	}
	defer rc.Close()
	sc := podlogs.Scanner(rc)
	for sc.Scan() {
		l := podlogs.ParseLine(sc.Text())
		if l.Time != nil {
			if !l.Time.After(rs.last) {
				continue
			}
			rs.last = *l.Time
		}
		if err := f.emit(StepLogRecord{Kind: "line", Container: src.container, Time: l.Time, Text: l.Text}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// sources are the containers whose log is the step's, in the order they
// run, and the UID of the app's pod ("" without one). The home step starts
// with the Job pods that ready the home or copy it to another storage mode,
// oldest first.
func (f *stepFollower) sources(ctx context.Context) ([]logSource, types.UID, error) {
	pod, err := f.s.pod(ctx, f.app)
	if err != nil {
		return nil, "", err
	}
	var out []logSource
	if f.step == model.ConditionHomeReady {
		// The Jobs that ready the home and copy it to another storage mode.
		var jobPods []corev1.Pod
		for _, role := range []string{model.RoleHomePrepare, model.RoleHomeMigrate} {
			var pods corev1.PodList
			if err := f.s.k.List(ctx, &pods, client.InNamespace(f.app.Namespace),
				client.MatchingLabels{model.LabelRole: role, model.LabelOwner: f.app.Owner}); err != nil {
				return nil, "", err
			}
			jobPods = append(jobPods, pods.Items...)
		}
		slices.SortStableFunc(jobPods, func(a, b corev1.Pod) int { return a.CreationTimestamp.Compare(b.CreationTimestamp.Time) })
		for i := range jobPods {
			p := &jobPods[i]
			for _, c := range p.Spec.Containers {
				out = append(out, logSource{pod: p, container: c.Name})
			}
		}
	}
	if pod == nil {
		return out, "", nil
	}
	for _, c := range stepContainers(pod, f.step) {
		out = append(out, logSource{pod: pod, container: c})
	}
	return out, pod.UID, nil
}
