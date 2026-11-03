package helm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/argoproj/notifications-engine/pkg/api"
	"github.com/argoproj/notifications-engine/pkg/services"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/nebari-dev/javo/internal/jobs/notify"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestNotificationsOffByDefault(t *testing.T) {
	t.Parallel()
	out, err := render(t)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if strings.Contains(out, "--notifications-") || strings.Contains(out, "on-run-failed") {
		t.Fatal("notifications rendered while disabled")
	}
}

// The example ConfigMap must work with the engine: its trigger fires on a
// failed newest run, and its webhook body is valid JSON with the job name and
// the catalog title of the run's reason.
func TestExampleNotificationConfigSendsAFailedRun(t *testing.T) {
	t.Parallel()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	defer srv.Close()
	out, err := render(t, "--set", "notifications.enabled=true", "--set", "notifications.webhookURL="+srv.URL)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	var cm *corev1.ConfigMap
	d := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var u unstructured.Unstructured
		if err := d.Decode(&u.Object); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if u.GetKind() == "ConfigMap" && u.GetName() == "javo-notifications" {
			cm = &corev1.ConfigMap{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cm); err != nil {
				t.Fatal(err)
			}
		}
	}
	if cm == nil {
		t.Fatalf("no javo-notifications ConfigMap in:\n%s", out)
	}
	cfg, err := api.ParseConfig(cm, &corev1.Secret{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := api.NewAPI(*cfg, notify.TemplateVars)
	if err != nil {
		t.Fatal(err)
	}
	job := map[string]any{
		"metadata": map[string]any{"name": "nightly", "namespace": "javo-apps"},
		"spec":     map[string]any{"displayName": `Nightly "report"`},
		"status": map[string]any{"recentRuns": []any{
			map[string]any{"id": "r1", "outcome": "TimedOut", "reason": "RunTimedOut", "message": "line1\nline2"},
		}},
	}
	res, err := a.RunTrigger("on-run-failed", job)
	if err != nil || len(res) != 1 || !res[0].Triggered || res[0].OncePer != "r1" {
		t.Fatalf("trigger result %+v, err %v", res, err)
	}
	if err := a.Send(job, res[0].Templates, services.Destination{Service: "javo"}); err != nil {
		t.Fatal(err)
	}
	var body struct{ Job, Run, Outcome, Reason, Message string }
	if err := json.Unmarshal([]byte(got), &body); err != nil {
		t.Fatalf("webhook body is not JSON: %v\n%s", err, got)
	}
	title, _ := reasons.Lookup(reasons.RunTimedOut)
	if body.Job != `Nightly "report"` || body.Run != "r1" || body.Outcome != "TimedOut" || body.Reason != title.Title || body.Message != "line1\nline2" {
		t.Fatalf("body %+v", body)
	}

	// A succeeded newest run does not trigger.
	job["status"] = map[string]any{"recentRuns": []any{map[string]any{"id": "r2", "outcome": "Succeeded"}}}
	if res, _ := a.RunTrigger("on-run-failed", job); len(res) != 1 || res[0].Triggered {
		t.Fatalf("succeeded run triggered: %+v", res)
	}
	// A definition with no runs yet does not trigger either.
	job["status"] = map[string]any{}
	if res, _ := a.RunTrigger("on-run-failed", job); len(res) != 1 || res[0].Triggered {
		t.Fatalf("no runs triggered: %+v", res)
	}
}
