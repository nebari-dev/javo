package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/ai/aitest"
)

var mine = []AppSummary{{Name: "sales", DisplayName: "Sales", Phase: "Running", Template: "streamlit"}, {Name: "report", DisplayName: "Report", Phase: "Running", Template: "panel"}}

var acts = []actions.Info{{ID: "stop", Summary: "Stop an app"}, {ID: "start", Summary: "Start an app"}}

func TestCommandKeepsOnlyTheirAppsAndCatalogActions(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"reply":"I will stop both.","actions":[
	 {"action":"stop","target":"sales","size":""},{"action":"stop","target":"report","size":""},
	 {"action":"stop","target":"bobs-app","size":""},{"action":"share","target":"sales","size":""}],"create":false}`})
	r := service(p).Command(context.Background(), "alice-a", "stop all my idle apps", mine, acts)
	if r.Reply != "I will stop both." || len(r.Proposals) != 2 || r.Dropped.Actions != 2 {
		t.Fatalf("result %+v", r)
	}
	for _, pr := range r.Proposals {
		if pr.Action != "stop" || (pr.Target != "sales" && pr.Target != "report") {
			t.Errorf("proposal %+v", pr)
		}
	}
	if !strings.Contains(p.Requests[0].Untrusted, `ref="app/sales"`) || strings.Contains(p.Requests[0].System, "sales") {
		t.Error("app names must travel as data")
	}
}

func TestCommandAsksForANewAppInsteadOfRefusing(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"reply":"Here is a new Streamlit app to review.","actions":[],"create":true}`})
	r := service(p).Command(context.Background(), "alice-a", "create a streamlit dashboard", mine, acts)
	if !r.Create || len(r.Proposals) != 0 {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(string(p.Requests[0].Schema), `"create"`) {
		t.Error("the model was not offered a way to create an app")
	}
}

func TestCommandWithNoAppsMayStillCreate(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"reply":"A new app.","actions":[],"create":true}`})
	r := service(p).Command(context.Background(), "alice-a", "a panel app for my report", nil, acts)
	if !r.Create {
		t.Fatalf("result %+v", r)
	}
}
