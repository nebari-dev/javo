package workspace

import (
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/sizes"
)

// The Terminal template is a shell behind ttyd's terminal page, in the
// folder the user chose or else the home folder. It runs no multiplexer, so
// the page keeps its own scrollback, and it is never shared: whoever opens
// it gets a shell with the owner's files.
func TestTerminalTemplateRunsAShellInTheChosenFolder(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "terminal")
	if tpl.Spec.MaxSharing != model.AccessPrivate || tpl.Spec.FromRepo != model.FromRepoNone || len(tpl.Spec.FileExtensions) != 0 {
		t.Fatalf("maxSharing = %q, fromRepo = %q, fileExtensions = %v; want a private folder app without repositories",
			tpl.Spec.MaxSharing, tpl.Spec.FromRepo, tpl.Spec.FileExtensions)
	}
	for _, c := range []struct{ label, file, want string }{
		{"no folder", "", "/home/user"},
		{"folder", "projects/api", "/home/user/projects/api"},
	} {
		t.Run(c.label, func(t *testing.T) {
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "shell", Owner: "alice", Template: tpl.Name, File: c.file}
			pod, err := BuildPod(ws, tpl, "/u/a/shell/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			argv := slices.Concat(pod.Spec.Containers[0].Command, pod.Spec.Containers[0].Args)
			i := slices.Index(argv, "ttyd")
			if argv[0] != "/opt/javo/pod-user.sh" || i < 0 {
				t.Fatalf("argv = %q, want pod-user.sh running ttyd", argv)
			}
			for _, want := range []string{"--writable", "--check-origin", "--index=/opt/javo/agents/terminal.html", "--cwd=" + c.want} {
				if !slices.Contains(argv[i:], want) {
					t.Fatalf("argv = %q, want %s", argv, want)
				}
			}
			if argv[len(argv)-1] != "bash" || slices.Contains(argv, "tmux") {
				t.Fatalf("argv = %q, want ttyd to run bash itself", argv)
			}
		})
	}
}
