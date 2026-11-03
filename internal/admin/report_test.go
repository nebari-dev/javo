package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func app(owner, name, tpl, phase, failing string, lastActive time.Time) model.App {
	a := model.App{Name: name, Owner: owner, Namespace: "javo-apps", Template: tpl, LastActiveAt: lastActive}
	a.Status.Phase = phase
	if failing != "" {
		a.Status.Conditions = []metav1.Condition{{Type: model.ConditionStarted, Status: metav1.ConditionFalse, Reason: failing,
			Message: "Traceback: password=hunter2"}}
	}
	return a
}

func rows(r ClusterReport) map[string]ClusterRow {
	m := map[string]ClusterRow{}
	for _, row := range r.Rows {
		m[row.Ref] = row
	}
	return m
}

func TestReportGroupsFailuresPhasesAndIdleApps(t *testing.T) {
	apps := []model.App{
		app("alice-a", "a1", "streamlit", "Starting", "NoCapacity", time.Time{}),
		app("alice-a", "a2", "streamlit", "Starting", "NoCapacity", time.Time{}),
		app("bob-b", "b1", "panel", "Failed", "ImagePullFailed", time.Time{}),
		app("bob-b", "b2", "panel", "Running", "", now.Add(-2*time.Hour)),
		app("bob-b", "b3", "panel", "Running", "", now.Add(-time.Minute)),
	}
	r := Build(apps, now, false)
	m := rows(r)
	if row := m["failures/NoCapacity"]; row.Value != 2 || len(row.Objects) != 2 {
		t.Errorf("NoCapacity row %+v", row)
	}
	if row := m["failures/ImagePullFailed"]; row.Value != 1 {
		t.Errorf("ImagePullFailed row %+v", row)
	}
	if row := m["phase/Running"]; row.Value != 2 {
		t.Errorf("Running row %+v", row)
	}
	if row := m["template/panel"]; row.Value != 3 {
		t.Errorf("panel row %+v", row)
	}
	if row, ok := m["idle/javo-apps/b2"]; !ok || row.Value < 119 {
		t.Errorf("idle row %+v (rows %v)", row, m)
	}
	if _, ok := m["idle/javo-apps/b3"]; ok {
		t.Error("an app used a minute ago is listed as idle")
	}
	js, _ := json.Marshal(r)
	if strings.Contains(string(js), "hunter2") || strings.Contains(string(js), "Traceback") {
		t.Fatal("log text in the report")
	}
	if strings.Contains(string(js), "bob-b") {
		t.Fatal("owners named without includeOwners")
	}
	if !strings.Contains(string(rowsJSON(Build(apps, now, true))), "bob-b") {
		t.Fatal("includeOwners did not name owners")
	}
}

func rowsJSON(r ClusterReport) []byte { b, _ := json.Marshal(r); return b }
