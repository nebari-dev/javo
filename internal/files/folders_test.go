package files_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
)

// homeRows answers for the homes table: a home by namespace and owner,
// and which were asked for.
type homeRows struct {
	homes map[string]model.Home
	asked []string
}

func (h *homeRows) Home(_ context.Context, namespace, owner string) (model.Home, error) {
	h.asked = append(h.asked, namespace+"/"+owner)
	return h.homes[namespace+"/"+owner], nil
}

// folderHome is a home row as the host's home reconciler leaves it: ready,
// with the folder where a claim's name would be.
func folderHome(dir string) model.Home {
	return model.Home{Status: model.HomeStatus{
		ClaimName: dir, Prepared: true,
		Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue}},
	}}
}

func TestFoldersGiveTheFolderOfAReadyHome(t *testing.T) {
	ctx := context.Background()
	home, _ := newDiskHome(t, map[string][]byte{"notes/a.py": []byte("abc")})
	root := filepath.Dir(home)
	rows := &homeRows{homes: map[string]model.Home{ns + "/" + owner: folderHome(home)}}
	f := files.Folders{Homes: rows, Namespace: ns, Root: root}
	if got, err := f.Ensure(ctx, owner); err != nil || got != home {
		t.Fatalf("Ensure = %q, %v; want %q", got, err, home)
	}
	if got, err := f.EnsureGroup(ctx, owner, ""); err != nil || got != home {
		t.Fatalf("EnsureGroup without a folder = %q, %v; want %q", got, err, home)
	}
	// There is nothing to keep or to free.
	f.Touch(owner)
	if err := f.Release(ctx, owner); err != nil {
		t.Fatal(err)
	}

	// The Lister and the gateway's handlers need nothing else.
	l := files.NewLister(f, files.Direct{})
	got, err := l.List(ctx, owner, "notes")
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Name != "a.py" {
		t.Fatalf("List = %+v, %v", got, err)
	}
	if _, err := l.Save(ctx, owner, "notes/b.py", []byte("print(1)\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ListGroup(ctx, owner, "ml", ""); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("ListGroup err = %v, want ErrNotFound: a host has no group folders", err)
	}
}

// A person without a home is given one by asking (as an app's start does),
// and waits until the controller has made its folder.
func TestFoldersWaitForAHomeThatIsNotReady(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "home-"+owner)
	notReady := folderHome(dir)
	notReady.Status.Conditions[0].Status = metav1.ConditionFalse
	unprepared := folderHome(dir)
	unprepared.Status.Prepared = false
	for name, home := range map[string]model.Home{"a new row": {}, "not ready": notReady, "not prepared": unprepared} {
		rows := &homeRows{homes: map[string]model.Home{ns + "/" + owner: home}}
		f := files.Folders{Homes: rows, Namespace: ns, Root: root}
		if _, err := f.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) {
			t.Errorf("%s: err = %v, want ErrHomeStarting", name, err)
		}
		if len(rows.asked) != 1 || rows.asked[0] != ns+"/"+owner {
			t.Errorf("%s: asked for %v", name, rows.asked)
		}
	}
}

// A row is not trusted to say where a home is: one that names a folder
// anywhere but directly in the homes' folder (a claim's name from another
// install, a path elsewhere on the machine) is not served.
func TestFoldersRefuseAHomeOutsideTheirRoot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, dir := range []string{
		"home-" + owner, "/etc", root, filepath.Dir(root),
		filepath.Join(root, "home-"+owner, "deeper"), root + "/../elsewhere/home-" + owner, root + "/home-alice/..",
	} {
		rows := &homeRows{homes: map[string]model.Home{ns + "/" + owner: folderHome(dir)}}
		f := files.Folders{Homes: rows, Namespace: ns, Root: root}
		if got, err := f.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeConflict) {
			t.Errorf("home at %q: Ensure = %q, %v; want ErrHomeConflict", dir, got, err)
		}
	}
}

func TestFoldersUseTheNamespaceOfTheCall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "home-"+owner)
	rows := &homeRows{homes: map[string]model.Home{"team-a/" + owner: folderHome(dir)}}
	f := files.Folders{Homes: rows, Namespace: ns, Root: root}
	if got, err := f.Ensure(files.WithNamespace(context.Background(), "team-a"), owner); err != nil || got != dir {
		t.Fatalf("Ensure = %q, %v; want %q", got, err, dir)
	}
}
