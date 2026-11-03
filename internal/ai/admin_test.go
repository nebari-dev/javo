package ai

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/admin"
	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/reasons"
)

var report = admin.ClusterReport{Rows: []admin.ClusterRow{{Ref: "failures/NoCapacity", Title: "Apps held by: No machine has room", Value: 2, Unit: "apps"}}}

func TestAdminAnswerKeepsOnlyRealRows(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"answer":"Two apps wait for room.","refs":["failures/NoCapacity","idle/x/y","question"]}`})
	r := service(p).AskAdmin(context.Background(), "admin-a", "why are apps stuck?", report)
	if r.Answer == "" || len(r.Refs) != 1 || r.Refs[0] != "failures/NoCapacity" || r.Dropped.Refs != 2 {
		t.Fatalf("answer %+v", r)
	}
}

func TestAdminAnswerWithNoRealRowIsRefused(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"answer":"All fine.","refs":["idle/x/y"]}`})
	r := service(p).AskAdmin(context.Background(), "admin-a", "anything?", report)
	if r.Answer != "" || r.Message != reasons.Msg(reasons.MsgAIInvalidAnswer) {
		t.Fatalf("answer %+v", r)
	}
}
