package apitest

import (
	"context"
	"io"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// RunLogs answers pod log reads with prev or cur by the Previous option and
// records every read.
type RunLogs struct {
	Mu        sync.Mutex
	Prev, Cur string
	Calls     []struct {
		Pod  string
		Opts corev1.PodLogOptions
	}
}

func (l *RunLogs) Stream(_ context.Context, _, pod string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	l.Calls = append(l.Calls, struct {
		Pod  string
		Opts corev1.PodLogOptions
	}{pod, *opts})
	body := l.Cur
	if opts.Previous {
		body = l.Prev
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// CrashedApp is alice's app "sales" as the controller records a crash.
func CrashedApp() *model.App {
	app := gatewaytest.Workspace("sales", "alice-aaaaaaaa", "Sales")
	app.Status.Phase = "Failed"
	app.Status.Conditions = []metav1.Condition{
		{Type: model.ConditionScheduled, Status: metav1.ConditionTrue, Reason: "Ready"},
		{Type: model.ConditionStarted, Status: metav1.ConditionFalse, Reason: string(reasons.AppCrashed), Message: "container app terminated: Error, exit code 1"},
	}
	return app
}

type DiagnosisJSON struct {
	Code, Title, What, Todo, Rule string
	Known                         bool
	Evidence, Bundle              []struct{ Kind, Ref, Excerpt string }
	Proposals                     []struct {
		Action, Target, Summary string
		Params                  map[string]string
	}
}
