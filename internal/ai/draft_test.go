package ai

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/reasons"
)

var tpls = []DraftTemplate{
	{Name: "streamlit", DisplayName: "Streamlit", Kind: "streamlit", Extensions: []string{".py"}},
	{Name: "jupyterlab", DisplayName: "JupyterLab", Kind: "jupyterlab"},
}

func TestDraftFillsTheFormFromTheDescription(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"displayName":"Sales","template":"streamlit","file":"e2e/hello.py"}`})
	r := service(p).Draft(context.Background(), "alice-a", "A Streamlit app from e2e/hello.py called Sales", tpls)
	if r.Draft != (Draft{DisplayName: "Sales", Template: "streamlit", File: "e2e/hello.py"}) {
		t.Fatalf("draft %+v (%s)", r.Draft, r.Message)
	}
	if !slices.Equal(r.SetBy, []string{"displayName", "template", "file"}) {
		t.Errorf("setBy %v", r.SetBy)
	}
	req := p.Requests[0]
	if !strings.Contains(req.Untrusted, "e2e/hello.py called Sales") || strings.Contains(req.System, "hello.py") {
		t.Errorf("the description must travel as data, not instructions")
	}
	// The model is told it may only pick one of the person's templates.
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(req.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(schema.Properties["template"].Enum, []string{"streamlit", "jupyterlab"}) {
		t.Errorf("template enum %v", schema.Properties["template"].Enum)
	}
}

func TestDraftNamingAnotherTemplateIsRefused(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"displayName":"x","template":"admins-only","file":""}`},
		aitest.Reply{JSON: `{"displayName":"x","template":"admins-only","file":""}`})
	r := service(p).Draft(context.Background(), "alice-a", "anything", tpls)
	if r.Draft != (Draft{}) || r.Message != reasons.Msg(reasons.MsgAIInvalidAnswer) {
		t.Fatalf("draft %+v %q", r.Draft, r.Message)
	}
}

func TestDraftLeavesUnsetFieldsUnmarked(t *testing.T) {
	p := aitest.New(aitest.Reply{JSON: `{"displayName":"","template":"jupyterlab","file":""}`})
	r := service(p).Draft(context.Background(), "alice-a", "a notebook", tpls)
	if !slices.Equal(r.SetBy, []string{"template"}) {
		t.Fatalf("setBy %v", r.SetBy)
	}
}
