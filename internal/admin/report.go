// Package admin builds the read-only cluster report admins see and ask the
// assistant about: fixed queries over the apps table, never log contents.
package admin

import (
	"fmt"
	"maps"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// IdleAfter is how long a running app may go without traffic before the
// report lists it as idle.
const IdleAfter = 30 * time.Minute

// ClusterRow is one line of the report. Ref is stable ("failures/NoCapacity",
// "phase/Running", "template/streamlit", "idle/<namespace>/<app>"), so an
// answer can cite it and the server can check the citation.
type ClusterRow struct {
	Ref     string   `json:"ref"`
	Title   string   `json:"title"`
	Value   float64  `json:"value"`
	Unit    string   `json:"unit"`
	Objects []string `json:"objects"`
}

// ClusterReport is the admin's view of the cluster.
type ClusterReport struct {
	Rows []ClusterRow `json:"rows"`
}

// Build reports apps at now. Objects are "<namespace>/<app>", plus the
// owner's name when includeOwners.
func Build(apps []model.App, now time.Time, includeOwners bool) ClusterReport {
	obj := func(a *model.App) string {
		if includeOwners {
			return a.Namespace + "/" + a.Name + " (" + a.Owner + ")"
		}
		return a.Namespace + "/" + a.Name
	}
	failures := map[string][]string{}
	phases := map[string]int{}
	templates := map[string]int{}
	var idle []ClusterRow
	for i := range apps {
		a := &apps[i]
		if !a.DeletedAt.IsZero() {
			continue
		}
		phase := a.Status.Phase
		if a.Paused {
			phase = "Stopped"
		}
		phases[phase]++
		templates[a.Template]++
		if c := failing(a); c != nil {
			failures[c.Reason] = append(failures[c.Reason], obj(a))
		}
		if phase == "Running" && !a.LastActiveAt.IsZero() && now.Sub(a.LastActiveAt) > IdleAfter {
			idle = append(idle, ClusterRow{Ref: "idle/" + a.Namespace + "/" + a.Name, Title: "Running app unused (on this gateway's count)",
				Value: now.Sub(a.LastActiveAt).Minutes(), Unit: "minutes", Objects: []string{obj(a)}})
		}
	}
	var out ClusterReport
	for _, code := range slices.Sorted(maps.Keys(failures)) {
		r, _ := reasons.Lookup(reasons.Code(code))
		out.Rows = append(out.Rows, ClusterRow{Ref: "failures/" + code, Title: fmt.Sprintf("Apps held by: %s", r.Title),
			Value: float64(len(failures[code])), Unit: "apps", Objects: failures[code]})
	}
	for _, p := range slices.Sorted(maps.Keys(phases)) {
		out.Rows = append(out.Rows, ClusterRow{Ref: "phase/" + p, Title: "Apps " + p, Value: float64(phases[p]), Unit: "apps", Objects: []string{}})
	}
	for _, t := range slices.Sorted(maps.Keys(templates)) {
		out.Rows = append(out.Rows, ClusterRow{Ref: "template/" + t, Title: "Apps from template " + t, Value: float64(templates[t]), Unit: "apps", Objects: []string{}})
	}
	out.Rows = append(out.Rows, idle...)
	if out.Rows == nil {
		out.Rows = []ClusterRow{}
	}
	return out
}

// failing is the app's False condition naming a problem, or nil.
func failing(a *model.App) *metav1.Condition {
	if a.Paused {
		return nil
	}
	for i := range a.Status.Conditions {
		c := &a.Status.Conditions[i]
		if c.Status != metav1.ConditionFalse {
			continue
		}
		switch reasons.Code(c.Reason) {
		case reasons.Starting, reasons.Stopped, reasons.StoppedIdle, reasons.IdentityPending, reasons.HomeStarting, reasons.NamespacePending:
			continue
		}
		return c
	}
	return nil
}
