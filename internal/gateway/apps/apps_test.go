package apps

import (
	"context"
	"errors"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store/storetest"
	corev1 "k8s.io/api/core/v1"
	"net/http"
	"slices"
	"testing"
)

// Each step's log is the log of the containers doing that step's work, in
// the order they run.
func TestStepContainers(t *testing.T) {
	t.Parallel()
	env := gatewaytest.PodWith("env", gatewaytest.EnvInits...)
	site := gatewaytest.PodWith("site", "clone", "source-check", "build", "site-copy", "site-config", "javo-files", "pod-user")
	plain := gatewaytest.PodWith("plain", "clone", "source-check", "javo-files", "pod-user")
	for _, tc := range []struct {
		pod  *corev1.Pod
		step string
		want []string
	}{
		{env, model.ConditionScheduled, nil},
		{env, model.ConditionHomeReady, []string{"javo-files", "pod-user", "sample-dir", "sample-copy"}},
		{env, model.ConditionImageReady, nil},
		{env, model.ConditionEnvReady, []string{"clone", "source-check", "env-tools", "env-install"}},
		{env, model.ConditionStarted, []string{podspec.ContainerName}},
		{env, model.ConditionReady, nil},
		{site, model.ConditionBuilt, []string{"clone", "source-check", "build", "site-copy", "site-config"}},
		{site, model.ConditionStarted, []string{podspec.ContainerName}},
		// Without a build or an environment, the clone is part of the start.
		{plain, model.ConditionStarted, []string{"clone", "source-check", podspec.ContainerName}},
		{plain, model.ConditionEnvReady, nil},
	} {
		if got := stepContainers(tc.pod, tc.step); !slices.Equal(got, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.pod.Name, tc.step, got, tc.want)
		}
	}
}

// The events of a container belong to the step its log does.
func TestContainerEventsFollowTheStepMapping(t *testing.T) {
	t.Parallel()
	env := gatewaytest.PodWith("env", gatewaytest.EnvInits...)
	for c, want := range map[string]string{
		"clone": model.ConditionEnvReady, "env-install": model.ConditionEnvReady, "env-tools": model.ConditionEnvReady,
		"sample-copy": model.ConditionHomeReady, "javo-files": model.ConditionHomeReady,
	} {
		e := corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", FieldPath: "spec.initContainers{" + c + "}"}, Reason: "Started"}
		if got := eventStep(&e, env); got != want {
			t.Errorf("%s: step %q, want %q", c, got, want)
		}
	}
}

func TestEventStepMapping(t *testing.T) {
	t.Parallel()
	app := "spec.containers{app}"
	for _, tc := range []struct{ reason, path, msg, step string }{
		{"Scheduled", "", "Successfully assigned", model.ConditionScheduled},
		{"FailedScheduling", "", "0/3 nodes are available", model.ConditionScheduled},
		{"SuccessfulAttachVolume", "", "AttachVolume.Attach succeeded", model.ConditionHomeReady},
		{"FailedAttachVolume", "", "Multi-Attach error", model.ConditionHomeReady},
		{"FailedMount", "", "MountVolume.SetUp failed", model.ConditionHomeReady},
		{"Pulling", app, "Pulling image \"x\"", model.ConditionImageReady},
		{"Pulled", app, "Successfully pulled image \"x\"", model.ConditionImageReady},
		{"Failed", app, "Failed to pull image \"x\": not found", model.ConditionImageReady},
		{"Failed", app, "Error: ErrImagePull", model.ConditionImageReady},
		{"BackOff", app, "Back-off pulling image \"x\"", model.ConditionImageReady},
		{"Created", app, "Created container app", model.ConditionStarted},
		{"Started", app, "Started container app", model.ConditionStarted},
		{"Killing", app, "Stopping container app", model.ConditionStarted},
		{"BackOff", app, "Back-off restarting failed container app", model.ConditionStarted},
		{"Failed", app, "Error: failed to create containerd task", model.ConditionStarted},
		{"Unhealthy", app, "Readiness probe failed", model.ConditionReady},
		{"SandboxChanged", "", "Pod sandbox changed", ""},
		// The sample-copy init containers fill the home folder.
		{"Pulled", "spec.initContainers{sample-copy}", "Container image already present", model.ConditionHomeReady},
		{"Started", "spec.initContainers{sample-copy}", "Container started", model.ConditionHomeReady},
	} {
		e := corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "Pod", FieldPath: tc.path}, Reason: tc.reason, Message: tc.msg}
		if got := eventStep(&e, nil); got != tc.step {
			t.Errorf("%s %s %q: step %q, want %q", tc.reason, tc.path, tc.msg, got, tc.step)
		}
	}
	claim := corev1.Event{InvolvedObject: corev1.ObjectReference{Kind: "PersistentVolumeClaim"}, Reason: "WaitForFirstConsumer"}
	if got := eventStep(&claim, nil); got != model.ConditionHomeReady {
		t.Errorf("claim event: step %q", got)
	}
}

// A change built on an app another request has changed since is refused with
// the 409 "app changed", and changes nothing.
func TestConcurrentChangeIsAConflict(t *testing.T) {
	t.Parallel()
	const alice = "alice-1a2b3c4d"
	ctx := context.Background()
	db := storetest.New(t)
	gatewaytest.Seed(t, db, []any{gatewaytest.Workspace("report", alice, "Report")})
	ws := &Workspaces{db: db}
	get := func() *model.App {
		t.Helper()
		app, err := db.Apps().GetByOwnerName(ctx, alice, "report")
		if err != nil {
			t.Fatal(err)
		}
		return &app
	}
	stale := get()
	if _, err := db.Apps().UpdateSpec(ctx, gatewaytest.Actor, stale.ID, stale.Version, func(a *model.App) error {
		a.Size = "medium"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := ws.updateApp(ctx, stale, func(a *model.App) { a.Sharing = model.Sharing{Access: model.AccessSignedIn} })
	var got *problem.Details
	if !errors.As(err, &got) || got.Status != http.StatusConflict || got.Title != reasons.Msg(reasons.MsgAppChanged) {
		t.Fatalf("err = %#v, want the 409 app changed", err)
	}
	if now := get(); now.Sharing.Access != model.AccessPrivate || now.Size != "medium" {
		t.Fatalf("stored %+v", now)
	}
}
