package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

const (
	// shortHex and longHex are the two name forms (alice-<hash>): the short
	// one, and the long one when another person holds the short one.
	shortHex = 8
	longHex  = 12
	// maxGroupsSnapshot is the most groups (and provider roles) kept per
	// person; a person with more has the first ones kept, so a gated
	// scheduled run fails closed for the dropped ones instead of the login
	// failing.
	maxGroupsSnapshot = 200
	// MinSearchLen is the shortest prefix Search answers, so the share
	// picker cannot list everyone.
	MinSearchLen = 2

	counterUID = "uid"
	counterGID = "gid"
)

// ErrRangeExhausted is returned when every number in an IDRange is taken.
var ErrRangeExhausted = errors.New("store: id range exhausted")

// IDRange is the inclusive range uids or gids are allocated from. The zero
// value means "allocate nothing".
type IDRange struct{ First, Last int64 }

func (r IDRange) usable() bool { return r.First > 0 && r.Last >= r.First }

// ErrIDTaken is returned when the number a counter handed out is held by a
// row the counter did not create (an import, another writer) and the insert
// raced with it.
var ErrIDTaken = errors.New("store: id already taken")

// allocate takes the next free number of the counter in r, inside the
// caller's transaction (a rolled-back write returns its number). Numbers that
// taken reports as held by an existing row are skipped, so rows written by
// another path never block the counter.
func allocate(ctx context.Context, q *sqlcgen.Queries, counter string, r IDRange, taken func(context.Context, int64) (bool, error)) (int64, error) {
	if err := q.SeedCounter(ctx, sqlcgen.SeedCounterParams{Name: counter, Next: r.First}); err != nil {
		return 0, err
	}
	for {
		n, err := q.AllocateID(ctx, sqlcgen.AllocateIDParams{Next: r.First, Name: counter})
		if err != nil {
			return 0, err
		}
		if n > r.Last {
			return 0, fmt.Errorf("%w: %s %d-%d", ErrRangeExhausted, counter, r.First, r.Last)
		}
		held, err := taken(ctx, n)
		if err != nil {
			return 0, err
		}
		if !held {
			return n, nil
		}
	}
}

// Claims is what an auth provider learned about a person at sign-in.
type Claims struct {
	Issuer, Subject, Username, Email, DisplayName string
	// Groups are the provider's group values, verbatim.
	Groups []string
	// ProviderRoles are the provider's role values, verbatim.
	ProviderRoles []string
}

// People is the people table. A person is the pair (issuer, subject) from
// their identity provider, never a username or email.
type People struct {
	s    *Store
	uids IDRange
}

// People returns the people table. New people get a uid from uids; a zero
// range allocates none (uid mode fixed).
func (s *Store) People(uids IDRange) *People { return &People{s: s, uids: uids} }

// errRetry means a concurrent first login of the same person won; read again.
var errRetry = errors.New("retry")

// Ensure returns the person for c, creating them at first sign-in with the
// next free uid and the name alice-<hash> (frozen from then on). Username,
// email, display name, groups and last login are updated on every call.
func (p *People) Ensure(ctx context.Context, c Claims) (model.Person, error) {
	var err error
	for range 3 {
		var person model.Person
		if person, err = p.ensureOnce(ctx, c); !errors.Is(err, errRetry) && !errors.Is(err, ErrIDTaken) {
			return person, err
		}
	}
	return model.Person{}, fmt.Errorf("store: ensure person: gave up after three attempts: %w", err)
}

// snapshot is values capped at maxGroupsSnapshot, as the JSON a column keeps.
func snapshot(values []string) (string, error) {
	if len(values) > maxGroupsSnapshot {
		values = values[:maxGroupsSnapshot]
	}
	return marshal(values, "[]")
}

// lists are a sign-in's groups and provider roles as stored.
type lists struct{ groups, providerRoles string }

func (p *People) ensureOnce(ctx context.Context, c Claims) (model.Person, error) {
	var l lists
	var err error
	if l.groups, err = snapshot(c.Groups); err != nil {
		return model.Person{}, err
	}
	if l.providerRoles, err = snapshot(c.ProviderRoles); err != nil {
		return model.Person{}, err
	}
	var row sqlcgen.Person
	err = p.s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		var err error
		if row, err = personBySubject(ctx, q, c.Issuer, c.Subject); err == nil {
			row, err = refreshLogin(ctx, q, row, c, l)
			return nil, err
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		row, err = p.create(ctx, q, c, l)
		if err != nil {
			return nil, err
		}
		return []Change{{Table: "people", ID: row.ID}}, nil
	})
	if err != nil {
		return model.Person{}, err
	}
	person, err := personOf(ctx, p.s.q, row)
	if err != nil {
		return model.Person{}, err
	}
	return person, nil
}

