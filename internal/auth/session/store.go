// Package session implements server-side sessions stored in the database.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// DefaultTTL is the session lifetime when DBStore.TTL is zero.
const DefaultTTL = 12 * time.Hour

// Session kinds. A ui session is what the browser cookie names; an app
// session or ticket is minted from one and ends with it.
const (
	KindUI     = "ui"
	KindApp    = "app"
	KindTicket = "ticket"
)

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionExpired  = errors.New("session expired")
	// ErrSessionChanged means another writer updated the session since it
	// was read; re-read and retry.
	ErrSessionChanged = errors.New("session changed")
)

// Session is one sign-in. Refresh and IDToken live only in the database (they
// are needed to refresh and for id_token_hint), never in a cookie or the API.
type Session struct {
	Principal authz.Principal
	Provider  string
	Kind      string // KindUI, KindApp or KindTicket
	// Parent is the ID of the ui session an app session or ticket came from.
	Parent string
	// Workspace is "namespace/name", for app sessions and tickets.
	Workspace string
	Refresh   string
	IDToken   string
	Expires   time.Time
	// RevalidateAt is when the provider must be asked again.
	RevalidateAt time.Time

	// version is the row's version at read time; Update writes against it.
	version int64
}

// Store persists sessions keyed by an opaque bearer token.
type Store interface {
	// Create stores s and returns its token. A zero Expires means now + TTL.
	Create(ctx context.Context, s Session) (token string, err error)
	Get(ctx context.Context, token string) (Session, error)
	// Update replaces a session previously returned by Get. It returns
	// ErrSessionChanged if the stored session changed since that read.
	Update(ctx context.Context, token string, s Session) error
	Delete(ctx context.Context, token string) error
	// GetByID, UpdateByID and DeleteByID act on a ui session named by its ID
	// (ID of its token) rather than its token: that is how an app session's
	// Parent reaches it. GetByID of anything but a live ui session is
	// ErrSessionNotFound.
	GetByID(ctx context.Context, id string) (Session, error)
	UpdateByID(ctx context.Context, id string, s Session) error
	DeleteByID(ctx context.Context, id string) error
	// DeleteFor ends every session of the Identity and returns how many.
	DeleteFor(ctx context.Context, identity string) (int, error)
	// Redeem returns a ticket and deletes it in one step; a second Redeem of
	// the same token fails with ErrSessionNotFound.
	Redeem(ctx context.Context, token string) (Session, error)
	// Sweep deletes expired sessions and returns how many.
	Sweep(ctx context.Context) (int, error)
}

// SystemSessions is the audit actor for ending sessions in bulk (DeleteFor),
// which the interface takes no caller for.
const SystemSessions = "system:sessions"

// DBStore keeps each session in the sessions table under the SHA-256 of the
// token, so the raw token is never written down. With a Key, the identity
// provider's refresh and id tokens are sealed with it (AES-256-GCM, bound to
// the row and the column), so a copy of the database, such as a backup,
// holds no live credential.
type DBStore struct {
	Sessions *store.Sessions
	// Key seals the provider's tokens. nil stores them in clear: for tests
	// and for commands that only end sessions; the gateway always sets it.
	Key tink.AEAD
	TTL time.Duration    // zero means DefaultTTL
	Now func() time.Time // nil means time.Now
}

var _ Store = (*DBStore)(nil)

func (s *DBStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *DBStore) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}

// ID is the hashed identifier of a session: its row id, safe to store and to
// use as Session.Parent.
func ID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:16])
}

func validKind(k string) bool { return k == KindUI || k == KindApp || k == KindTicket }

// Columns of the sealed tokens, part of what each is bound to.
const (
	colRefresh = "refresh"
	colIDToken = "id_token"
)

// sealedPrefix marks a sealed token: the version of the scheme, then the
// ciphertext in base64.
const sealedPrefix = "v1."

// errUnsealed is a stored token that does not open with the key: another
// key, another row's, or written in clear.
var errUnsealed = errors.New("the session's provider tokens do not open")

// tokenAD binds a sealed token to its row and column, so one copied onto
// another row or column does not open.
func tokenAD(id, column string) []byte {
	b, _ := json.Marshal([]string{"javo session token v1", id, column})
	return b
}

