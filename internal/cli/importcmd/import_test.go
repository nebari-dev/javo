package importcmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/cli/kubeclient"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const testIssuer = "https://idp.example.com/realms/nebari"

// importConfig is a platform config with OIDC sign-in and allocated uids.
func importConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := "auth:\n  oidc:\n    issuer: " + testIssuer + "\n    clientID: javo\nidentity:\n  uidMode: allocated\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// hubClaim is a JupyterHub home claim as KubeSpawner makes it.
func hubClaim(t *testing.T, c client.Client, user string) {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-" + user, Namespace: "jupyterhub",
			Annotations: map[string]string{"hub.jupyter.org/username": user}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}},
		},
	}
	if err := c.Create(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
}

func runImport(t *testing.T, db *store.Store, c client.Client, args ...string) string {
	t.Helper()
	out, err := clitest.Run(New(clusterOf(c), clitest.Opener(t, db)), append([]string{"import", "jupyterhub-homes", "--from-namespace", "jupyterhub",
		"--config", importConfig(t), "--image", "registry.example.org/javo:1", "--no-wait"}, args...)...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return out
}

// clusterOf hands the command the cluster c.
func clusterOf(c client.Client) kubeclient.Factory {
	return func() (client.Client, error) { return c, nil }
}

func line(out, user string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, user+" (claim-") {
			return l
		}
	}
	return ""
}

// A dry run matches each JupyterHub user to a person by username or email,
// says what it would do, and changes nothing.
func TestImportDryRunMatchesPeopleAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	db, c := storetest.New(t), clitest.Cluster(t)
	hubClaim(t, c, "alice")
	hubClaim(t, c, "bob@example.org")
	alice, err := db.People(store.IDRange{First: 100000, Last: 199999}).Ensure(ctx, store.Claims{Issuer: testIssuer, Subject: "s-a", Username: "alice", Email: "alice@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	out := runImport(t, db, c, "--dry-run")
	if l := line(out, "alice"); !strings.Contains(l, alice.Name) || !strings.Contains(l, "would make a home") {
		t.Errorf("alice: %q\n%s", l, out)
	}
	if l := line(out, "bob@example.org"); !strings.Contains(l, "skipped: no Javo person") {
		t.Errorf("bob: %q\n%s", l, out)
	}
	if homes, _ := db.Homes().List(ctx); len(homes) != 0 {
		t.Errorf("a dry run made homes: %+v", homes)
	}
	var jobs batchv1.JobList
	var claims corev1.PersistentVolumeClaimList
	_ = c.List(ctx, &jobs)
	_ = c.List(ctx, &claims)
	if len(jobs.Items) != 0 || len(claims.Items) != 2 {
		t.Errorf("a dry run made %d Jobs and %d claims", len(jobs.Items), len(claims.Items))
	}
}

// With --create-people, a user without a Javo account becomes a person with
// the subject the file gives, gets a home at least as large as their
// JupyterHub one, and once the home is ready the import stages it.
func TestImportCreatesPeopleAndStartsOnceTheHomeIsReady(t *testing.T) {
	ctx := context.Background()
	db, c := storetest.New(t), clitest.Cluster(t)
	hubClaim(t, c, "bob")
	subjects := filepath.Join(t.TempDir(), "subjects.csv")
	if err := os.WriteFile(subjects, []byte("\"bob\",\"0b5e2c4a-1111-2222-3333-444455556666\",\"bob@example.org\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runImport(t, db, c, "--create-people", "--subjects", subjects)
	bob, err := db.People(store.IDRange{}).BySubject(ctx, testIssuer, "0b5e2c4a-1111-2222-3333-444455556666")
	if err != nil || bob.Username != "bob" || bob.Email != "bob@example.org" || bob.UID == 0 {
		t.Fatalf("person %+v, %v\n%s", bob, err, out)
	}
	home, err := db.Homes().ByOwner(ctx, model.DefaultWorkspaceNamespace, bob.Name)
	if err != nil || home.Spec.Size == nil || home.Spec.Size.Cmp(resource.MustParse("20Gi")) < 0 {
		t.Fatalf("home %+v, %v", home, err)
	}
	if l := line(out, "bob"); !strings.Contains(l, "in progress") {
		t.Errorf("bob before the home is ready: %q\n%s", l, out)
	}

	// The controller makes and readies the home.
	home.Status.ClaimName, home.Status.Prepared = "home-"+bob.Name, true
	if _, err := db.Homes().Update(ctx, home, home.Version); err != nil {
		t.Fatal(err)
	}
	clitest.HomeClaim(t, c, model.DefaultWorkspaceNamespace, bob.Name)
	out = runImport(t, db, c, "--create-people", "--subjects", subjects)
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs, client.InNamespace("jupyterhub")); err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs in jupyterhub: %d, %v\n%s", len(jobs.Items), err, out)
	}
	if !strings.Contains(out, "bob: staging") {
		t.Errorf("no staging line:\n%s", out)
	}
}

// A home already imported from this JupyterHub claim is reported as done.
func TestImportSkipsAHomeAlreadyImported(t *testing.T) {
	ctx := context.Background()
	db, c := storetest.New(t), clitest.Cluster(t)
	hubClaim(t, c, "alice")
	alice, err := db.People(store.IDRange{First: 100000, Last: 199999}).Ensure(ctx, store.Claims{Issuer: testIssuer, Subject: "s-a", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	home, err := db.Homes().Ensure(ctx, model.DefaultWorkspaceNamespace, alice.Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	home.Status.ClaimName, home.Status.Prepared = "home-"+alice.Name, true
	if _, err := db.Homes().Update(ctx, home, home.Version); err != nil {
		t.Fatal(err)
	}
	pvc := clitest.HomeClaim(t, c, model.DefaultWorkspaceNamespace, alice.Name)
	pvc.Annotations = map[string]string{model.AnnotationImportedFrom: "jupyterhub/claim-alice"}
	if err := c.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	if l := line(runImport(t, db, c, "--dry-run"), "alice"); !strings.Contains(l, "already imported") {
		t.Errorf("dry run: %q", l)
	}
	if l := line(runImport(t, db, c), "alice"); !strings.Contains(l, "imported into") {
		t.Errorf("run: %q", l)
	}
}

// --subjects - reads the file from standard input: the image has no shell
// to copy a file into, but kubectl exec -i passes its input on.
func TestImportReadsSubjectsFromStandardInput(t *testing.T) {
	ctx := context.Background()
	db, c := storetest.New(t), clitest.Cluster(t)
	hubClaim(t, c, "bob")
	root := clitest.Root(New(clusterOf(c), clitest.Opener(t, db)))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("bob,sub-bob,bob@example.org\n"))
	root.SetArgs([]string{"import", "jupyterhub-homes", "--from-namespace", "jupyterhub", "--config", importConfig(t),
		"--image", "registry.example.org/javo:1", "--no-wait", "--create-people", "--subjects", "-"})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if p, err := db.People(store.IDRange{}).BySubject(ctx, testIssuer, "sub-bob"); err != nil || p.Username != "bob" {
		t.Fatalf("person %+v, %v\n%s", p, err, out.String())
	}
}
