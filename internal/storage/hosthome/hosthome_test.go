package hosthome_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/storage/hosthome"
)

const owner = "alice-aaaaaaaa"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	must(t, err)
	return fi.Mode()
}

// entries are the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	must(t, err)
	out := []string{}
	for _, e := range list {
		out = append(out, e.Name())
	}
	return out
}

func TestOpenMakesAMissingRootPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "homes")
	root, err := hosthome.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); !m.IsDir() || m.Perm() != 0o700 || root.Dir() != dir {
		t.Fatalf("root %s has mode %v", root.Dir(), m)
	}
	if _, err := hosthome.Open(dir); err != nil {
		t.Fatalf("a root that exists: %v", err)
	}
}

func TestOpenRefusesARootThatIsNotPrivate(t *testing.T) {
	base := t.TempDir()
	at := func(name string) string { return filepath.Join(base, name) }
	must(t, os.Mkdir(at("real"), 0o700))
	must(t, os.Symlink(at("real"), at("link")))
	must(t, os.WriteFile(at("file"), nil, 0o600))
	for name, perm := range map[string]os.FileMode{"group-read": 0o740, "others-read": 0o704, "group-write": 0o720, "others-list": 0o701, "open": 0o777} {
		must(t, os.Mkdir(at(name), 0o700))
		must(t, os.Chmod(at(name), perm))
	}
	for dir, want := range map[string]string{
		at("link"):        "symbolic link",
		at("file"):        "not a folder",
		at("group-read"):  "others",
		at("others-read"): "others",
		at("group-write"): "others",
		at("others-list"): "others",
		at("open"):        "others",
		"homes":           "absolute",
		"":                "absolute",
		at("no/parent"):   "no such file",
	} {
		if _, err := hosthome.Open(dir); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Open(%q) err = %v, want one saying %q", dir, err, want)
		}
	}
	if got := entries(t, at("real")); len(got) != 0 {
		t.Errorf("the folder the link leads to now holds %v", got)
	}
}

// A folder of another user is refused whatever its mode. Only root can
// make one, so the test uses a system folder where there is one.
func TestOpenRefusesARootOfAnotherUser(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns the system's folders")
	}
	for _, dir := range []string{"/root", "/var/root", "/"} {
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		if _, err := hosthome.Open(dir); err == nil || !strings.Contains(err.Error(), "not owned") {
			t.Errorf("Open(%q) err = %v, want one saying it is not owned by this user", dir, err)
		}
	}
}

func TestEnsureMakesAPrivateFolderPerPerson(t *testing.T) {
	root, err := hosthome.Open(filepath.Join(t.TempDir(), "homes"))
	must(t, err)
	dir, err := root.Ensure(owner)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root.Dir(), "home-"+owner); dir != want {
		t.Fatalf("home = %q, want %q", dir, want)
	}
	if m := mode(t, dir); !m.IsDir() || m.Perm() != 0o700 {
		t.Fatalf("home has mode %v", m)
	}
	// What is in a home stays, however often it is ensured.
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o644))
	again, err := root.Ensure(owner)
	if got, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || again != dir || string(got) != "keep" {
		t.Fatalf("second Ensure = %q, %v; notes.txt = %q", again, err, got)
	}
	other, err := root.Ensure("bob-bbbbbbbb")
	if err != nil || other == dir {
		t.Fatalf("another person's home = %q, %v", other, err)
	}
}

// Names are validated where people are made. Even so, no name, however
// hostile, makes or names a folder anywhere but directly in the root.
func TestEnsureCannotBeSteeredOutOfTheRoot(t *testing.T) {
	base := t.TempDir()
	root, err := hosthome.Open(filepath.Join(base, "homes"))
	must(t, err)
	must(t, os.Mkdir(filepath.Join(root.Dir(), "home-x"), 0o700))
	for _, name := range []string{
		"", ".", "..", "../x", "../../x", "x/..", "x/../..", "x/y", "/etc", "/", "x/", "x\x00y", "x\\y",
		"..-x", "x..", "x.y", "Alice", "-x", "x-", "x y", "x\ny", "~", "$HOME", strings.Repeat("x", 64), strings.Repeat("x", 5000),
	} {
		if dir, err := root.Ensure(name); err == nil {
			t.Errorf("Ensure(%q) = %q, want an error", name, dir)
		}
	}
	if got := entries(t, root.Dir()); len(got) != 1 {
		t.Errorf("the root now holds %v", got)
	}
	if got := entries(t, base); len(got) != 1 {
		t.Errorf("beside the root there is now %v", got)
	}
	if got := entries(t, filepath.Join(root.Dir(), "home-x")); len(got) != 0 {
		t.Errorf("another home now holds %v", got)
	}
}

// What already has the home's name must be a private folder of this user:
// a link put there is not followed, and nothing is made behind it.
func TestEnsureRefusesWhatIsNotAPrivateFolder(t *testing.T) {
	base := t.TempDir()
	root, err := hosthome.Open(filepath.Join(base, "homes"))
	must(t, err)
	home := filepath.Join(root.Dir(), "home-"+owner)
	elsewhere := filepath.Join(base, "elsewhere")
	must(t, os.Mkdir(elsewhere, 0o700))
	for name, put := range map[string]func(){
		"a link leading out":  func() { must(t, os.Symlink(elsewhere, home)) },
		"a link staying in":   func() { must(t, os.Mkdir(home+"-real", 0o700)); must(t, os.Symlink("home-"+owner+"-real", home)) },
		"a link to nothing":   func() { must(t, os.Symlink(filepath.Join(base, "nothing"), home)) },
		"a file":              func() { must(t, os.WriteFile(home, nil, 0o600)) },
		"a folder others see": func() { must(t, os.Mkdir(home, 0o700)); must(t, os.Chmod(home, 0o755)) },
	} {
		t.Run(name, func(t *testing.T) {
			put()
			defer func() { must(t, os.RemoveAll(home)); must(t, os.RemoveAll(home+"-real")) }()
			if dir, err := root.Ensure(owner); err == nil {
				t.Errorf("Ensure = %q, want an error", dir)
			}
			if got := entries(t, elsewhere); len(got) != 0 {
				t.Errorf("the folder the link leads to now holds %v", got)
			}
			if _, err := os.Lstat(filepath.Join(base, "nothing")); err == nil {
				t.Error("the folder a dangling link names was made")
			}
		})
	}
}

// The root is looked at again each time: one swapped for a link, or opened
// to others, after Javo started is not used.
func TestEnsureRefusesARootThatChanged(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "homes")
	root, err := hosthome.Open(dir)
	must(t, err)
	must(t, os.Chmod(dir, 0o755))
	if _, err := root.Ensure(owner); err == nil {
		t.Error("a home was made in a root others can read")
	}
	must(t, os.Remove(dir))
	must(t, os.Mkdir(filepath.Join(base, "elsewhere"), 0o700))
	must(t, os.Symlink(filepath.Join(base, "elsewhere"), dir))
	if _, err := root.Ensure(owner); err == nil {
		t.Error("a home was made through a root that is now a link")
	}
	if got := entries(t, filepath.Join(base, "elsewhere")); len(got) != 0 {
		t.Errorf("the folder the link leads to now holds %v", got)
	}
}
