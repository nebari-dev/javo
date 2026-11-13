// Package podlogs reads the logs of app and job pods, capped in size and
// split into timestamped lines.
package podlogs

import (
	"bufio"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

// Source reads a pod container's log; Clientset implements it. Job logs are
// read through it (Open), app logs through the executor (Read).
type Source interface {
	Stream(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error)
}

// Clientset reads pod logs through client-go (pods/log get).
type Clientset struct{ Clientset kubernetes.Interface }

// Stream opens the log of one container of pod.
func (c Clientset) Stream(ctx context.Context, namespace, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	return c.Clientset.CoreV1().Pods(namespace).GetLogs(pod, opts).Stream(ctx)
}

// LogLine is one line the app wrote, as untrusted plain text.
type LogLine struct {
	// Time is when the line was written; absent when the cluster gave none.
	Time *time.Time `json:"time,omitempty"`
	Text string     `json:"text"`
}

// maxLogBytes caps one log read so a noisy app cannot fill the response.
const maxLogBytes = 512 << 10

// ContainerStatus returns the status of the pod's container or init
// container called name, or nil before it has one.
func ContainerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	for _, l := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
		if i := slices.IndexFunc(l, func(c corev1.ContainerStatus) bool { return c.Name == name }); i >= 0 {
			return &l[i]
		}
	}
	return nil
}

// hasLog reports whether the asked-for run of the container has a log to
// read. A container waiting to restart (CrashLoopBackOff) still has the log
// of the run that just ended, which the kubelet serves as the current one.
func hasLog(cs *corev1.ContainerStatus, previous bool) bool {
	switch {
	case cs == nil:
		return false
	case previous:
		return cs.LastTerminationState.Terminated != nil
	}
	return cs.State.Running != nil || cs.State.Terminated != nil || cs.LastTerminationState.Terminated != nil
}

// Open opens the log of opts.Container (the app container when empty)
// of pod, or returns nil when there is none to read. Unless following or set
// by the caller, it sets the size cap.
func Open(ctx context.Context, logs Source, pod *corev1.Pod, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	if opts.Container == "" {
		opts.Container = podspec.ContainerName
	}
	if pod == nil || !hasLog(ContainerStatus(pod, opts.Container), opts.Previous) {
		return nil, nil
	}
	if logs == nil {
		return nil, problem.Internal("read pod log", errors.New("no pod log reader configured"))
	}
	if !opts.Follow && opts.LimitBytes == nil {
		opts.LimitBytes = new(int64(maxLogBytes))
	}
	return opened(logs.Stream(ctx, pod.Namespace, pod.Name, opts))
}

// Read opens the log of the unit o.Unit (the app's own when empty) of the
// app's pod through exec, or returns nil when there is none to read. Unless
// following or set by the caller, it sets the size cap.
func Read(ctx context.Context, exec executor.Executor, pod *corev1.Pod, o executor.LogOptions) (io.ReadCloser, error) {
	if o.Unit == "" {
		o.Unit = podspec.ContainerName
	}
	if pod == nil || !hasLog(ContainerStatus(pod, o.Unit), o.Previous) {
		return nil, nil
	}
	if exec == nil {
		return nil, problem.Internal("read pod log", errors.New("no pod log reader configured"))
	}
	if !o.Follow && o.MaxBytes == 0 {
		o.MaxBytes = maxLogBytes
	}
	ref := workload.Ref{Namespace: pod.Namespace, Name: pod.Name, ID: pod.Labels[model.LabelApp], Owner: pod.Labels[model.LabelOwner]}
	return opened(exec.Logs(ctx, ref, o))
}

// opened is a log that was asked for, or nil when there turned out to be
// none.
func opened(rc io.ReadCloser, err error) (io.ReadCloser, error) {
	switch {
	case apierrors.IsBadRequest(err) || apierrors.IsNotFound(err):
		// The container changed state between the read and the request.
		return nil, nil
	case err != nil:
		return nil, problem.Internal("read pod log", err)
	}
	return rc, nil
}

// Scanner splits a log into lines; an over-long line ends the read.
func Scanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLogBytes)
	return sc
}

// ParseLine splits the RFC 3339 time the kubelet puts before each line
// when Timestamps is set.
func ParseLine(raw string) LogLine {
	if ts, text, ok := strings.Cut(raw, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			t = t.UTC()
			return LogLine{Time: &t, Text: text}
		}
	}
	return LogLine{Text: raw}
}
