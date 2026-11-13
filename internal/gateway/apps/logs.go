package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/store"
)

// Event is one Kubernetes event about an app's pod or home folder. Reason
// and message are the cluster's own words, shown as plain text.
type Event struct {
	// Step is the startup step type the event belongs to; absent when it
	// belongs to none.
	Step    string    `json:"step,omitempty" enum:"Scheduled,HomeReady,ImageReady,EnvReady,Built,Started,Ready"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int32     `json:"count"`
}

type eventListOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         struct {
		Items []Event `json:"items"`
	}
}

// eventSteps maps a pod event reason to its step. Failed and BackOff are
// shared by image pulls and container starts; eventStep splits them.
var eventSteps = map[string]string{
	"Scheduled":              model.ConditionScheduled,
	"FailedScheduling":       model.ConditionScheduled,
	"SuccessfulAttachVolume": model.ConditionHomeReady,
	"FailedAttachVolume":     model.ConditionHomeReady,
	"FailedMount":            model.ConditionHomeReady,
	"Pulling":                model.ConditionImageReady,
	"Pulled":                 model.ConditionImageReady,
	"Created":                model.ConditionStarted,
	"Started":                model.ConditionStarted,
	"Killing":                model.ConditionStarted,
	"Failed":                 model.ConditionStarted,
	"BackOff":                model.ConditionStarted,
	"Unhealthy":              model.ConditionReady,
}

// eventStep returns the step an event belongs to, or "" for none. Every
// event about the home claim is the home step; one about an init container
// of pod is the step that container's log is (containerStep).
func eventStep(e *corev1.Event, pod *corev1.Pod) string {
	if c, ok := strings.CutPrefix(e.InvolvedObject.FieldPath, "spec.initContainers{"); ok {
		return containerStep(pod, strings.TrimSuffix(c, "}"), true)
	}
	if e.InvolvedObject.Kind == "PersistentVolumeClaim" {
		return model.ConditionHomeReady
	}
	if (e.Reason == "Failed" || e.Reason == "BackOff") && strings.Contains(strings.ToLower(e.Message), "image") {
		return model.ConditionImageReady
	}
	return eventSteps[e.Reason]
}

// steppedEvent is an event with the startup step it belongs to.
type steppedEvent struct {
	ev   *corev1.Event
	step string
}

// eventOrder sorts events by the second they happened, then by startup
// step. The kubelet records whole seconds and the scheduler finer times,
// so a plain time sort can put a pull before the scheduling it followed.
func eventOrder(a, b steppedEvent) int {
	ta, tb := kube.EventTime(a.ev).Truncate(time.Second), kube.EventTime(b.ev).Truncate(time.Second)
	if c := ta.Compare(tb); c != 0 {
		return c
	}
	return stepRank(a.step) - stepRank(b.step)
}

// homeWarningsFirst moves the home step's warnings before its other events,
// each kind keeping its order, and leaves the other steps' events in place:
// a mount that keeps failing is otherwise buried under the claim's events.
func homeWarningsFirst(evs []steppedEvent) {
	var at []int
	var home []steppedEvent
	for i, e := range evs {
		if e.step == model.ConditionHomeReady {
			at, home = append(at, i), append(home, e)
		}
	}
	slices.SortStableFunc(home, func(a, b steppedEvent) int {
		return boolRank(a.ev.Type != corev1.EventTypeWarning) - boolRank(b.ev.Type != corev1.EventTypeWarning)
	})
	for i, e := range home {
		evs[at[i]] = e
	}
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// stepRank is a step's place in startup order; no step sorts last.
func stepRank(step string) int {
	if i := slices.Index(stepOrder, step); i >= 0 {
		return i
	}
	return len(stepOrder)
}

// AppLogs is the app container's recent log.
type AppLogs struct {
	Lines []podlogs.LogLine `json:"lines"`
	// Restarts is how often the container restarted; above 0 the previous
	// run's log can be read with previous=true.
	Restarts int32 `json:"restarts"`
}

type logsInput struct {
	Name     string `path:"name" maxLength:"63"`
	Owner    string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Tail     int64  `query:"tail" minimum:"1" maximum:"1000" default:"200" doc:"How many of the latest lines"`
	Previous bool   `query:"previous" doc:"Read the run before the last restart (after a crash)"`
	Step     string `query:"step" enum:"Started,Built" default:"Started" doc:"Whose log: the app's (Started) or its build's (Built)"`
}

type logsOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         AppLogs
}

// logStreamHeartbeat keeps idle log streams open through proxies.
const logStreamHeartbeat = 20 * time.Second

func (s *Workspaces) registerLogs(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "listWorkspaceEvents", Method: http.MethodGet, Path: "/api/workspaces/{name}/events",
		Summary: "Kubernetes events of an app's pod and home folder (with the Job that readies it), oldest first; the home step's warnings come before its other events", Tags: []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, s.events)

	huma.Register(api, huma.Operation{
		OperationID: "getWorkspaceLogs", Method: http.MethodGet, Path: "/api/workspaces/{name}/logs",
		Summary: "The latest lines the app wrote", Tags: []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, s.recentLogs)

	// huma's sse package cannot answer 404 before the stream starts, so the
	// stream is written here: one "line" event per log line, JSON encoded.
	huma.Register(api, s.cfg.Streams.Op(api, huma.Operation{
		OperationID: "streamWorkspaceLogs", Method: http.MethodGet, Path: "/api/workspaces/{name}/logs/stream",
		Summary: "Follow the app's log live (server-sent events, one line event per log line)", Tags: []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
		Responses: map[string]*huma.Response{"200": {
			Description: "Server-sent events: the latest lines, then new ones as they are written",
			Content: map[string]*huma.MediaType{"text/event-stream": {
				Schema: api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[podlogs.LogLine](), true, "LogLine"),
			}},
		}},
	}), s.streamLogs)

	s.registerStepLogs(api)
}

// stepContainer is the container whose log is step's: the app's, or for
// Built the build's (the site copy's when the pod builds nothing).
func stepContainer(pod *corev1.Pod, step string) string {
	switch {
	case step != model.ConditionBuilt:
		return podspec.ContainerName
	case pod != nil && !slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool { return c.Name == source.BuildContainer }):
		return source.SiteCopyContainer
	}
	return source.BuildContainer
}

// openLog opens the app's log, or returns nil when there is none to read.
func (s *Workspaces) openLog(ctx context.Context, in *logsInput, previous, follow bool) (io.ReadCloser, *corev1.Pod, error) {
	app, err := s.authorized(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
	if err != nil {
		return nil, nil, err
	}
	pod, err := s.pod(ctx, app)
	if err != nil {
		return nil, pod, err
	}
	rc, err := podlogs.Read(ctx, s.logs, pod, executor.LogOptions{
		Unit: stepContainer(pod, in.Step), Timestamps: true, Tail: in.Tail, Previous: previous, Follow: follow,
	})
	if err != nil {
		return nil, nil, err
	}
	return rc, pod, nil
}

func (s *Workspaces) recentLogs(ctx context.Context, in *logsInput) (*logsOutput, error) {
	rc, pod, err := s.openLog(ctx, in, in.Previous, false)
	if err != nil {
		return nil, err
	}
	out := &logsOutput{CacheControl: "no-store", Body: AppLogs{Lines: []podlogs.LogLine{}}}
	if cs := podlogs.ContainerStatus(pod, stepContainer(pod, in.Step)); cs != nil {
		out.Body.Restarts = cs.RestartCount
	}
	if rc == nil {
		return out, nil
	}
	defer rc.Close()
	sc := podlogs.Scanner(rc)
	for sc.Scan() {
		out.Body.Lines = append(out.Body.Lines, podlogs.ParseLine(sc.Text()))
	}
	return out, nil
}

func (s *Workspaces) streamLogs(ctx context.Context, in *logsInput) (*huma.StreamResponse, error) {
	rc, _, err := s.openLog(ctx, in, false, true)
	if err != nil {
		return nil, err
	}
	return &huma.StreamResponse{Body: func(hc huma.Context) {
		hc.SetHeader("Content-Type", "text/event-stream")
		hc.SetHeader("Cache-Control", "no-store")
		hc.SetHeader("X-Accel-Buffering", "no")
		hc.SetStatus(http.StatusOK)
		w := hc.BodyWriter()
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		// Reconnect after a while when the log ends (a restart, or no log yet).
		io.WriteString(w, "retry: 5000\n\n")
		flush()
		if rc == nil {
			return
		}
		defer rc.Close()
		lines := make(chan podlogs.LogLine)
		go func() {
			defer close(lines)
			sc := podlogs.Scanner(rc)
			for sc.Scan() {
				select {
				case lines <- podlogs.ParseLine(sc.Text()):
				case <-hc.Context().Done():
					return
				}
			}
		}()
		beat := time.NewTicker(logStreamHeartbeat)
		defer beat.Stop()
		for {
			select {
			case <-hc.Context().Done():
				return
			case <-beat.C:
				io.WriteString(w, ": keep-alive\n\n")
			case l, ok := <-lines:
				if !ok {
					return
				}
				b, _ := json.Marshal(l)
				fmt.Fprintf(w, "event: line\ndata: %s\n\n", b)
			}
			flush()
		}
	}}, nil
}

func (s *Workspaces) events(ctx context.Context, in *nameInput) (*eventListOutput, error) {
	app, err := s.authorized(ctx, in, authz.ActManageApp)
	if err != nil {
		return nil, err
	}
	out := &eventListOutput{CacheControl: "no-store"}
	out.Body.Items = []Event{}
	pod, err := s.pod(ctx, app)
	if err != nil {
		return nil, err
	}
	var evs []corev1.Event
	if pod != nil {
		if evs, err = s.eventsAbout(ctx, app.Namespace, "Pod", pod.Name, pod.UID); err != nil {
			return nil, err
		}
	}
	// The claim the owner's home is on: after a move between storage modes it
	// is not the first one.
	claimName := naming.HomeClaimName(app.Owner)
	switch home, err := s.db.Homes().ByOwner(ctx, app.Namespace, app.Owner); {
	case err == nil && home.Status.ClaimName != "":
		claimName = home.Status.ClaimName
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return nil, problem.Internal("read home", err)
	}
	var claim corev1.PersistentVolumeClaim
	claimKey := client.ObjectKey{Namespace: app.Namespace, Name: claimName}
	var homeEvs []corev1.Event
	switch err := s.k.Get(ctx, claimKey, &claim); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, problem.Internal("read home claim", err)
	default:
		if homeEvs, err = s.eventsAbout(ctx, app.Namespace, "PersistentVolumeClaim", claim.Name, claim.UID); err != nil {
			return nil, err
		}
	}
	prep, err := s.homePrepareEvents(ctx, app)
	if err != nil {
		return nil, err
	}
	homeEvs = append(homeEvs, prep...)
	all := make([]steppedEvent, 0, len(evs)+len(homeEvs))
	for i := range evs {
		all = append(all, steppedEvent{&evs[i], eventStep(&evs[i], pod)})
	}
	for i := range homeEvs {
		all = append(all, steppedEvent{&homeEvs[i], model.ConditionHomeReady})
	}
	slices.SortStableFunc(all, eventOrder)
	homeWarningsFirst(all)
	for _, se := range all {
		e := se.ev
		count := e.Count
		if e.Series != nil {
			count = e.Series.Count
		}
		out.Body.Items = append(out.Body.Items, Event{
			Step: se.step, Time: kube.EventTime(e).UTC(),
			Type: e.Type, Reason: e.Reason, Message: e.Message, Count: max(count, 1),
		})
	}
	return out, nil
}

// homePrepareEvents are the events of the Jobs that ready the owner's home
// and of their pods: a volume that cannot be mounted shows only there. The
// pods are found by their labels and each Job by its pod's owner reference,
// so the gateway needs no right to read Jobs.
func (s *Workspaces) homePrepareEvents(ctx context.Context, app *model.App) ([]corev1.Event, error) {
	var pods corev1.PodList
	if err := s.k.List(ctx, &pods, client.InNamespace(app.Namespace),
		client.MatchingLabels{model.LabelRole: model.RoleHomePrepare, model.LabelOwner: app.Owner}); err != nil {
		return nil, problem.Internal("list home prepare pods", err)
	}
	var out []corev1.Event
	jobs := map[types.UID]bool{}
	for _, p := range pods.Items {
		pe, err := s.eventsAbout(ctx, app.Namespace, "Pod", p.Name, p.UID)
		if err != nil {
			return nil, err
		}
		out = append(out, pe...)
		for _, ref := range p.OwnerReferences {
			if ref.Kind != "Job" || jobs[ref.UID] {
				continue
			}
			jobs[ref.UID] = true
			je, err := s.eventsAbout(ctx, app.Namespace, "Job", ref.Name, ref.UID)
			if err != nil {
				return nil, err
			}
			out = append(out, je...)
		}
	}
	return out, nil
}

// pod returns the app's live pod (named like the app), or nil.
func (s *Workspaces) pod(ctx context.Context, app *model.App) (*corev1.Pod, error) {
	var pod corev1.Pod
	err := s.k.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: app.ObjectName()}, &pod)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, problem.Internal("read app pod", err)
	}
	return &pod, nil
}

// eventsAbout lists the events about this exact object. Matching on UID
// keeps a deleted object's events from describing its same-named successor.
func (s *Workspaces) eventsAbout(ctx context.Context, namespace, kind, name string, uid types.UID) ([]corev1.Event, error) {
	var l corev1.EventList
	if err := s.k.List(ctx, &l, client.InNamespace(namespace), client.MatchingFields{"involvedObject.name": name}); err != nil {
		return nil, problem.Internal("list events", err)
	}
	out := l.Items[:0]
	for _, e := range l.Items {
		if e.InvolvedObject.Kind == kind && e.InvolvedObject.UID == uid {
			out = append(out, e)
		}
	}
	return out, nil
}
