package workspace

import (
	"context"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// ownPort is an executor that, like the one that runs apps as processes,
// serves every app on a port of its own choosing and may refuse a plan.
type ownPort struct {
	executor.Executor
	refuse error

	mu      sync.Mutex
	started []workload.Spec
}

func (x *ownPort) Addr(context.Context, workload.Ref, int32) (string, error) {
	return "127.0.0.1:49231", nil
}

func (x *ownPort) Start(ctx context.Context, s workload.Spec) error {
	x.mu.Lock()
	x.started = append(x.started, s)
	x.mu.Unlock()
	if x.refuse != nil {
		return x.refuse
	}
	return x.Executor.Start(ctx, s)
}

// The app is told to serve on the port its executor reaches it at.
func TestAnAppServesOnThePortOfItsExecutor(t *testing.T) {
	t.Parallel()
	x := &ownPort{}
	fx := newEnv(t, func(r *Reconciler) { x.Executor, r.Exec = r.Exec, x })
	fx.newTemplate(t, "lab-port", func(s *model.TemplateSpec) { s.Args = []string{"--port={{.Port}}"} })
	fx.newApp(t, "ws-port", "jon", "lab-port")
	eventually(t, "a start", func() bool {
		x.mu.Lock()
		defer x.mu.Unlock()
		return len(x.started) > 0
	})
	s := x.started[0]
	if s.Port != 49231 || len(s.Main.Args) != 1 || s.Main.Args[0] != "--port=49231" {
		t.Fatalf("port %d, args %q: want the executor's port, 49231", s.Port, s.Main.Args)
	}
}

// A plan the executor cannot run without containers is the template's to
// fix: the app fails as TemplateInvalid with the executor's words, and is
// not tried again as an error.
func TestAnAppTheExecutorCannotRunFailsAsTemplateInvalid(t *testing.T) {
	t.Parallel()
	x := &ownPort{refuse: &executor.UnsupportedError{Unit: "app", Why: "it needs the image example.com/lab:1, and nothing here runs images"}}
	fx := newEnv(t, func(r *Reconciler) { x.Executor, r.Exec = r.Exec, x })
	fx.newTemplate(t, "lab-image")
	app := fx.newApp(t, "ws-image", "jon", "lab-image")
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateInvalid" || !strings.Contains(c.Message, "nothing here runs images") {
		t.Fatalf("scheduled condition = %+v", c)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("a refused app has a pod")
	}
}

// An app of a reconciler that plans for a host is planned with that
// host's folders; here its home is a claim, not a folder, so it is
// refused as the template's problem rather than started in a container.
func TestAReconcilerWithAHostPlansForIt(t *testing.T) {
	t.Parallel()
	fx := newEnv(t, func(r *Reconciler) {
		r.Host = func(workload.Ref) workload.Host { return testHost }
	})
	fx.newTemplate(t, "lab-host")
	app := fx.newApp(t, "ws-host", "jon", "lab-host")
	eventually(t, "phase Failed", fx.phaseIs(app.ID, "Failed"))
	c := condition(fx.getApp(t, app.ID), model.ConditionScheduled)
	if c == nil || c.Reason != "TemplateInvalid" || !strings.Contains(c.Message, "home is not a folder") {
		t.Fatalf("scheduled condition = %+v", c)
	}
}
