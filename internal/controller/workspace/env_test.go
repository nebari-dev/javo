package workspace

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
)

// Plain values go into the app container as they are; a change replaces the
// pod once. Values from secrets still come by reference.
func TestAppGetsItsPlainVariables(t *testing.T) {
	newTemplate(t, "lab-env1")
	putSecret(t, "alice", "ENV1_KEY", testValue)
	app := newApp(t, "ws-env1", "alice", "lab-env1", func(a *model.App) { a.Env = []model.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}} })
	var pod *corev1.Pod
	eventually(t, "a pod with LOG_LEVEL", func() bool {
		pod = podOf(app)
		return pod != nil && secretEnv(pod, "LOG_LEVEL") != nil
	})
	if e := secretEnv(pod, "LOG_LEVEL"); e.Value != "debug" || e.ValueFrom != nil {
		t.Fatalf("env %+v", e)
	}

	next, err := db.Apps().SetEnv(bg, actor, app.ID, getApp(t, app.ID).Version,
		[]model.EnvVar{{Name: "LOG_LEVEL", Value: "info"}}, []model.SecretBinding{{Env: "API_KEY", Secret: "ENV1_KEY"}})
	if err != nil {
		t.Fatal(err)
	}
	var fresh *corev1.Pod
	eventually(t, "a new pod with the change", func() bool {
		fresh = podOf(next)
		return fresh != nil && fresh.UID != pod.UID && secretEnv(fresh, "API_KEY") != nil
	})
	if e := secretEnv(fresh, "LOG_LEVEL"); e == nil || e.Value != "info" {
		t.Fatalf("env %+v", e)
	}
	if e := secretEnv(fresh, "API_KEY"); e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("the secret is not by reference: %+v", e)
	}
}
