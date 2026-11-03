package apitest

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

const (
	TestRepoToken = "ghp_testtoken123"
	PrivateRepo   = "https://github.com/example-org/private-site"
)

// RepoTemplates are the three ways a template treats a repository.
func RepoTemplates() []any {
	optional := gatewaytest.Template("streamlit", "streamlit")
	optional.Spec.FromRepo = model.FromRepoOptional
	required := gatewaytest.Template("backend-node", "custom")
	required.Spec.FromRepo = model.FromRepoRequired
	required.Spec.UserCommand = model.UserCommandRequired
	static := gatewaytest.Template("static-site", "static")
	static.Spec.FromRepo = model.FromRepoOptional
	static.Spec.FileExtensions = []string{".html"}
	static.Spec.Build = &model.TemplateBuild{Image: "example.org/node:1", Command: "npm ci && npm run build", IfFile: "package.json", Output: "dist", ServeDir: "/site"}
	return []any{optional, required, static, gatewaytest.Template("lab", "jupyterlab")}
}

type SourceJSON struct {
	URL, Ref, Path, Commit, WebURL string
	HasToken, PreviewsAvailable    bool
	TokenSecret                    string
	WebhookConfigured              bool
}

type RepoWorkspaceJSON struct {
	WorkspaceJSON
	Source    *SourceJSON
	PreviewOf *struct {
		Name       string
		PullNumber int32
	}
}

// RepoApp is alice's app that runs from a repository.
func RepoApp(short string) *model.App {
	app := gatewaytest.Workspace(short, AliceID, short)
	app.File = "app.py"
	app.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}
	return app
}

// PreviewChild stores a preview of parent for pull request number.
func (f *Fixture) PreviewChild(parent *model.App, number int32) model.App {
	f.T.Helper()
	p := f.GetWorkspace(parent.Owner, parent.Name)
	child, err := f.Db.Previews().Put(context.Background(), &model.App{
		Namespace: p.Namespace, Name: naming.PreviewName(p.Name, number), Owner: p.Owner, Template: p.Template,
		File: p.File, ParentID: p.ID, PullNumber: number, Sharing: p.Sharing, DisplayName: "PR " + p.Name,
		Source: &model.Source{URL: p.Source.URL, Ref: "abc123"},
	})
	if err != nil {
		f.T.Fatalf("put preview: %v", err)
	}
	return child
}

func CaptureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const CreatePrivate = `{"displayName":"Private","template":"streamlit","file":"app.py",
 "source":{"url":"` + PrivateRepo + `","ref":"main","path":"web","tokenSecret":"GH_TOKEN"}}`

type PreviewsJSON struct {
	Enabled                   bool
	Max, MaxAllowed           int32
	Forks                     bool
	ForksWarning              string
	SyncedAt                  string
	Reason, Title, Text, Todo string
	Items                     []struct {
		Number       int32
		Title, App   string
		Reason, Text string
	}
}
