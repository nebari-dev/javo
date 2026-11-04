package usercmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

const idp = "https://idp.example.com/realms/javo"

// ssoPerson is someone who signed in through the identity provider: a people
// row and nothing in local_accounts.
func ssoPerson(t *testing.T, e *userEnv, subject, username, email string) model.Person {
	t.Helper()
	p, err := e.db.People(store.IDRange{}).Ensure(context.Background(),
		store.Claims{Issuer: idp, Subject: subject, Username: username, Email: email})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// withSecretKeys makes the database's secrets keyset, so secrets can be stored.
func withSecretKeys(t *testing.T, e *userEnv) {
	t.Helper()
	kek, err := secrets.KEKFromFile(clitest.KEKFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&secrets.Keys{Store: e.db, KEK: kek}).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// signedIn gives the person a browser session and an API token, and returns them.
func signedIn(t *testing.T, e *userEnv, p model.Person) (sess, tok string) {
	t.Helper()
	ctx := context.Background()
	sess, err := e.sessions().Create(ctx, session.Session{
		Principal: authz.Principal{ID: p.Name, Username: p.Username, Email: p.Email, Issuer: idp}, Provider: "oidc", Kind: session.KindUI,
	})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err = (&token.DBStore{Tokens: e.db.Tokens()}).Create(ctx, model.Actor{Person: p.Name},
		token.Token{Name: "ci", Owner: p.Name, Scope: token.ScopeRead})
	if err != nil {
		t.Fatal(err)
	}
	return sess, tok
}

// Someone who signs in through the identity provider has no local account,
// yet the operator can remove them by their email, their username or their
// identity name: their sessions end, their API tokens and secrets go, and
// nobody else is touched.
func TestUserRemoveRemovesAnSSOPersonByEmailUsernameOrIdentityName(t *testing.T) {
	ctx := context.Background()
	for _, by := range []string{"email", "username", "identity name"} {
		e := newEnv(t)
		withSecretKeys(t, e)
		alice := ssoPerson(t, e, "s-1", "alice", "Alice@Example.com")
		bob := ssoPerson(t, e, "s-2", "bob", "bob@example.com")
		aliceSess, aliceTok := signedIn(t, e, alice)
		bobSess, bobTok := signedIn(t, e, bob)
		clitest.StoreSecret(t, e.db, alice.Name, "db-password", "x")
		clitest.StoreSecret(t, e.db, bob.Name, "db-password", "y")

		who := map[string]string{"email": "alice@example.com", "username": "alice", "identity name": alice.Name}[by]
		out, _, err := runUser(t, e, "remove", who)
		if err != nil {
			t.Fatalf("by %s: %v", by, err)
		}
		if !strings.Contains(out, alice.Name) {
			t.Errorf("by %s: output does not name who was removed: %q", by, out)
		}
		if live(e.sessions(), aliceSess) || !live(e.sessions(), bobSess) {
			t.Errorf("by %s: alice's session live %v, bob's %v", by, live(e.sessions(), aliceSess), live(e.sessions(), bobSess))
		}
		tokens := &token.DBStore{Tokens: e.db.Tokens()}
		if _, err := tokens.Authenticate(ctx, aliceTok); !errors.Is(err, token.ErrInvalid) {
			t.Errorf("by %s: alice's token: %v", by, err)
		}
		if _, err := tokens.Authenticate(ctx, bobTok); err != nil {
			t.Errorf("by %s: bob's token: %v", by, err)
		}
		if left, _ := e.db.Secrets().List(ctx, alice.Name); len(left) != 0 {
			t.Errorf("by %s: alice's secrets left: %d", by, len(left))
		}
		if left, _ := e.db.Secrets().List(ctx, bob.Name); len(left) != 1 {
			t.Errorf("by %s: bob's secrets: %d", by, len(left))
		}
	}
}

// A username or email that more than one person signs in with removes no one:
// the command lists each person's identity name, and one of those removes
// exactly that person.
func TestUserRemoveRefusesAnAmbiguousNameAndListsWhoMatches(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(t.TempDir(), "alice")); err != nil {
		t.Fatal(err)
	}
	sso := ssoPerson(t, e, "s-1", "alice", "alice@example.com")
	ssoSess, _ := signedIn(t, e, sso)
	localSess := sessionsOf(t, e.sessions(), "alice", 1)[0]

	_, _, err := runUser(t, e, "remove", "alice")
	if err == nil {
		t.Fatal("an ambiguous name removed someone")
	}
	for _, want := range []string{sso.Name, local.UserID("alice")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not list %s: %v", want, err)
		}
	}
	if !live(e.sessions(), ssoSess) || !live(e.sessions(), localSess) {
		t.Fatal("a refused remove ended sessions")
	}
	if _, err := e.users().Lookup(ctx, "alice"); err != nil {
		t.Fatalf("a refused remove deleted the local account: %v", err)
	}

	if _, _, err := runUser(t, e, "remove", sso.Name); err != nil {
		t.Fatal(err)
	}
	if live(e.sessions(), ssoSess) || !live(e.sessions(), localSess) {
		t.Fatal("removing by identity name hit the wrong person")
	}
	if _, err := e.users().Lookup(ctx, "alice"); err != nil {
		t.Fatalf("removing the SSO alice deleted the local account: %v", err)
	}
}

