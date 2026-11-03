// Package local implements local username/password accounts stored as
// argon2id hashes in a Kubernetes Secret.
package local

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/store"
)

// OWASP argon2id parameters.
const (
	argonMemory  uint32 = 19456 // KiB
	argonTime    uint32 = 2
	argonThreads uint8  = 1
	saltLen             = 16
	keyLen              = 32

	// Upper bounds accepted when parsing a stored hash, so a corrupted
	// row cannot make Verify allocate unbounded memory.
	maxMemory  = 256 * 1024
	maxTime    = 10
	maxThreads = 16
)

var (
	// ErrInvalidCredentials is returned for every failed login: unknown
	// user, bad username and wrong password all yield this same value.
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrUserExists is returned by Add when the user exists and force is false.
	ErrUserExists = errors.New("user already exists")
	// ErrUserNotFound is returned by Remove when there is no such user.
	ErrUserNotFound = errors.New("user not found")
)

// User is an authenticated principal.
type User struct {
	ID     string
	Name   string
	Groups []string
}

// Users verifies credentials.
type Users interface {
	Verify(ctx context.Context, username, password string) (User, error)
	// Lookup returns the account without a password check, or
	// ErrUserNotFound. Sessions use it to notice a deleted account.
	Lookup(ctx context.Context, username string) (User, error)
}

// usernameRe is the set of accepted account names.
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidUsername reports whether name can be used as an account name.
func ValidUsername(name string) bool { return usernameRe.MatchString(name) }

// HashPassword returns an argon2id PHC string for pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the PHC string encoded.
// Malformed or out-of-bounds hashes never match.
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	if n, err := fmt.Sscanf(parts[2], "v=%d", &version); n != 1 || err != nil || version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); n != 3 || err != nil {
		return false
	}
	if m == 0 || m > maxMemory || t == 0 || t > maxTime || p == 0 || p > maxThreads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnHash spends the same CPU as a real verification so unknown users
// cannot be told apart by response time.
func burnHash(pw string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("javo-dummy-password") })
	VerifyPassword(dummyHash, pw)
}

// DBUsers is a Users backed by the local_accounts table.
type DBUsers struct {
	Accounts *store.LocalAccounts
}

// read returns the stored account of username; ok is false when there is none.
func (u *DBUsers) read(ctx context.Context, username string) (a store.LocalAccount, ok bool, err error) {
	if !ValidUsername(username) {
		return store.LocalAccount{}, false, nil
	}
	a, err = u.Accounts.Get(ctx, username)
	switch {
	case err == nil:
		return a, a.Hash != "", nil
	case errors.Is(err, store.ErrNotFound):
		return store.LocalAccount{}, false, nil
	default:
		return store.LocalAccount{}, false, fmt.Errorf("read local account: %w", err)
	}
}

// Verify checks username and password. Every credential failure returns
// ErrInvalidCredentials; other errors indicate infrastructure trouble.
func (u *DBUsers) Verify(ctx context.Context, username, password string) (User, error) {
	a, ok, err := u.read(ctx, username)
	if err != nil {
		return User{}, err
	}
	if !ok {
		burnHash(password)
		return User{}, ErrInvalidCredentials
	}
	if !VerifyPassword(a.Hash, password) {
		return User{}, ErrInvalidCredentials
	}
	return User{ID: UserID(username), Name: username, Groups: a.Groups}, nil
}

// Lookup returns the account username, or ErrUserNotFound.
func (u *DBUsers) Lookup(ctx context.Context, username string) (User, error) {
	a, ok, err := u.read(ctx, username)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, ErrUserNotFound
	}
	return User{ID: UserID(username), Name: username, Groups: a.Groups}, nil
}

// Issuer stands in for the identity provider of local accounts, so a
// local user's ID has the same shape as any other Identity name.
const Issuer = model.LocalIssuer

const localIssuer = Issuer

// UserID is the ID of the local account username: an Identity name whose
// subject is the username.
func UserID(username string) string {
	return naming.IdentityName(username, localIssuer, username, 8)
}

// cliActor is who the audit log names for account changes: they are made by
// the operator through `javo user`.
var cliActor = model.Actor{System: model.SystemCLI}

// Add stores hash and groups for username. Without force it fails with
// ErrUserExists if the user is already present.
func (u *DBUsers) Add(ctx context.Context, username, hash string, groups []string, force bool) error {
	if !ValidUsername(username) {
		return fmt.Errorf("invalid username %q: use letters, digits, '.', '_' or '-', up to 63 characters", username)
	}
	existing, err := u.Accounts.Get(ctx, username)
	switch {
	case err == nil:
		if !force {
			return ErrUserExists
		}
	case !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("read local account: %w", err)
	}
	// A replaced account keeps its link to the person it signs in as.
	if err := u.Accounts.Put(ctx, cliActor, store.LocalAccount{Username: username, Hash: hash, Groups: groups, PersonID: existing.PersonID}); err != nil {
		return fmt.Errorf("write local account: %w", err)
	}
	return nil
}

// Remove deletes username's account. It returns ErrUserNotFound if there is
// no such user.
func (u *DBUsers) Remove(ctx context.Context, username string) error {
	err := u.Accounts.Delete(ctx, cliActor, username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrUserNotFound
	case err != nil:
		return fmt.Errorf("delete local account: %w", err)
	}
	return nil
}
