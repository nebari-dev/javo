package preview

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

const repoURL = "https://github.com/example-org/site"

// readWriteTemplate is a template whose apps get ReadWrite API access unless
// the app says otherwise.
func readWriteTemplate() *model.Template {
	return &model.Template{Name: "streamlit", Spec: model.TemplateSpec{
		Kind: "streamlit", Image: "example.com/streamlit:1", Port: 8501, APIToken: true,
		Command: []string{"streamlit"}, Args: []string{"run", "{{.File}}"}, ReadinessPath: "/",
	}}
}

func parentApp() model.App {
	return model.App{
		ID: "parent-1", Namespace: "javo-apps", Name: "site", Owner: "alice", DisplayName: "Site",
		Template: "streamlit", Size: "small", File: "app.py", Command: "", APIAccess: model.APIAccessReadWrite,
		Sharing: model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}, Groups: []string{"team"}},
		Source: &model.Source{
			URL: repoURL, Ref: "main", Path: "web",
			Build:    &model.SourceBuild{Command: "npm ci && npm run build", Output: "dist"},
			Previews: &model.Previews{Enabled: true, Max: 3},
		},
	}
}

func TestChildFollowsTheParentAtTheHeadCommit(t *testing.T) {
	parent := parentApp()
	child := Child(&parent, codehost.PullRequest{Number: 12, HeadSHA: "abc123"})

	if child.Name != "site--pr-12" || child.ParentID != "parent-1" || child.PullNumber != 12 {
		t.Fatalf("identity = %q parent %q pull %d", child.Name, child.ParentID, child.PullNumber)
	}
	if child.DisplayName != "Site PR #12" {
		t.Errorf("display name = %q", child.DisplayName)
	}
	if child.Namespace != parent.Namespace || child.Owner != "alice" || child.Template != "streamlit" || child.Size != "small" || child.File != "app.py" {
		t.Errorf("child = %+v", child)
	}
	if child.Sharing.Access != model.AccessRestricted || !slices.Equal(child.Sharing.Users, []string{"bob"}) || !slices.Equal(child.Sharing.Groups, []string{"team"}) {
		t.Errorf("sharing = %+v", child.Sharing)
	}
	s := child.Source
	if s.URL != repoURL || s.Ref != "abc123" || s.Path != "web" || s.Previews != nil || s.Fork {
		t.Errorf("source = %+v", s)
	}
	if s.Build == nil || s.Build.Command != "npm ci && npm run build" || s.Build.Output != "dist" {
		t.Errorf("build = %+v", s.Build)
	}
	if parent.Source.Ref != "main" || parent.Source.Previews == nil {
		t.Errorf("the parent changed: %+v", parent.Source)
	}
	s.Build.Command = "changed"
	child.Sharing.Users[0] = "carol"
	if parent.Source.Build.Command == "changed" || parent.Sharing.Users[0] == "carol" {
		t.Error("the child shares memory with the parent")
	}
	if err := child.Validate(); err != nil {
		t.Errorf("child does not validate: %v", err)
	}
}

// A preview runs in the parent's environment; the controller installs the
// parent's stored files for it.
func TestChildRunsInTheParentsEnvironment(t *testing.T) {
	parent := parentApp()
	parent.Environment = &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3", Digest: "d"}
	child := Child(&parent, codehost.PullRequest{Number: 12, HeadSHA: "abc123"})
	if child.Environment == nil || *child.Environment != *parent.Environment {
		t.Fatalf("environment = %+v", child.Environment)
	}
}

func TestChildNeverGetsAPIAccess(t *testing.T) {
	parent, tpl := parentApp(), readWriteTemplate()
	if got := model.APIAccessOf(&parent, tpl); got != model.APIAccessReadWrite {
		t.Fatalf("test setup: the parent has %q access", got)
	}
	parent.APIAccess = ""
	child := Child(&parent, codehost.PullRequest{Number: 1, HeadSHA: "a"})
	if got := model.APIAccessOf(&child, tpl); got != model.APIAccessNone {
		t.Fatalf("child API access = %q on a template that defaults to ReadWrite", got)
	}
}

