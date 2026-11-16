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
	risky := func(g model.FolderGrant) model.FolderGrant { g.Risky = true; return g }
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
		{name: "a name that only starts like the project's", asked: []model.FolderGrant{read("projects/api-docs")}, want: []model.FolderGrant{read("projects/api-docs")}},
		{name: "a hidden name below the top of the home", asked: []model.FolderGrant{read("projects/web/.github"), write("data/.cache")},
			want: []model.FolderGrant{read("projects/web/.github"), write("data/.cache")}},
		{name: "the rest is kept", asked: []model.FolderGrant{read("data"), read(".ssh"), write("notes.md")},
			want: []model.FolderGrant{read("data"), write("notes.md")}, refused: "SSH keys"},
		{name: "a hidden place its owner allowed", asked: []model.FolderGrant{read("data"), risky(read(".ssh")), risky(write(".config/gh/hosts.yml"))},
			want: []model.FolderGrant{read("data"), risky(read(".ssh")), risky(write(".config/gh/hosts.yml"))}},

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
		{name: "the folder around the sign-in to the platform", asked: []model.FolderGrant{risky(read(".config"))}, refused: "tick it there, not the whole folder"},
		{name: "the agents' own state", asked: []model.FolderGrant{risky(write(".claude/projects"))}, refused: "already open"},
		{name: "all of the platform's folder", asked: []model.FolderGrant{risky(read(".javo/envs"))}, refused: "no agent can open ~/.javo"},
		{name: "a listed place says its own reason", asked: []model.FolderGrant{read(".config/gh/hosts.yml")}, refused: "GitHub sign-in"},

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

// What the warning cannot open: the platform's own places (the sign-in to
// it lifts the lock), by themselves, by anything in them and by a folder
// around them, and what every agent has open already, which is not the
// owner's to add. Each says why, to read as much as to change.
func TestNoWarningOpensThePlatformsPlacesOrWhatIsOpenAlready(t *testing.T) {
	t.Parallel()
	app := &model.App{Name: "api", Owner: "alice", File: "projects/api"}
	for p, why := range map[string]string{
		".javo": "no agent can open ~/.javo", ".javo/envs/x": "no agent can open ~/.javo",
		".config/javo": "can lift its own lock", ".config/javo/credentials.json": "can lift its own lock", ".config": "can lift its own lock",
		".claude": "already open", ".claude.json": "already open", ".claude/projects/x": "already open", ".pi": "already open", ".harf": "already open",
		".config/herdr": "already open", ".gitconfig": "already open", ".config/git/config": "already open",
		".cache": "already open", ".cache/pip/wheels": "already open", ".npm": "already open",
	} {
		for _, mode := range []string{model.FolderRead, model.FolderWrite} {
			got, err := LockFolders(app, []model.FolderGrant{{Path: p, Mode: mode, Risky: true}})
			if len(got) != 0 || err == nil || !strings.Contains(err.Error(), why) {
				t.Errorf("%s to %s: kept %v, err %v; want it refused, about %q", p, mode, got, err, why)
			}
		}
	}
}

// The page is handed every place with words of its own, each with what it
// holds, and the folders around a closed one with what to tick instead.
func TestLockPlacesSayWhatTheyHold(t *testing.T) {
	t.Parallel()
	rules := map[string]string{}
	for _, n := range LockPlaces() {
		rules[n.Path] = n.Rule
		if n.What == "" || n.Rule != PlaceOpen && n.Means == "" || (n.Rule == PlaceOpen) != (n.Mode != "") {
			t.Errorf("%+v: want what it holds, what that means unless it is open, and a mode only when it is", n)
		}
	}
	for p, want := range map[string]string{".ssh": PlaceRisky, ".aws": PlaceRisky, ".config/gh": PlaceRisky, ".bashrc": PlaceRisky,
		".claude": PlaceOpen, ".config/herdr": PlaceOpen, ".javo": PlaceClosed, ".config/javo": PlaceClosed, ".config": PlaceAround} {
		if rules[p] != want {
			t.Errorf("%s is %q, want %q", p, rules[p], want)
		}
	}
	if h := HiddenPlace(); h.Rule != PlaceRisky || h.What == "" || h.Means == "" {
		t.Errorf("every other hidden name: %+v, want it risky with what it holds", h)
	}
}

// A person may let their agent into a hidden place at the top of their
// home (their keys, a tool's sign-in) once they were warned: the grant
// says so itself (Risky), and is kept with that. Without it the same path
// is refused, with what the place holds.
func TestAHiddenPlaceIsAddedOnlyWithTheWarningAcknowledged(t *testing.T) {
	t.Parallel()
	app := &model.App{Name: "api", Owner: "alice", File: "projects/api"}
	for _, p := range []string{".ssh", ".ssh/a-private-key", ".aws", ".config/gh", ".bashrc", ".npmrc", ".local/bin", ".config/ghostty"} {
		for _, mode := range []string{model.FolderRead, model.FolderWrite} {
			allowed := model.FolderGrant{Path: p, Mode: mode, Risky: true}
			if got, err := LockFolders(app, []model.FolderGrant{allowed}); err != nil || !reflect.DeepEqual(got, []model.FolderGrant{allowed}) {
				t.Errorf("%s to %s, acknowledged: kept %v, err %v; want it kept as asked", p, mode, got, err)
			}
			if got, err := LockFolders(app, []model.FolderGrant{{Path: p, Mode: mode}}); err == nil || len(got) != 0 || !strings.Contains(err.Error(), "warning") {
				t.Errorf("%s to %s, not acknowledged: kept %v, err %v; want it refused until the warning is accepted", p, mode, got, err)
			}
		}
	}
	// The acknowledgement is about hidden places only: it is not kept on
	// any other grant.
	got, err := LockFolders(app, []model.FolderGrant{{Path: "data", Mode: model.FolderRead, Risky: true}})
	if want := []model.FolderGrant{{Path: "data", Mode: model.FolderRead}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("data: kept %v, err %v; want %v", got, err, want)
	}
}
