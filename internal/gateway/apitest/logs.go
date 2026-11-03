package apitest

import (
	"context"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

var T0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// KubeEvent is an event about object kind/name/uid, sec seconds after t0.
func KubeEvent(name, kind, obj string, uid types.UID, sec int, typ, reason, msg string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: gatewaytest.Namespace},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: obj, Namespace: gatewaytest.Namespace, UID: uid},
		Type:           typ, Reason: reason, Message: msg, Count: 1,
		LastTimestamp: metav1.NewTime(T0.Add(time.Duration(sec) * time.Second)),
	}
}

type EventJSON struct {
	Step, Type, Reason, Message string
	Time                        time.Time
	Count                       int32
}

// Running gives pod a started app container that restarted restarts times.
func Running(pod *corev1.Pod, restarts int32) *corev1.Pod {
	cs := corev1.ContainerStatus{Name: podspec.ContainerName, RestartCount: restarts,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	if restarts > 0 {
		cs.LastTerminationState.Terminated = &corev1.ContainerStateTerminated{ExitCode: 1}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
	return pod
}

// FakeLogs is a fake clientset whose pod log answer is body.
func FakeLogs(body string) *k8sfake.Clientset {
	cs := k8sfake.NewClientset()
	cs.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "log" {
			return false, nil, nil
		}
		return true, &runtime.Unknown{Raw: []byte(body)}, nil
	})
	return cs
}

// LogOpts returns the options of every pod log request made to cs.
func LogOpts(cs *k8sfake.Clientset) []*corev1.PodLogOptions {
	var out []*corev1.PodLogOptions
	for _, a := range cs.Actions() {
		if a.GetSubresource() == "log" {
			out = append(out, a.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions))
		}
	}
	return out
}

type LogsJSON struct {
	Lines []struct {
		Time *time.Time
		Text string
	}
	Restarts int32
}

// SiteApp is alice's static site from Git whose build failed: it has a Built
// step, and its pod a failed build init container.
func SiteApp() (*model.App, *corev1.Pod) {
	ws := gatewaytest.Workspace("site", "alice-aaaaaaaa", "Site")
	ws.Template = "static-site"
	ws.Status = model.AppStatus{Phase: "Failed", Conditions: []metav1.Condition{
		{Type: model.ConditionScheduled, Status: metav1.ConditionTrue, Reason: "Ready"},
		{Type: model.ConditionHomeReady, Status: metav1.ConditionTrue, Reason: "Ready"},
		{Type: model.ConditionImageReady, Status: metav1.ConditionTrue, Reason: "Ready"},
		{Type: model.ConditionBuilt, Status: metav1.ConditionFalse, Reason: "BuildFailed", Message: "container build terminated: Error, exit code 1\nnpm error code EUSAGE"},
		{Type: model.ConditionStarted, Status: metav1.ConditionUnknown, Reason: "Starting"},
		{Type: model.ConditionReady, Status: metav1.ConditionUnknown, Reason: "Starting"},
	}}
	pod := gatewaytest.LivePod("alice-aaaaaaaa", "site", "uid-site")
	for _, n := range []string{"clone", "source-check", "build", "site-copy", "site-config"} {
		pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{Name: n})
	}
	ok := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "clone", State: ok}, {Name: "source-check", State: ok},
		{Name: "build", RestartCount: 2, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}},
	}
	return ws, pod
}

// PipeLogs is a log stream that stays open until the request ends.
type PipeLogs struct {
	W      *io.PipeWriter
	Opts   chan *corev1.PodLogOptions
	Closed chan struct{}
}

func NewPipeLogs() *PipeLogs {
	return &PipeLogs{Opts: make(chan *corev1.PodLogOptions, 1), Closed: make(chan struct{})}
}

func (p *PipeLogs) Stream(ctx context.Context, _, _ string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	r, w := io.Pipe()
	p.W = w
	p.Opts <- opts
	go func() {
		<-ctx.Done()
		w.CloseWithError(ctx.Err())
		close(p.Closed)
	}()
	return r, nil
}

// HomePreparePod is the pod of the Job that readies owner's home.
func HomePreparePod(owner, name string, uid types.UID) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: gatewaytest.Namespace, UID: uid,
		Labels:          map[string]string{model.LabelRole: model.RoleHomePrepare, model.LabelOwner: owner},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "javo-prepare-home-" + owner, UID: types.UID("job-" + uid)}},
	}}
}
