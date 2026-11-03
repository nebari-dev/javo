package apitest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/gateway/apps"
)

// ScriptLogs answers pod log requests from per-container scripts: a fixed
// body, or a pipe the test writes to (a followed log that stays open).
type ScriptLogs struct {
	Mu     sync.Mutex
	Bodies map[string]string
	Pipes  map[string]*io.PipeReader
}

func NewScriptLogs() *ScriptLogs {
	return &ScriptLogs{Bodies: map[string]string{}, Pipes: map[string]*io.PipeReader{}}
}

// LogKey names one log: pod/container, with "/previous" for the run before.
func LogKey(pod, container string, previous bool) string {
	k := pod + "/" + container
	if previous {
		k += "/previous"
	}
	return k
}

// Follow makes the next read of key a pipe; the returned writer feeds it.
func (l *ScriptLogs) Follow(key string) *io.PipeWriter {
	r, w := io.Pipe()
	l.Mu.Lock()
	defer l.Mu.Unlock()
	l.Pipes[key] = r
	return w
}

func (l *ScriptLogs) Stream(ctx context.Context, _, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	key := LogKey(pod, opts.Container, opts.Previous)
	l.Mu.Lock()
	r, piped := l.Pipes[key]
	delete(l.Pipes, key)
	body := l.Bodies[key]
	l.Mu.Unlock()
	if piped {
		go func() {
			<-ctx.Done()
			r.CloseWithError(ctx.Err())
		}()
		return r, nil
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func Terminated(name string, code int32, reason, msg string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, Reason: reason, Message: msg}}}
}

func RunningCS(name string, restarts int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, RestartCount: restarts, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

// StepStream opens the step's log stream and returns its records as they come.
func StepStream(t *testing.T, f *Fixture, app, step string) (<-chan apps.StepLogRecord, context.CancelFunc) {
	t.Helper()
	srv := httptest.NewServer(f.Srv)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/workspaces/"+app+"/steps/"+step+"/log", nil)
	req.AddCookie(f.Session("alice-aaaaaaaa"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("status %d, type %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	out := make(chan apps.StepLogRecord, 64)
	go func() {
		defer close(out)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			var r apps.StepLogRecord
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				t.Errorf("record %q: %v", sc.Text(), err)
				return
			}
			out <- r
		}
	}()
	t.Cleanup(func() {
		cancel()
		srv.Close()
	})
	return out, cancel
}

// Next returns the next record, failing after a while.
func Next(t *testing.T, recs <-chan apps.StepLogRecord) apps.StepLogRecord {
	t.Helper()
	select {
	case r, ok := <-recs:
		if !ok {
			t.Fatal("stream ended")
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no record")
	}
	return apps.StepLogRecord{}
}

// Says renders a record for comparison: kind container [text] [exit code].
func Says(r apps.StepLogRecord) string {
	s := r.Kind + " " + r.Container
	if r.Text != "" {
		s += " " + r.Text
	}
	if r.ExitCode != nil {
		s += fmt.Sprintf(" exit=%d", *r.ExitCode)
	}
	if r.Reason != "" {
		s += " (" + r.Reason + ")"
	}
	if r.Restarts > 0 {
		s += fmt.Sprintf(" restarts=%d", r.Restarts)
	}
	return s
}

func Expect(t *testing.T, recs <-chan apps.StepLogRecord, want ...string) {
	t.Helper()
	for _, w := range want {
		if got := Says(Next(t, recs)); got != w {
			t.Fatalf("record %q, want %q", got, w)
		}
	}
}

func ExpectEnd(t *testing.T, recs <-chan apps.StepLogRecord) {
	t.Helper()
	select {
	case r, ok := <-recs:
		if ok {
			t.Fatalf("record %q after the end", Says(r))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream still open")
	}
}

func SetStatuses(t *testing.T, f *Fixture, pod *corev1.Pod, inits ...corev1.ContainerStatus) {
	t.Helper()
	pod.Status.InitContainerStatuses = inits
	if err := f.Client.Status().Update(context.Background(), pod); err != nil {
		if err := f.Client.Update(context.Background(), pod); err != nil {
			t.Fatal(err)
		}
	}
}

func FastPoll(t *testing.T) {
	old := apps.StepPollEvery
	apps.StepPollEvery = 10 * time.Millisecond
	t.Cleanup(func() { apps.StepPollEvery = old })
}
