package ids

import (
	"context"
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// people is a fixed set of person rows by name.
type people map[string]model.Person

func (p people) ByName(_ context.Context, name string) (model.Person, error) {
	if v, ok := p[name]; ok {
		return v, nil
	}
	return model.Person{}, store.ErrNotFound
}

var bg = context.Background()

func TestFixedModeGivesEveryoneTheSameIDs(t *testing.T) {
	u := People{People: people{}, Mode: config.UIDModeFixed}
	uid, gid, err := u.UID(bg, "alice-1a2b3c4d")
	if err != nil || uid != 1000 || gid != 100 {
		t.Fatalf("uid, gid, err = %d, %d, %v; want 1000, 100", uid, gid, err)
	}
}

func TestAllocatedModeReadsThePerson(t *testing.T) {
	u := People{People: people{"alice-1a2b3c4d": {Name: "alice-1a2b3c4d", UID: 100500}}, Mode: config.UIDModeAllocated}
	uid, gid, err := u.UID(bg, "alice-1a2b3c4d")
	if err != nil || uid != 100500 || gid != 100 {
		t.Fatalf("uid, gid, err = %d, %d, %v; want 100500, 100", uid, gid, err)
	}
}

// No uid yet is a state to wait out, not a failure to retry with backoff.
func TestAllocatedModeWithoutAUIDIsPending(t *testing.T) {
	u := People{People: people{"bob-5e6f7a8b": {Name: "bob-5e6f7a8b"}}, Mode: config.UIDModeAllocated}
	for _, owner := range []string{"bob-5e6f7a8b", "nobody-00000000"} {
		if _, _, err := u.UID(bg, owner); !errors.Is(err, ErrNoUID) {
			t.Errorf("%s: err = %v, want ErrNoUID", owner, err)
		}
	}
}

func TestPersonGroupsReadsTheGroupsOfLastLogin(t *testing.T) {
	g := PersonGroups{People: people{"alice-1a2b3c4d": {Name: "alice-1a2b3c4d", Groups: []string{"research", "/ops"}}}}
	got, err := g.Groups(bg, "alice-1a2b3c4d")
	if err != nil || len(got) != 2 || got[0] != "research" || got[1] != "/ops" {
		t.Fatalf("groups, err = %v, %v", got, err)
	}
	// Someone who never signed in is in no group.
	if got, err := g.Groups(bg, "nobody-00000000"); err != nil || len(got) != 0 {
		t.Fatalf("groups, err = %v, %v", got, err)
	}
}

// ensurer hands out gids from 2001 on, one per new group.
type ensurer map[string]int64

func (e ensurer) Ensure(_ context.Context, _ model.Actor, name string) (int64, error) {
	if gid, ok := e[name]; ok {
		return gid, nil
	}
	e[name] = int64(2001 + len(e))
	return e[name], nil
}

// A group gets its gid the first time a folder asks, and keeps it.
func TestAllocatedGroupsGivesEachGroupOneGID(t *testing.T) {
	g := AllocatedGroups{Groups: ensurer{}}
	first, ok, err := g.GID(bg, "/org/data-science")
	if err != nil || !ok || first != 2001 {
		t.Fatalf("gid, ok, err = %d, %v, %v", first, ok, err)
	}
	if again, _, _ := g.GID(bg, "/org/data-science"); again != first {
		t.Fatalf("gid changed from %d to %d", first, again)
	}
	if other, _, _ := g.GID(bg, "/org/ops"); other == first {
		t.Fatal("two groups share a gid")
	}
}
