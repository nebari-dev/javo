package usercmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// userEnv is what the commands talk to: the database (accounts, sessions and
// API tokens).
type userEnv struct {
	db *store.Store
}

func newEnv(t *testing.T) *userEnv {
	t.Helper()
	return &userEnv{db: storetest.New(t)}
}

func (e *userEnv) users() *local.DBUsers { return &local.DBUsers{Accounts: e.db.LocalAccounts()} }

func (e *userEnv) sessions() *session.DBStore { return &session.DBStore{Sessions: e.db.Sessions()} }

func runUser(t *testing.T, e *userEnv, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := clitest.Root(New(nil, clitest.Opener(t, e.db)))
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(append([]string{"user"}, args...))
	err = root.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func runUserAdd(t *testing.T, e *userEnv, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runUser(t, e, append([]string{"add"}, args...)...)
}

// sessionsOf creates n sessions for user and returns their tokens.
func sessionsOf(t *testing.T, st *session.DBStore, user string, n int) []string {
	t.Helper()
	var toks []string
	for range n {
		tok, err := st.Create(context.Background(), session.Session{Principal: authz.Principal{ID: local.UserID(user), Username: user}, Provider: "local", Kind: session.KindUI})
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tok)
	}
	return toks
}

func live(st *session.DBStore, tok string) bool {
	_, err := st.Get(context.Background(), tok)
	return err == nil
}

func TestUserAddWritesHashAndPasswordFile(t *testing.T) {
	e := newEnv(t)
	pwFile := filepath.Join(t.TempDir(), "pw")

	stdout, stderr, err := runUserAdd(t, e, "alice", "--password-file", pwFile, "--namespace", "javo-system")
	if err != nil {
		t.Fatal(err)
	}
	pw, err := os.ReadFile(pwFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 32 {
		t.Fatalf("password too short: %d bytes", len(pw))
	}
	if strings.Contains(stdout, string(pw)) || strings.Contains(stderr, string(pw)) {
		t.Fatal("password printed")
	}
	fi, _ := os.Stat(pwFile)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	u := e.users()
	if _, err := u.Verify(context.Background(), "alice", string(pw)); err != nil {
		t.Fatalf("stored hash does not match password file: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(pwFile))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestUserAddRefusesExistingUserWithoutForce(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "pw1")
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", first, "--namespace", "javo-system"); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "pw2")
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", second, "--namespace", "javo-system"); err == nil {
		t.Fatal("expected refusal")
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("password file written despite refusal")
	}
	pw1, _ := os.ReadFile(first)
	u := e.users()
	if _, err := u.Verify(context.Background(), "alice", string(pw1)); err != nil {
		t.Fatal("existing credential was changed")
	}

	if _, _, err := runUserAdd(t, e, "alice", "--password-file", second, "--namespace", "javo-system", "--force"); err != nil {
		t.Fatal(err)
	}
	pw2, _ := os.ReadFile(second)
	if _, err := u.Verify(context.Background(), "alice", string(pw2)); err != nil {
		t.Fatalf("force did not replace: %v", err)
	}
}

func TestUserAddForceEndsTheUsersSessions(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(dir, "pw1"), "--namespace", "javo-system"); err != nil {
		t.Fatal(err)
	}
	st := e.sessions()
	alice := sessionsOf(t, st, "alice", 2)
	bob := sessionsOf(t, st, "bob", 1)
	_, stderr, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(dir, "pw2"), "--namespace", "javo-system", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "signed out 2 sessions") {
		t.Fatalf("stderr %q", stderr)
	}
	if live(st, alice[0]) || live(st, alice[1]) {
		t.Fatal("alice's old sessions survived a password replacement")
	}
	if !live(st, bob[0]) {
		t.Fatal("bob's session ended")
	}
}

func TestUserRemoveDeletesUserAndSessions(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	for _, name := range []string{"alice", "bob"} {
		if _, _, err := runUserAdd(t, e, name, "--password-file", filepath.Join(dir, name), "--namespace", "javo-system"); err != nil {
			t.Fatal(err)
		}
	}
	st := e.sessions()
	alice := sessionsOf(t, st, "alice", 1)
	bob := sessionsOf(t, st, "bob", 1)

	if _, _, err := runUser(t, e, "remove", "alice", "--namespace", "javo-system"); err != nil {
		t.Fatal(err)
	}
	pw, _ := os.ReadFile(filepath.Join(dir, "alice"))
	u := e.users()
	if _, err := u.Verify(context.Background(), "alice", string(pw)); err == nil {
		t.Fatal("removed user can still sign in")
	}
	if live(st, alice[0]) {
		t.Fatal("removed user's session survived")
	}
	if !live(st, bob[0]) {
		t.Fatal("bob's session ended")
	}
	if _, _, err := runUser(t, e, "remove", "alice", "--namespace", "javo-system"); err == nil {
		t.Fatal("removing an unknown user succeeded")
	}
}