func personBySubject(ctx context.Context, q *sqlcgen.Queries, issuer, subject string) (sqlcgen.Person, error) {
	row, err := q.GetPersonBySubject(ctx, sqlcgen.GetPersonBySubjectParams{Issuer: issuer, Subject: subject})
	if errors.Is(err, sql.ErrNoRows) {
		return q.GetPersonByLinkedSubject(ctx, sqlcgen.GetPersonByLinkedSubjectParams{Issuer: issuer, Subject: subject})
	}
	return row, err
}

func refreshLogin(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.Person, c Claims, l lists) (sqlcgen.Person, error) {
	row.Username, row.Email, row.DisplayName = c.Username, c.Email, c.DisplayName
	row.UsernameLower, row.EmailLower = strings.ToLower(c.Username), strings.ToLower(c.Email)
	row.Groups, row.ProviderRoles, row.LastLoginAt = l.groups, l.providerRoles, dbtime.From(now())
	return row, q.UpdatePersonLogin(ctx, sqlcgen.UpdatePersonLoginParams{
		Username: row.Username, Email: row.Email, DisplayName: row.DisplayName,
		UsernameLower: row.UsernameLower, EmailLower: row.EmailLower, Groups: l.groups,
		LastLoginAt: row.LastLoginAt, ProviderRoles: l.providerRoles, ID: row.ID,
	})
}

func (p *People) create(ctx context.Context, q *sqlcgen.Queries, c Claims, l lists) (sqlcgen.Person, error) {
	candidate := model.Person{Subjects: []model.Subject{{Issuer: c.Issuer, Subject: c.Subject}}}
	if err := candidate.Validate(); err != nil {
		return sqlcgen.Person{}, err
	}
	uid := sql.NullInt64{}
	if p.uids.usable() {
		n, err := allocate(ctx, q, counterUID, p.uids, uidTaken(q))
		if err != nil {
			return sqlcgen.Person{}, err
		}
		uid = sql.NullInt64{Int64: n, Valid: true}
	}
	id, at := newID(), dbtime.From(now())
	for _, hexLen := range []int{shortHex, longHex} {
		name := naming.IdentityName(c.Username, c.Issuer, c.Subject, hexLen)
		row, err := q.InsertPerson(ctx, sqlcgen.InsertPersonParams{
			ID: id, Name: name, Issuer: c.Issuer, Subject: c.Subject, Username: c.Username, Email: c.Email,
			DisplayName: c.DisplayName, UsernameLower: strings.ToLower(c.Username), EmailLower: strings.ToLower(c.Email),
			Uid: uid, Groups: l.groups, LastLoginAt: at, CreatedAt: at, ProviderRoles: l.providerRoles,
		})
		if err == nil {
			return row, recordAudit(ctx, q, model.Actor{Person: name}, auditRow{action: "person.create", kind: "person", name: name})
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return sqlcgen.Person{}, err
		}
		// Nothing inserted: the name is taken, or this same person just signed
		// in for the first time on another connection.
		if _, err := personBySubject(ctx, q, c.Issuer, c.Subject); err == nil {
			return sqlcgen.Person{}, errRetry
		} else if !errors.Is(err, sql.ErrNoRows) {
			return sqlcgen.Person{}, err
		}
		if uid.Valid {
			if taken, err := uidTaken(q)(ctx, uid.Int64); err != nil {
				return sqlcgen.Person{}, err
			} else if taken {
				return sqlcgen.Person{}, fmt.Errorf("%w: uid %d", ErrIDTaken, uid.Int64)
			}
		}
	}
	return sqlcgen.Person{}, errors.New("store: create person: both name forms are taken by other subjects")
}

// ByName returns the person with this name, or ErrNotFound.
func (p *People) ByName(ctx context.Context, name string) (model.Person, error) {
	row, err := p.s.q.GetPersonByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Person{}, ErrNotFound
	}
	if err != nil {
		return model.Person{}, err
	}
	return personOf(ctx, p.s.q, row)
}

// ByID returns the person with this row id (the id a "people" change names),
// or ErrNotFound.
func (p *People) ByID(ctx context.Context, id string) (model.Person, error) {
	row, err := p.s.q.GetPerson(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Person{}, ErrNotFound
	}
	if err != nil {
		return model.Person{}, err
	}
	return personOf(ctx, p.s.q, row)
}

// IDs returns the row id of every person, for the controller's resync.
func (p *People) IDs(ctx context.Context) ([]string, error) {
	return p.s.q.ListPersonIDs(ctx)
}