func (s *DBStore) seal(id, column, v string) (string, error) {
	if s.Key == nil || v == "" {
		return v, nil
	}
	ct, err := s.Key.Encrypt([]byte(v), tokenAD(id, column))
	if err != nil {
		return "", fmt.Errorf("seal the session's %s: %w", column, err)
	}
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

func (s *DBStore) open(id, column, v string) (string, error) {
	if s.Key == nil || v == "" {
		return v, nil
	}
	b64, ok := strings.CutPrefix(v, sealedPrefix)
	if !ok {
		return "", errUnsealed
	}
	ct, err := base64.RawStdEncoding.DecodeString(b64)
	if err != nil {
		return "", errUnsealed
	}
	pt, err := s.Key.Decrypt(ct, tokenAD(id, column))
	if err != nil {
		return "", errUnsealed
	}
	return string(pt), nil
}

func (s *DBStore) rowOf(id string, sess Session) (store.Session, error) {
	principal, err := json.Marshal(sess.Principal)
	if err != nil {
		return store.Session{}, err
	}
	refresh, err := s.seal(id, colRefresh, sess.Refresh)
	if err != nil {
		return store.Session{}, err
	}
	idToken, err := s.seal(id, colIDToken, sess.IDToken)
	if err != nil {
		return store.Session{}, err
	}
	return store.Session{
		ID: id, Kind: sess.Kind, ParentID: sess.Parent, Person: sess.Principal.ID, Principal: string(principal),
		Provider: sess.Provider, Workspace: sess.Workspace, Refresh: refresh, IDToken: idToken,
		ExpiresAt: sess.Expires, RevalidateAt: sess.RevalidateAt, Version: sess.version,
	}, nil
}

func (s *DBStore) sessionOf(row store.Session) (Session, error) {
	sess := Session{
		Provider: row.Provider, Kind: row.Kind, Parent: row.ParentID, Workspace: row.Workspace,
		Expires: row.ExpiresAt, RevalidateAt: row.RevalidateAt, version: row.Version,
	}
	var err error
	if sess.Refresh, err = s.open(row.ID, colRefresh, row.Refresh); err != nil {
		return Session{}, err
	}
	if sess.IDToken, err = s.open(row.ID, colIDToken, row.IDToken); err != nil {
		return Session{}, err
	}
	return sess, json.Unmarshal([]byte(row.Principal), &sess.Principal)
}

// Create stores a new session and returns its token (32 random bytes, base64url).
func (s *DBStore) Create(ctx context.Context, sess Session) (string, error) {
	if !validKind(sess.Kind) {
		return "", fmt.Errorf("create session: unknown kind %q", sess.Kind)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if sess.Expires.IsZero() {
		sess.Expires = s.now().Add(s.ttl())
	}
	row, err := s.rowOf(ID(token), sess)
	if err != nil {
		return "", err
	}
	if err := s.Sessions.Create(ctx, model.Actor{Person: sess.Principal.ID}, row); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return token, nil
}

// Get returns the session for token: ErrSessionNotFound if unknown, or an app
// session or ticket whose parent is gone, expired, not a ui session or
// another identity's, and ErrSessionExpired (removing it) if past its expiry.
func (s *DBStore) Get(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionNotFound
	}
	return s.get(ctx, ID(token))
}

// GetByID returns the live ui session with the given ID; an app session, a
// ticket or an unknown ID is ErrSessionNotFound.
func (s *DBStore) GetByID(ctx context.Context, id string) (Session, error) {
	if id == "" {
		return Session{}, ErrSessionNotFound
	}
	sess, err := s.get(ctx, id)
	if err == nil && sess.Kind != KindUI {
		return Session{}, ErrSessionNotFound
	}
	return sess, err
}

// get reads the session in row id.
func (s *DBStore) get(ctx context.Context, id string) (Session, error) {
	row, err := s.Sessions.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && !validKind(row.Kind) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	sess, err := s.sessionOf(row)
	if errors.Is(err, errUnsealed) {
		// Nobody can use it any more: it ends, and the person signs in again.
		slog.Warn("ending a session whose provider tokens do not open with the sessions key", "kind", row.Kind)
		_, _ = s.Sessions.Redeem(ctx, row.ID)
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	if !s.now().Before(sess.Expires) {
		// Like Sweep, an expiry is not audited: nobody signed out.
		_, _ = s.Sessions.Redeem(ctx, row.ID)
		return Session{}, ErrSessionExpired
	}
	if sess.Kind != KindUI {
		parent, err := s.Sessions.Get(ctx, sess.Parent)
		if errors.Is(err, store.ErrNotFound) {
			return Session{}, ErrSessionNotFound
		}
		if err != nil {
			return Session{}, fmt.Errorf("read parent session: %w", err)
		}
		// Judged by the row alone, so an expired parent the janitor has not
		// swept yet still ends its children.
		if parent.Kind != KindUI || parent.Person != row.Person || !s.now().Before(parent.ExpiresAt) {
			return Session{}, ErrSessionNotFound
		}
		// Who the person is comes from the ui session, which the provider
		// keeps current, so a group lost there is lost here at once.
		ps, err := s.sessionOf(parent)
		if err != nil {
			return Session{}, ErrSessionNotFound
		}
		sess.Principal = ps.Principal
	}
	return sess, nil
}

// Redeem returns the live ticket for token and deletes it, so a ticket works
// once. Of several concurrent redeems of the same ticket exactly one
// succeeds; the others get ErrSessionNotFound. Any other kind is refused
// untouched.
func (s *DBStore) Redeem(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionNotFound
	}
	sess, err := s.get(ctx, ID(token))
	if err != nil {
		return Session{}, err
	}
	if sess.Kind != KindTicket {
		return Session{}, ErrSessionNotFound
	}
	if _, err := s.Sessions.Redeem(ctx, ID(token)); errors.Is(err, store.ErrNotFound) {
		return Session{}, ErrSessionNotFound
	} else if err != nil {
		return Session{}, fmt.Errorf("redeem ticket: %w", err)
	}
	return sess, nil
}

// Update replaces the stored session with sess, which must come from Get.
func (s *DBStore) Update(ctx context.Context, token string, sess Session) error {
	return s.update(ctx, ID(token), sess)
}

// UpdateByID is Update for the ui session with the given ID.
func (s *DBStore) UpdateByID(ctx context.Context, id string, sess Session) error {
	return s.update(ctx, id, sess)
}

func (s *DBStore) update(ctx context.Context, id string, sess Session) error {
	if sess.version == 0 {
		return errors.New("update session: session was not read with Get")
	}
	if !validKind(sess.Kind) {
		return fmt.Errorf("update session: unknown kind %q", sess.Kind)
	}
	row, err := s.rowOf(id, sess)
	if err != nil {
		return err
	}
	switch err := s.Sessions.Update(ctx, row); {
	case errors.Is(err, store.ErrConflict):
		return ErrSessionChanged
	case errors.Is(err, store.ErrNotFound):
		return ErrSessionNotFound
	case err != nil:
		return fmt.Errorf("update session: %w", err)
	}
	return nil
}

// Delete removes the session; deleting an unknown session is not an error.
func (s *DBStore) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.deleteID(ctx, ID(token))
}