// A person with apps or jobs is removed only when the operator says their
// apps and jobs go too; then each is marked deleted for the controller.
func TestUserRemoveDeletesAppsAndJobsOnlyWhenAsked(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p := ssoPerson(t, e, "s-1", "alice", "alice@example.com")
	sess, _ := signedIn(t, e, p)
	if _, err := e.db.Apps().Create(ctx, model.Actor{Person: p.Name}, &model.App{
		Namespace: "javo-apps", Name: "notebook", Owner: p.Name, Template: "jupyterlab", DisplayName: "notebook", Size: "small",
	}); err != nil {
		t.Fatal(err)
	}
	job := &model.Job{Namespace: "javo-apps", Name: "nightly", Owner: p.Name, Spec: model.JobSpec{
		Template: "papermill", Engine: "papermill", Kernel: "python3", Source: model.JobSource{Path: "nightly.ipynb"},
	}}
	job.ApplyDefaults()
	if _, err := e.db.Jobs().Create(ctx, model.Actor{Person: p.Name}, job); err != nil {
		t.Fatal(err)
	}

	_, _, err := runUser(t, e, "remove", "alice@example.com")
	if err == nil || !strings.Contains(err.Error(), "notebook") || !strings.Contains(err.Error(), "nightly") ||
		!strings.Contains(err.Error(), "--delete-apps-and-jobs") {
		t.Fatalf("remove with work left = %v", err)
	}
	if !live(e.sessions(), sess) {
		t.Fatal("a refused remove ended the session")
	}

	if _, _, err := runUser(t, e, "remove", "alice@example.com", "--delete-apps-and-jobs"); err != nil {
		t.Fatal(err)
	}
	if apps, _ := e.db.Apps().List(ctx, p.Name, true); len(apps) != 0 {
		t.Errorf("apps left: %+v", apps)
	}
	if jobs, _ := e.db.Jobs().List(ctx, p.Name); len(jobs) != 0 {
		t.Errorf("jobs left: %+v", jobs)
	}
	if live(e.sessions(), sess) {
		t.Error("session survived")
	}
}

// runWith runs `javo args...` on e's database and the cluster c.
func runWith(t *testing.T, e *userEnv, c client.Client, args ...string) (string, error) {
	t.Helper()
	return clitest.Run(New(func() (client.Client, error) { return c, nil }, clitest.Opener(t, e.db)), args...)
}

