package workspace

import (
	"cmp"
	"reflect"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
)

// What a person may add to what their locked agent can open: a folder or
// file of their home, by a path that is what it reads as, that is not open
// already and that neither is nor holds one of the places no agent is let
// into.
func TestLockFolders(t *testing.T) {
	t.Parallel()
	app := &model.App{Name: "api", Owner: "alice", File: "projects/api"}
	read := func(p string) model.FolderGrant { return model.FolderGrant{Path: p, Mode: model.FolderRead} }
	write := func(p string) model.FolderGrant { return model.FolderGrant{Path: p, Mode: model.FolderWrite} }
	long := strings.Repeat("a", maxLockFolderPath+1)
	many := func(n int) (out []model.FolderGrant) {
		for i := range n {
			out = append(out, read("data/"+string(rune('a'+i))))
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		app   *model.App
		asked []model.FolderGrant
		want  []model.FolderGrant
		// refused is part of why the first grant not kept was refused.
		refused string
	}{
		{name: "none"},
		{name: "a folder to read and a file to change", asked: []model.FolderGrant{read("data/sales-2026"), write("notes.md")},
			want: []model.FolderGrant{read("data/sales-2026"), write("notes.md")}},
		{name: "cleaned", asked: []model.FolderGrant{read("./data//sales/"), read("/home/user/projects/shared-lib")},
			want: []model.FolderGrant{read("data/sales"), read("projects/shared-lib")}},
		{name: "the folder the project is in", asked: []model.FolderGrant{read("projects")}, want: []model.FolderGrant{read("projects")}},
		{name: "a folder inside another one granted", asked: []model.FolderGrant{read("data"), write("data/out")},
			want: []model.FolderGrant{read("data"), write("data/out")}},
		{name: "a name that only starts like a refused one", asked: []model.FolderGrant{read(".sshfs"), read("projects/api-docs"), read(".config/ghostty")},
			want: []model.FolderGrant{read(".sshfs"), read("projects/api-docs"), read(".config/ghostty")}},
		{name: "the rest is kept", asked: []model.FolderGrant{read("data"), read(".ssh"), write("notes.md")},
			want: []model.FolderGrant{read("data"), write("notes.md")}, refused: "SSH keys"},

		{name: "no path", asked: []model.FolderGrant{read("")}, refused: "inside your home"},
		{name: "the home", asked: []model.FolderGrant{read(".")}, refused: "inside your home"},
		{name: "the home by its path", asked: []model.FolderGrant{read("/home/user/")}, refused: "inside your home"},
		{name: "a folder the home is in", asked: []model.FolderGrant{read("/home")}, refused: "inside your home"},
		{name: "the whole disk", asked: []model.FolderGrant{read("/")}, refused: "inside your home"},
		{name: "outside the home", asked: []model.FolderGrant{read("/etc/hosts")}, refused: "inside your home"},
		{name: "another home", asked: []model.FolderGrant{read("/home/username/data")}, refused: "inside your home"},
		{name: "up and out", asked: []model.FolderGrant{read("../bob/data")}, refused: `".."`},
		{name: "up and back in", asked: []model.FolderGrant{read("data/../.ssh")}, refused: `".."`},
		{name: "up from the home's path", asked: []model.FolderGrant{read("/home/user/../user/.ssh")}, refused: `".."`},
		{name: "a line break", asked: []model.FolderGrant{read("data\n--rw")}, refused: "control characters"},
		{name: "too long", asked: []model.FolderGrant{read(long)}, refused: "too long"},
		{name: "no mode", asked: []model.FolderGrant{{Path: "data"}}, refused: "read or write"},
		{name: "another mode", asked: []model.FolderGrant{{Path: "data", Mode: "execute"}}, refused: "read or write"},

		{name: "the project", asked: []model.FolderGrant{write("projects/api")}, refused: "already open"},
		{name: "inside the project", asked: []model.FolderGrant{read("projects/api/src/main.go")}, refused: "already open"},
		{name: "the project of an app with no folder", app: &model.App{Name: "todo", Owner: "alice"},
			asked: []model.FolderGrant{read("projects/todo/src")}, refused: "already open"},
		{name: "twice", asked: []model.FolderGrant{read("data"), write("data/")}, want: []model.FolderGrant{read("data")}, refused: "already on the list"},

		{name: "the SSH keys", asked: []model.FolderGrant{read(".ssh")}, refused: "SSH keys"},
		{name: "one of the SSH keys", asked: []model.FolderGrant{read(".ssh/a-private-key")}, refused: "SSH keys"},
		{name: "a folder that holds a refused one", asked: []model.FolderGrant{read(".config")}, refused: "~/.config/"},
		{name: "the agents' own state", asked: []model.FolderGrant{write(".claude/projects")}, refused: "already open"},
		{name: "all of the platform's folder", asked: []model.FolderGrant{read(".javo/envs")}, refused: "~/.javo"},

		{name: "as many as allowed", asked: many(MaxLockFolders), want: many(MaxLockFolders)},
		{name: "one too many", asked: many(MaxLockFolders + 1), want: many(MaxLockFolders), refused: "at most 16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := LockFolders(cmp.Or(tc.app, app), tc.asked)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
			switch {
			case tc.refused == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)):
				t.Errorf("err = %v, want one about %q", err, tc.refused)
			}
		})
	}
}

// Every place no agent is let into is refused, by itself, by anything in it
// and by the folder it is in, to read as much as to change: the person's
// keys and sign-ins, the files their shell runs at every start, the
// platform's folder and what the agents keep, which is open already.
func TestNoAgentIsLetIntoTheNeverFolders(t *testing.T) {
	t.Parallel()
	app := &model.App{Name: "api", Owner: "alice", File: "projects/api"}
	want := []string{".ssh", ".git-credentials", ".bashrc", ".bash_profile", ".profile", ".zshrc", ".zshenv", ".config/gh", ".aws", ".kube",
		".gnupg", ".netrc", ".docker", ".javo", ".config/javo", ".claude", ".claude.json", ".pi", ".harf", ".config/herdr"}
	listed := map[string]bool{}
	for _, n := range NeverFolders() {
		listed[n.Path] = true
		if n.Why == "" {
			t.Errorf("%s: no reason to show", n.Path)
		}
	}
	for _, p := range want {
		if !listed[p] {
			t.Errorf("%s is not on the list", p)
		}
		asked := []string{p, p + "/sub/file"}
		if dir := strings.LastIndex(p, "/"); dir > 0 {
			asked = append(asked, p[:dir])
		}
		for _, a := range asked {
			for _, mode := range []string{model.FolderRead, model.FolderWrite} {
				if got, err := LockFolders(app, []model.FolderGrant{{Path: a, Mode: mode}}); err == nil || len(got) != 0 {
					t.Errorf("%s to %s: kept %v, err %v; want it refused", a, mode, got, err)
				}
			}
		}
	}
}
