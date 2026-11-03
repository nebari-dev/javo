// Package token implements personal API tokens stored in the database, the
// way sessions are (internal/auth/session).
//
// A token reads javo_<id>_<secret>. The id (10 random bytes, base32) is the
// row's id and is shown in lists; the secret (32 random bytes, base64url) is
// shown once, at creation, and never stored. The row keeps the SHA-256 of the
// whole token: a token is 256 bits from
// crypto/rand, so a fast hash is enough (OWASP Password Storage Cheat Sheet:
// slow hashes are for low-entropy secrets), and the comparison is constant
// time (crypto/subtle), so the lookup by id cannot become a timing oracle for
// the secret. Clients send the token as an RFC 6750 bearer token.
package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Prefix starts every token, so people and secret scanners recognise one.
const Prefix = model.ProductName + "_"

// Scope is what a token may do.
type Scope string

const (
	// ScopeRead allows GET and HEAD only.
	ScopeRead Scope = "read"
	// ScopeReadWrite allows everything its owner may do through the API.
	ScopeReadWrite Scope = "read-write"
)

// ScopeFor is the scope of the token an app with API access (model.APIAccess*)
// gets; "" for None or an unknown value.
func ScopeFor(access string) Scope {
	switch access {
	case model.APIAccessRead:
		return ScopeRead
	case model.APIAccessReadWrite:
		return ScopeReadWrite
	}
	return ""
}

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool { return s == ScopeRead || s == ScopeReadWrite }

// Allows reports whether a token of this scope may make a request with method.
func (s Scope) Allows(method string) bool {
	switch s {
	case ScopeReadWrite:
		return true
	case ScopeRead:
		return method == http.MethodGet || method == http.MethodHead
	}
	return false
}

// lastUseEvery is how often a token's last use is written at most.
const lastUseEvery = time.Minute

const (
	idBytes     = 10
	secretBytes = 32
)

var (
	// ErrInvalid is any token that does not authenticate: malformed,
	// unknown, revoked, wrong secret or expired. Callers answer them alike.
	ErrInvalid = errors.New("invalid token")
	// ErrNotFound is a token id that is not one of the owner's tokens.
	ErrNotFound = errors.New("token not found")
)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

var (
	idLen     = idEncoding.EncodedLen(idBytes)
	secretLen = base64.RawURLEncoding.EncodedLen(secretBytes)
)

// Pattern matches a token's text inside other text, so a secret scanner can
// hide every token whatever surrounds it.
var Pattern = regexp.MustCompile(regexp.QuoteMeta(Prefix) + fmt.Sprintf(`[a-z2-7]{%d}_[A-Za-z0-9_-]{%d}`, idLen, secretLen))

// App is the app a token was made for. Name is the name of the app's Pod and
// Service (its object name); Short is the name people see.
type App struct {
	Namespace string
	Name      string
	Short     string
}

// Token is a token's record, never its secret.
type Token struct {
	ID    string
	Name  string
	Owner string // the person's name
	Scope Scope
	// CreatedAt is set by Create.
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	// App is set on a token made for one app; it ends with the app.
	App *App
	// Job is the row id of the job a token was made for; it ends with the job.
	Job string
}

// Workload reports whether t was made for an app or a job rather than by a
// person: it lives where others can make it act, so it is kept from its
// owner's secrets.
func (t Token) Workload() bool { return t.App != nil || t.Job != "" }

// Prefix is the part of the token text that is safe to show: the product
// prefix and the id.
func (t Token) Prefix() string { return Prefix + t.ID + "_" }

// generate returns a new token text and its id.
func generate() (text, id string, err error) {
	raw := make([]byte, idBytes+secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	id = strings.ToLower(idEncoding.EncodeToString(raw[:idBytes]))
	return Prefix + id + "_" + base64.RawURLEncoding.EncodeToString(raw[idBytes:]), id, nil
}

// parse returns the id of a well-formed token text.
func parse(text string) (string, bool) {
	rest, ok := strings.CutPrefix(text, Prefix)
	if !ok || len(rest) != idLen+1+secretLen || rest[idLen] != '_' {
		return "", false
	}
	id, secret := rest[:idLen], rest[idLen+1:]
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '2' && r <= '7') {
			return "", false
		}
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(secret); err != nil {
		return "", false
	}
	return id, true
}