// DeleteByID is Delete for the ui session with the given ID.
func (s *DBStore) DeleteByID(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	return s.deleteID(ctx, id)
}

func (s *DBStore) deleteID(ctx context.Context, id string) error {
	row, err := s.Sessions.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return s.deleteRow(ctx, row)
}

// deleteRow ends the session by the person it belongs to, which is who the
// audit row names for a sign-out. Expiry does not come through here.
func (s *DBStore) deleteRow(ctx context.Context, row store.Session) error {
	if err := s.Sessions.Delete(ctx, model.Actor{Person: row.Person}, row.ID); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteFor ends every session of the Identity: for example when its local
// account is removed or its password replaced.
func (s *DBStore) DeleteFor(ctx context.Context, identity string) (int, error) {
	n, err := s.Sessions.DeleteFor(ctx, model.Actor{System: SystemSessions}, identity)
	return int(n), err
}

// Sweep deletes every session that has expired (its app sessions and tickets
// go with it) and returns how many it removed directly.
func (s *DBStore) Sweep(ctx context.Context) (int, error) {
	n, err := s.Sessions.Sweep(ctx, s.now())
	return int(n), err
}

// RunJanitor calls Sweep every interval until ctx is done. Every gateway
// replica runs it; deletes are idempotent.
func (s *DBStore) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Sweep(ctx); err != nil {
				slog.Error("sweep sessions", "err", err)
			}
		}
	}
}