func TestChildOfALongParentNameStillValidates(t *testing.T) {
	parent := parentApp()
	parent.Name = strings.Repeat("a", 60)
	child := Child(&parent, codehost.PullRequest{Number: 2147483647, HeadSHA: "a"})
	if child.Name != naming.PreviewName(parent.Name, 2147483647) || len(child.Name) > 63 {
		t.Fatalf("name = %q (%d)", child.Name, len(child.Name))
	}
	if err := child.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestForkChildIsMarkedAndHasNoToken(t *testing.T) {
	parent := parentApp()
	child := Child(&parent, codehost.PullRequest{Number: 5, HeadSHA: "f00", Fork: true})
	if !child.Source.Fork {
		t.Fatal("Source.Fork is not set")
	}
	pod := builtPod(t, &child)
	if hasSecretVolume(pod) || hasHomeClaim(pod) || hasEnv(pod, "JAVO_TOKEN") {
		t.Fatalf("fork preview pod: secret %v, home %v, JAVO_TOKEN %v", hasSecretVolume(pod), hasHomeClaim(pod), hasEnv(pod, "JAVO_TOKEN"))
	}
}

// A fork's pull request is a stranger's code: its preview opens for the
// owner only, whoever the parent is shared with.
func TestForkChildIsPrivate(t *testing.T) {
	parent := parentApp()
	parent.Sharing = model.Sharing{Access: model.AccessPublic, Users: []string{"bob"}, Groups: []string{"team"}}
	child := Child(&parent, codehost.PullRequest{Number: 5, HeadSHA: "f00", Fork: true})
	if child.Sharing.Access != model.AccessPrivate || len(child.Sharing.Users) != 0 || len(child.Sharing.Groups) != 0 {
		t.Fatalf("fork preview sharing = %+v", child.Sharing)
	}
}

func TestSameRepoChildHasTheTokenButNoHome(t *testing.T) {
	parent := parentApp()
	child := Child(&parent, codehost.PullRequest{Number: 6, HeadSHA: "beef"})
	pod := builtPod(t, &child)
	if !hasSecretVolume(pod) {
		t.Error("a same-repo preview does not mount the parent's token Secret")
	}
	if hasHomeClaim(pod) || hasEnv(pod, "JAVO_TOKEN") {
		t.Errorf("home %v, JAVO_TOKEN %v", hasHomeClaim(pod), hasEnv(pod, "JAVO_TOKEN"))
	}
}

func TestNewHeadCommitChangesThePodFingerprint(t *testing.T) {
	parent := parentApp()
	a := Child(&parent, codehost.PullRequest{Number: 6, HeadSHA: "one"})
	b := Child(&parent, codehost.PullRequest{Number: 6, HeadSHA: "two"})
	if workspace.RunFingerprint(&a) == workspace.RunFingerprint(&b) {
		t.Fatal("a new head commit would not replace the pod")
	}
}

// builtPod is the pod the workspace reconciler builds for child, with the
// parent's token Secret named as it names it.
func builtPod(t *testing.T, child *model.App) *corev1.Pod {
	t.Helper()
	img := workspace.Images{Clone: "example.com/git-sync:1", Runner: podspec.Image{Ref: "example.com/javo:1"}}
	pod, err := workspace.BuildPod(child, readWriteTemplate(), "/u/alice/"+child.Name+"/", "", podstorage.Build(child.Owner, podstorage.Input{UID: workspace.FixedUID, GID: podspec.FixedGID}), sizes.Size{}, img)
	if err != nil {
		t.Fatal(err)
	}
	workspace.WithGitToken(pod, child, workspace.GitTokenSecretName(naming.ObjectName("alice", "site")), "1")
	return pod
}

func hasSecretVolume(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil })
}

func hasHomeClaim(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Spec.Volumes, func(v corev1.Volume) bool { return v.PersistentVolumeClaim != nil })
}

func hasEnv(pod *corev1.Pod, name string) bool {
	for _, c := range append(slices.Clone(pod.Spec.InitContainers), pod.Spec.Containers...) {
		if slices.ContainsFunc(c.Env, func(e corev1.EnvVar) bool { return e.Name == name }) {
			return true
		}
	}
	return false
}