// Removing a person removes their home rows, so they are not backed up or
// listed any more. Their files stay unless --delete-home says otherwise:
// someone added again under the same name gets them back.
func TestUserRemoveRemovesTheirHomeAndOnlyAskedDeletesTheirFiles(t *testing.T) {
	ctx := context.Background()
	for _, deleteFiles := range []bool{false, true} {
		e := newEnv(t)
		c := clitest.Cluster(t)
		if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(t.TempDir(), "alice")); err != nil {
			t.Fatal(err)
		}
		owner := local.UserID("alice")
		if _, err := e.db.Homes().Ensure(ctx, "javo-apps", owner, nil); err != nil {
			t.Fatal(err)
		}
		pvc := clitest.HomeClaim(t, c, "javo-apps", owner)
		// A finished backup Job of the home still holds the claim until it goes.
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "javo-backup-x", Namespace: "javo-apps",
			Labels: map[string]string{model.LabelOwner: owner, model.LabelRole: model.RoleHomeBackup}}}
		if err := c.Create(ctx, job); err != nil {
			t.Fatal(err)
		}
		args := []string{"user", "remove", "alice"}
		if deleteFiles {
			args = append(args, "--delete-home")
		}
		if out, err := runWith(t, e, c, args...); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if left, _ := e.db.Homes().ListByOwner(ctx, owner); len(left) != 0 {
			t.Fatalf("home rows left: %+v", left)
		}
		err := c.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{})
		if deleteFiles != apierrors.IsNotFound(err) {
			t.Fatalf("--delete-home %v: claim get = %v", deleteFiles, err)
		}
		err = c.Get(ctx, client.ObjectKeyFromObject(job), &batchv1.Job{})
		if deleteFiles != apierrors.IsNotFound(err) {
			t.Fatalf("--delete-home %v: the home's backup Job get = %v", deleteFiles, err)
		}
	}
}

// A forgotten home stays forgotten: the controller's volume finder, which
// gives a row to every home volume it finds without one, must not bring it
// back (it would be listed and backed up again).
func TestUserRemoveForgottenHomeIsNotFoundAgain(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	c := clitest.Cluster(t)
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(t.TempDir(), "alice")); err != nil {
		t.Fatal(err)
	}
	owner := local.UserID("alice")
	if _, err := e.db.Homes().Ensure(ctx, "javo-apps", owner, nil); err != nil {
		t.Fatal(err)
	}
	clitest.HomeClaim(t, c, "javo-apps", owner)
	if out, err := runWith(t, e, c, "user", "remove", "alice"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := (storage.VolumeFinder{Reader: c, Store: e.db, Namespaces: []string{"javo-apps"}}).AdoptHomes(ctx); err != nil {
		t.Fatal(err)
	}
	if left, _ := e.db.Homes().ListByOwner(ctx, owner); len(left) != 0 {
		t.Fatalf("the forgotten home came back: %+v", left)
	}
}

// --delete-home refuses, before removing anything, while the person still
// has apps: the controller would make their home again for them right away,
// a home with no account that nobody removes.
func TestUserRemoveDeleteHomeRefusesWhileTheyHaveApps(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	c := clitest.Cluster(t)
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(t.TempDir(), "alice")); err != nil {
		t.Fatal(err)
	}
	owner := local.UserID("alice")
	if _, err := e.db.Homes().Ensure(ctx, "javo-apps", owner, nil); err != nil {
		t.Fatal(err)
	}
	pvc := clitest.HomeClaim(t, c, "javo-apps", owner)
	if _, err := e.db.Apps().Create(ctx, model.Actor{Person: owner}, &model.App{
		Namespace: "javo-apps", Name: "notebook", Owner: owner, Template: "jupyterlab", DisplayName: "notebook", Size: "small",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := runWith(t, e, c, "user", "remove", "alice", "--delete-home")
	if err == nil || !strings.Contains(err.Error(), "notebook") {
		t.Fatalf("remove with an app left = %v\n%s", err, out)
	}
	if _, err := (&local.DBUsers{Accounts: e.db.LocalAccounts()}).Lookup(ctx, "alice"); err != nil {
		t.Fatalf("the account went: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pvc), &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("the claim went: %v", err)
	}
}

func TestUserRemoveDeletesTheirSecrets(t *testing.T) {
	e := newEnv(t)
	if _, _, err := runUserAdd(t, e, "alice", "--password-file", filepath.Join(t.TempDir(), "alice")); err != nil {
		t.Fatal(err)
	}
	kek, err := secrets.KEKFromFile(clitest.KEKFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := (&secrets.Keys{Store: e.db, KEK: kek}).Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	clitest.StoreSecret(t, e.db, local.UserID("alice"), "API_KEY", "v")
	if _, _, err := runUser(t, e, "remove", "alice"); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.db.Secrets().List(context.Background(), local.UserID("alice")); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}
}