func digest(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

// DBStore keeps tokens in the api_tokens table.
type DBStore struct {
	Tokens *store.Tokens
	Now    func() time.Time // nil means time.Now
}

func (s *DBStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create stores a new token for t.Owner, who must be a known person, and
// returns its text, the only time the text exists outside the caller. ID and
// CreatedAt are filled in. The creation is audited under actor.
func (s *DBStore) Create(ctx context.Context, actor model.Actor, t Token) (string, Token, error) {
	if t.Owner == "" || !t.Scope.Valid() {
		return "", Token{}, fmt.Errorf("create token: owner %q, scope %q", t.Owner, t.Scope)
	}
	text, id, err := generate()
	if err != nil {
		return "", Token{}, err
	}
	t.ID, t.CreatedAt, t.LastUsedAt = id, s.now().UTC().Truncate(time.Second), nil
	row := store.APIToken{
		ID: id, Owner: t.Owner, Name: t.Name, Scope: string(t.Scope), Hash: hex.EncodeToString(digest(text)),
		Prefix: t.Prefix(), CreatedAt: t.CreatedAt, Job: t.Job,
	}
	if t.ExpiresAt != nil {
		row.ExpiresAt = *t.ExpiresAt
	}
	if t.App != nil {
		row.App = &store.TokenApp{ObjectName: t.App.Name}
	}
	if _, err := s.Tokens.Create(ctx, actor, row); err != nil {
		return "", Token{}, fmt.Errorf("create token: %w", err)
	}
	return text, t, nil
}

// tokenOf reads a row; a row with a scope this code does not know is not a token.
func tokenOf(r store.APIToken) (Token, bool) {
	t := Token{ID: r.ID, Name: r.Name, Owner: r.Owner, Scope: Scope(r.Scope), CreatedAt: r.CreatedAt, Job: r.Job}
	if !t.Scope.Valid() {
		return Token{}, false
	}
	if !r.ExpiresAt.IsZero() {
		at := r.ExpiresAt
		t.ExpiresAt = &at
	}
	if !r.LastUsedAt.IsZero() {
		at := r.LastUsedAt
		t.LastUsedAt = &at
	}
	if r.App != nil {
		t.App = &App{Namespace: r.App.Namespace, Name: r.App.ObjectName, Short: r.App.Name}
	}
	return t, true
}

func (s *DBStore) expired(t Token) bool {
	return t.ExpiresAt != nil && !s.now().Before(*t.ExpiresAt)
}

// Authenticate returns the record of a live token, or ErrInvalid.
func (s *DBStore) Authenticate(ctx context.Context, text string) (Token, error) {
	id, ok := parse(text)
	if !ok {
		return Token{}, ErrInvalid
	}
	row, err := s.Tokens.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Token{}, ErrInvalid
	}
	if err != nil {
		return Token{}, fmt.Errorf("read token: %w", err)
	}
	want, err := hex.DecodeString(row.Hash)
	if err != nil || subtle.ConstantTimeCompare(want, digest(text)) != 1 {
		return Token{}, ErrInvalid
	}
	t, ok := tokenOf(row)
	if !ok || s.expired(t) {
		return Token{}, ErrInvalid
	}
	return t, nil
}

// Get returns the record of the token with this id, live or expired, or
// ErrNotFound.
func (s *DBStore) Get(ctx context.Context, id string) (Token, error) {
	row, err := s.Tokens.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("read token: %w", err)
	}
	if t, ok := tokenOf(row); ok {
		return t, nil
	}
	return Token{}, ErrNotFound
}

// Exists reports whether a token with this id has a row.
func (s *DBStore) Exists(ctx context.Context, id string) (bool, error) {
	_, err := s.Tokens.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Touch records that t was used now, unless it was already recorded less than
// a minute ago. Failures are logged: a missed last-use time never fails a request.
func (s *DBStore) Touch(ctx context.Context, t Token) {
	now := s.now()
	if t.LastUsedAt != nil && now.Sub(*t.LastUsedAt) < lastUseEvery {
		return
	}
	if err := s.Tokens.Touch(ctx, t.ID, now, lastUseEvery); err != nil {
		slog.Warn("record token use", "err", err)
	}
}

// List returns owner's tokens, oldest first, expired ones included until the
// janitor removes them.
func (s *DBStore) List(ctx context.Context, owner string) ([]Token, error) {
	rows, err := s.Tokens.List(ctx, owner)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		if t, ok := tokenOf(r); ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// Revoke deletes owner's token id and returns its record; a token of someone
// else, or none, is ErrNotFound. It is audited under actor.
func (s *DBStore) Revoke(ctx context.Context, actor model.Actor, owner, id string) (Token, error) {
	row, err := s.Tokens.Revoke(ctx, actor, owner, id)
	if errors.Is(err, store.ErrNotFound) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("revoke token: %w", err)
	}
	t, _ := tokenOf(row)
	return t, nil
}

// RevokeForApp deletes the tokens made for the app with row id appID,
// except those of scope keep; keep "" deletes them all.
func (s *DBStore) RevokeForApp(ctx context.Context, actor model.Actor, appID string, keep Scope) (int64, error) {
	return s.Tokens.RevokeForApp(ctx, actor, appID, string(keep))
}

// AppCheck reports whether a token of scope made for app a is still
// wanted: the app exists and has API access of that scope.
type AppCheck func(ctx context.Context, a App, scope Scope) (bool, error)

// DeleteFor deletes every token of owner: for example when the local account
// is removed.
func (s *DBStore) DeleteFor(ctx context.Context, actor model.Actor, owner string) (int64, error) {
	return s.Tokens.DeleteFor(ctx, actor, owner)
}

// Sweep deletes expired tokens, tokens of apps marked deleted and app tokens
// their app no longer wants (wanted reports false: its API access changed),
// and returns how many. Every replica of the gateway runs it, so the
// unwanted ones are revoked, and audited, as model.SystemGateway.
func (s *DBStore) Sweep(ctx context.Context, wanted AppCheck) (int64, error) {
	n, err := s.Tokens.Sweep(ctx, s.now())
	if err != nil {
		return n, err
	}
	rows, err := s.Tokens.ListForApps(ctx)
	if err != nil {
		return n, fmt.Errorf("list app tokens: %w", err)
	}
	var errs []error
	for _, r := range rows {
		t, ok := tokenOf(r)
		keep := false
		if ok {
			if keep, err = wanted(ctx, *t.App, t.Scope); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if keep {
			continue
		}
		switch _, err := s.Tokens.Revoke(ctx, model.Actor{System: model.SystemGateway}, r.Owner, r.ID); {
		case err == nil:
			n++
		case !errors.Is(err, store.ErrNotFound):
			errs = append(errs, err)
		}
	}
	return n, errors.Join(errs...)
}

// RunJanitor calls Sweep every interval until ctx is done. Every gateway
// replica runs it; deletes are idempotent.
func (s *DBStore) RunJanitor(ctx context.Context, every time.Duration, wanted AppCheck) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Sweep(ctx, wanted); err != nil {
				slog.Error("sweep tokens", "err", err)
			}
		}
	}
}