// Search returns at most limit people whose username or email starts with
// prefix, ignoring case, ordered by name. A prefix shorter than MinSearchLen
// returns nothing.
func (p *People) Search(ctx context.Context, prefix string, limit int) ([]model.Person, error) {
	prefix = strings.ToLower(prefix)
	if utf8.RuneCountInString(prefix) < MinSearchLen || limit <= 0 {
		return nil, nil
	}
	rows, err := p.s.q.SearchPeople(ctx, sqlcgen.SearchPeopleParams{
		UsernameLower: prefix, Substr: int32(utf8.RuneCountInString(prefix)), Limit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.Person, 0, len(rows))
	for _, row := range rows {
		person, err := personOf(ctx, p.s.q, row)
		if err != nil {
			return nil, err
		}
		out = append(out, person)
	}
	return out, nil
}

// ByLogin returns everyone whose username or email is login, ignoring case,
// ordered by name. Neither is unique: providers reuse them, and two
// providers can each have an "alice".
func (p *People) ByLogin(ctx context.Context, login string) ([]model.Person, error) {
	rows, err := p.s.q.ListPeopleByLogin(ctx, strings.ToLower(login))
	if err != nil {
		return nil, err
	}
	out := make([]model.Person, 0, len(rows))
	for _, row := range rows {
		person, err := personOf(ctx, p.s.q, row)
		if err != nil {
			return nil, err
		}
		out = append(out, person)
	}
	return out, nil
}

func personOf(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.Person) (model.Person, error) {
	linked, err := q.ListLinkedSubjects(ctx, row.ID)
	if err != nil {
		return model.Person{}, err
	}
	person := model.Person{
		ID: row.ID, Name: row.Name, Username: row.Username, Email: row.Email, DisplayName: row.DisplayName,
		UID: row.Uid.Int64, LastLoginAt: row.LastLoginAt.Time(),
		Subjects: []model.Subject{{Issuer: row.Issuer, Subject: row.Subject}},
	}
	for _, l := range linked {
		person.Subjects = append(person.Subjects, model.Subject{Issuer: l.Issuer, Subject: l.Subject})
	}
	if err := unmarshal(row.Groups, &person.Groups); err != nil {
		return model.Person{}, err
	}
	return person, unmarshal(row.ProviderRoles, &person.ProviderRoles)
}

// Groups is the groups table: the numeric gid each provider group gets the
// first time it is seen.
type Groups struct {
	s    *Store
	gids IDRange
}

// Groups returns the groups table; new groups get a gid from gids.
func (s *Store) Groups(gids IDRange) *Groups { return &Groups{s: s, gids: gids} }

// Ensure returns the gid of group name, allocating one on first sight. The
// gid never changes afterwards.
func (g *Groups) Ensure(ctx context.Context, actor model.Actor, name string) (int64, error) {
	if !g.gids.usable() {
		return 0, errors.New("store: no gid range configured")
	}
	for range 3 {
		var gid int64
		err := g.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
			row, err := q.GetGroup(ctx, name)
			if err == nil {
				gid = row.Gid
				return effect{}, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return effect{}, err
			}
			if gid, err = allocate(ctx, q, counterGID, g.gids, gidTaken(q)); err != nil {
				return effect{}, err
			}
			n, err := q.InsertGroup(ctx, sqlcgen.InsertGroupParams{Name: name, Gid: gid})
			if err == nil && n == 0 {
				return effect{}, errRetry // another connection added it first
			}
			// A gid is permanent (files on disk carry it), so its allocation is audited.
			return effect{audit: &auditRow{action: "group.create", kind: "group", name: name, detail: map[string]int64{"gid": gid}}}, err
		})
		if !errors.Is(err, errRetry) {
			return gid, err
		}
	}
	return 0, errors.New("store: ensure group: lost the race three times")
}

func uidTaken(q *sqlcgen.Queries) func(context.Context, int64) (bool, error) {
	return func(ctx context.Context, uid int64) (bool, error) {
		n, err := q.UIDTaken(ctx, sql.NullInt64{Int64: uid, Valid: true})
		return n > 0, err
	}
}

func gidTaken(q *sqlcgen.Queries) func(context.Context, int64) (bool, error) {
	return func(ctx context.Context, gid int64) (bool, error) {
		n, err := q.GIDTaken(ctx, gid)
		return n > 0, err
	}
}

// BySubject returns the person who signs in as (issuer, subject), or
// ErrNotFound.
func (p *People) BySubject(ctx context.Context, issuer, subject string) (model.Person, error) {
	row, err := personBySubject(ctx, p.s.q, issuer, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Person{}, ErrNotFound
	}
	if err != nil {
		return model.Person{}, err
	}
	return personOf(ctx, p.s.q, row)
}
