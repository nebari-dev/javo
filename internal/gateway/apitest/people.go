package apitest

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const TestIssuer = "https://idp.example.com"

// PersonID is the name a person called username gets when they sign in with
// the subject testPerson uses.
func PersonID(username string) string {
	return naming.IdentityName(username, TestIssuer, "sub-"+username, 8)
}

// TestPerson is someone who has signed in to the gateway before a test
// starts. A zero lastLogin keeps the real time of the sign-in.
type TestPerson struct {
	Username  string
	LastLogin time.Time
	// NoName signs in without a display name, as a local account does.
	NoName bool
}

func SignedIn(username string, lastLogin time.Time) TestPerson {
	return TestPerson{Username: username, LastLogin: lastLogin}
}

// SignedInNoName is someone whose sign-in carried no display name.
func SignedInNoName(username string) TestPerson {
	return TestPerson{Username: username, NoName: true}
}

func (p TestPerson) SignIn(t *testing.T, db *store.Store, people authn.People) {
	t.Helper()
	display := strings.ToUpper(p.Username[:1]) + p.Username[1:]
	if p.NoName {
		display = ""
	}
	person, err := people.Ensure(context.Background(), store.Claims{
		Issuer: TestIssuer, Subject: "sub-" + p.Username, Username: p.Username, Email: p.Username + "@example.com",
		DisplayName: display,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.LastLogin.IsZero() {
		storetest.Exec(t, db, "UPDATE people SET last_login_at = ? WHERE name = ?", p.LastLogin.UnixMilli(), person.Name)
	}
}

// DeletePerson removes a person, as offboarding would.
func (f *Fixture) DeletePerson(name string) {
	f.T.Helper()
	storetest.Exec(f.T, f.Db, "DELETE FROM people WHERE name = ?", name)
}

// FailablePeople is the people table until fail is set, then every call
// reports the database as unavailable.
type FailablePeople struct {
	authn.People
	Fail atomic.Bool
}

var ErrPeopleDown = errors.New("people unavailable")

func (p *FailablePeople) Ensure(ctx context.Context, c store.Claims) (model.Person, error) {
	if p.Fail.Load() {
		return model.Person{}, ErrPeopleDown
	}
	return p.People.Ensure(ctx, c)
}

func (p *FailablePeople) ByName(ctx context.Context, name string) (model.Person, error) {
	if p.Fail.Load() {
		return model.Person{}, ErrPeopleDown
	}
	return p.People.ByName(ctx, name)
}

func (p *FailablePeople) Search(ctx context.Context, prefix string, limit int) ([]model.Person, error) {
	if p.Fail.Load() {
		return nil, ErrPeopleDown
	}
	return p.People.Search(ctx, prefix, limit)
}