func TestUserAddRefusesExistingPasswordFileWithoutForce(t *testing.T) {
	e := newEnv(t)
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", pwFile, "--namespace", "javo-system"); err == nil {
		t.Fatal("expected refusal")
	}
	if b, _ := os.ReadFile(pwFile); string(b) != "keep" {
		t.Fatal("file clobbered")
	}
}

func TestUserAddRequiresAPasswordFile(t *testing.T) {
	e := newEnv(t)
	if _, _, err := runUserAdd(t, e, "alice", "--namespace", "javo-system"); err == nil {
		t.Fatal("missing --password-file accepted")
	}
}

func TestUserAddWithoutForceNeverOverwritesAPasswordFileCreatedMeanwhile(t *testing.T) {
	pwFile := filepath.Join(t.TempDir(), "pw")
	// The file appears after the existence check; publishing must not replace it.
	if err := os.WriteFile(pwFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp, err := stagePasswordFile(pwFile, "new")
	if err != nil {
		t.Fatal(err)
	}
	if err := publishPasswordFile(tmp, pwFile, false); err == nil {
		t.Fatal("publish replaced an existing file without force")
	}
	if b, _ := os.ReadFile(pwFile); string(b) != "keep" {
		t.Fatalf("file now %q", b)
	}
	if _, err := os.Stat(tmp); err == nil {
		t.Fatal("staging file left behind")
	}
}

func TestUserAddStoresRepeatedGroupFlags(t *testing.T) {
	e := newEnv(t)
	pwFile := filepath.Join(t.TempDir(), "pw")
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", pwFile, "--namespace", "javo-system",
		"--group", "javo-admins", "--group", "/org/data-science"); err != nil {
		t.Fatal(err)
	}
	pw, _ := os.ReadFile(pwFile)
	u := e.users()
	got, err := u.Verify(context.Background(), "alice", string(pw))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Groups, ",") != "javo-admins,/org/data-science" {
		t.Fatalf("groups = %v", got.Groups)
	}
}

func TestUserRemoveRevokesTheirAPITokens(t *testing.T) {
	e := newEnv(t)
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(t.TempDir(), "alice"), "--namespace", "javo-system"); err != nil {
		t.Fatal(err)
	}
	// A token exists only for someone who signed in, which is what makes a people row.
	person, err := e.db.People(store.IDRange{}).Ensure(context.Background(), store.Claims{Issuer: "local", Subject: "alice", Username: "alice"})
	if err != nil || person.Name != local.UserID("alice") {
		t.Fatalf("person %+v, %v", person, err)
	}
	tokens := &token.DBStore{Tokens: e.db.Tokens()}
	text, _, err := tokens.Create(context.Background(), model.Actor{Person: person.Name}, token.Token{Name: "ci", Owner: person.Name, Scope: token.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runUser(t, e, "remove", "alice", "--namespace", "javo-system"); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.Authenticate(context.Background(), text); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("removed user's token: %v", err)
	}
}

// The command and the server are separate processes sharing one SQLite file:
// what the command writes, a server that opened the file earlier reads.
func TestUserAddThenSignIn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "javo.db")
	server, err := (&dbflag.Flags{Engine: "sqlite", Path: path}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	pwFile := filepath.Join(t.TempDir(), "pw")
	root := clitest.Root(New(nil, nil))
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"user", "add", "alice", "--database-path", path, "--password-file", pwFile, "--group", "javo-admins"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	pw, err := os.ReadFile(pwFile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (&local.DBUsers{Accounts: server.LocalAccounts()}).Verify(ctx, "alice", string(pw))
	if err != nil || got.ID != local.UserID("alice") || len(got.Groups) != 1 {
		t.Fatalf("the server cannot sign alice in: %+v, %v", got, err)
	}
}

// "-" hands the password to whatever reads stdout, for running the command
// in a pod that has no shell (kubectl exec ... > file): stdout holds the
// password and nothing else.
func TestUserAddWritesThePasswordToStdoutForDash(t *testing.T) {
	e := newEnv(t)
	stdout, stderr, err := runUserAdd(t, e, "alice", "--password-file", "-")
	if err != nil {
		t.Fatal(err)
	}
	if len(stdout) < 32 || strings.ContainsAny(stdout, " \n") {
		t.Fatalf("stdout is not the bare password: %q", stdout)
	}
	if strings.Contains(stderr, stdout) {
		t.Fatal("password on stderr")
	}
	if _, err := e.users().Verify(context.Background(), "alice", stdout); err != nil {
		t.Fatalf("stored hash does not match the password on stdout: %v", err)
	}
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", "-"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second add without --force = %v, want already exists", err)
	}
}
