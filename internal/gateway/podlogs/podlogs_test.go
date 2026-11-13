package podlogs

import (
	"context"
	"io"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// asked is an executor whose Logs records what it was asked for.
type asked struct {
	executor.Executor
	ref  workload.Ref
	opts []executor.LogOptions
	err  error
}

func (a *asked) Logs(_ context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	a.ref, a.opts = ref, append(a.opts, o)
	return io.NopCloser(strings.NewReader("line\n")), a.err
}

func appPod(statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "javo", Name: "alice-sales", Labels: map[string]string{model.LabelApp: "app-1", model.LabelOwner: "alice"}},
		Status:     corev1.PodStatus{ContainerStatuses: statuses},
	}
}

var runningApp = corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}

// A read that does not follow is capped in size; the app's own unit is
// read unless another is named.
func TestReadAsksForTheAppsLogCapped(t *testing.T) {
	logs := &asked{}
	ctx := context.Background()
	for _, o := range []executor.LogOptions{{Tail: 10}, {Follow: true}, {MaxBytes: 99}} {
		rc, err := Read(ctx, logs, appPod(runningApp), o)
		if err != nil || rc == nil {
			t.Fatalf("Read = %v, %v", rc, err)
		}
	}
	if want := (workload.Ref{Namespace: "javo", Name: "alice-sales", ID: "app-1", Owner: "alice"}); logs.ref != want {
		t.Errorf("asked for the log of %+v", logs.ref)
	}
	capped, followed, own := logs.opts[0], logs.opts[1], logs.opts[2]
	if capped.Unit != "app" || capped.Tail != 10 || capped.MaxBytes != maxLogBytes {
		t.Errorf("a plain read asked for %+v", capped)
	}
	if !followed.Follow || followed.MaxBytes != 0 {
		t.Errorf("a followed read asked for %+v", followed)
	}
	if own.MaxBytes != 99 {
		t.Errorf("a read with its own cap asked for %+v", own)
	}
}

// Nothing is asked for a unit that has no log yet, and a unit that
// changed state between the look and the read has none either.
func TestReadGivesNothingWhereThereIsNoLog(t *testing.T) {
	logs := &asked{}
	ctx := context.Background()
	waiting := corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}}
	for what, pod := range map[string]*corev1.Pod{"no pod": nil, "a waiting container": appPod(waiting), "another container": appPod(runningApp)} {
		o := executor.LogOptions{}
		if what == "another container" {
			o.Unit = "build"
		}
		if rc, err := Read(ctx, logs, pod, o); rc != nil || err != nil {
			t.Errorf("%s: Read = %v, %v", what, rc, err)
		}
	}
	// The run before a restart exists only once the container restarted.
	if rc, err := Read(ctx, logs, appPod(runningApp), executor.LogOptions{Previous: true}); rc != nil || err != nil {
		t.Errorf("the previous run of a container that never restarted: %v, %v", rc, err)
	}
	if len(logs.opts) != 0 {
		t.Errorf("the executor was asked %d times", len(logs.opts))
	}

	logs.err = apierrors.NewBadRequest("container is waiting")
	if rc, err := Read(ctx, logs, appPod(runningApp), executor.LogOptions{}); rc != nil || err != nil {
		t.Errorf("a container that changed state: %v, %v", rc, err)
	}
	logs.err = io.ErrUnexpectedEOF
	if _, err := Read(ctx, logs, appPod(runningApp), executor.LogOptions{}); err == nil {
		t.Error("a failed read returned no error")
	}
	if _, err := Read(ctx, nil, appPod(runningApp), executor.LogOptions{}); err == nil {
		t.Error("a read with no executor returned no error")
	}
}
