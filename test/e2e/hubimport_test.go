//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// hubNS stands in for the data science pack's JupyterHub namespace; the dev
// values grant the import access to it (jupyterhubImport.namespace).
const hubNS = "javo-e2e-jupyterhub"

// The files a JupyterHub user leaves in their home (uid 1000, as the data
// science pack's image runs): a notebook, a private file, a larger data
// file and a link. The script prints a checksum and the owner of each.
const (
	seedHub = `set -e; cd /home/jovyan
mkdir -p notebooks .private data
printf '{"cells": []}\n' > notebooks/analysis.ipynb
printf 'not for others\n' > .private/token; chmod 600 .private/token; chmod 700 .private
head -c 1048576 /dev/urandom > data/sample.bin
ln -s notebooks/analysis.ipynb latest.ipynb`
	listFiles = `set -e; cd "$1"
find . -type f | sort | xargs sha256sum
find . -mindepth 1 | sort | while read -r f; do stat -c 'owner %u:%g %a %n' "$f"; done`
)

// requireHubImportAccess skips on an install that does not grant the import
// (chart value jupyterhubImport.namespace unset): the controller may not
// read the claims in the JupyterHub namespace there.
func requireHubImportAccess(t *testing.T, c client.Client) {
	t.Helper()
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User: "system:serviceaccount:" + gatewayNS + ":javo-controller",
		ResourceAttributes: &authorizationv1.ResourceAttributes{
			Namespace: hubNS, Verb: "list", Resource: "persistentvolumeclaims",
		},
	}}
	if err := c.Create(context.Background(), sar); err != nil {
		t.Fatalf("ask whether the controller may list claims in %s: %v", hubNS, err)
	}
	if !sar.Status.Allowed {
		t.Skipf("this install does not grant the JupyterHub home import in %s: set jupyterhubImport.namespace", hubNS)
	}
}

// The JupyterHub home of a person moves into a folder of their Javo home:
// every file arrives with the same content, owned by the person's uid, and
// the JupyterHub claim is left as it was. A second run finds it done.
func TestJupyterHubHomeImport(t *testing.T) {
	t.Parallel()
	c := kube(t)
	ctx := context.Background()
	requireHubImportAccess(t, c)
	s := newUser(t)
	user := s.me().Username

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: hubNS}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim-" + kubespawnerEscape(user), Namespace: hubNS,
			Annotations: map[string]string{"hub.jupyter.org/username": user}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	if err := c.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, claim) })
	podRun(t, c, hubNS, claim.Name, "/home/jovyan", 1000, "sh", "-c", seedHub)
	before := podRun(t, c, hubNS, claim.Name, "/home/jovyan", 1000, "sh", "-c", listFiles, "list", "/home/jovyan")

	out := javoCLI(t, "import", "jupyterhub-homes", "--from-namespace", hubNS, "--user", user)
	t.Logf("import:\n%s", out)
	done := regexp.MustCompile(`imported into ` + appsNS + `/(\S+), folder jupyterhub`).FindStringSubmatch(out)
	uid := regexp.MustCompile(`every entry goes to uid (\d+), gid 100`).FindStringSubmatch(out)
	if done == nil || uid == nil {
		t.Fatalf("no import, or no uid in the copy's log:\n%s", out)
	}
	var personUID int64
	fmt.Sscan(uid[1], &personUID)
	after := podRun(t, c, appsNS, done[1], "/home/user", personUID, "sh", "-c", listFiles, "list", "/home/user/jupyterhub")
	t.Logf("JupyterHub home:\n%s\nJavo home, folder jupyterhub:\n%s", before, after)

	sums := func(listing string) []string {
		var out []string
		for _, l := range strings.Split(listing, "\n") {
			if l != "" && !strings.HasPrefix(l, "owner ") {
				out = append(out, l)
			}
		}
		return out
	}
	if b, a := sums(before), sums(after); strings.Join(b, "\n") != strings.Join(a, "\n") || len(b) != 3 {
		t.Errorf("checksums differ:\nJupyterHub:\n%s\nJavo:\n%s", strings.Join(b, "\n"), strings.Join(a, "\n"))
	}
	for _, l := range strings.Split(after, "\n") {
		if strings.HasPrefix(l, "owner ") && !strings.HasPrefix(l, fmt.Sprintf("owner %d:100 ", personUID)) {
			t.Errorf("not the person's: %s", l)
		}
	}
	if !strings.Contains(after, " 600 ./.private/token") {
		t.Errorf("the private file lost its mode:\n%s", after)
	}
	if again := podRun(t, c, hubNS, claim.Name, "/home/jovyan", 1000, "sh", "-c", listFiles, "list", "/home/jovyan"); again != before {
		t.Errorf("the JupyterHub home changed:\nbefore:\n%s\nafter:\n%s", before, again)
	}
	if out := javoCLI(t, "import", "jupyterhub-homes", "--from-namespace", hubNS, "--user", user); !strings.Contains(out, "imported into") {
		t.Errorf("a second run:\n%s", out)
	}
}

// kubespawnerEscape is KubeSpawner's "escape" slug: every byte but a
// lowercase letter or digit becomes '-' and its hex code.
func kubespawnerEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "-%02x", c)
		}
	}
	return b.String()
}

// podRun runs argv as uid (group 100) in a Job in ns that mounts claim at
// mount, and returns its log; the test fails unless it succeeds.
func podRun(t *testing.T, c client.Client, ns, claim, mount string, uid int64, argv ...string) string {
	t.Helper()
	ctx := context.Background()
	podSec, ctrSec := restrictedSecurity(uid)
	podSec.FSGroup = nil // the files keep the owners under test
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-hub-", Namespace: ns},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			TTLSecondsAfterFinished: ptr.To[int32](60),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				RestartPolicy:                corev1.RestartPolicyNever,
				AutomountServiceAccountToken: ptr.To(false),
				SecurityContext:              podSec,
				Containers: []corev1.Container{{
					Name: "job", Image: helperImage, ImagePullPolicy: corev1.PullIfNotPresent,
					Command: argv, SecurityContext: ctrSec,
					VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: mount}},
				}},
				Volumes: []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
				}}},
			}},
		},
	}
	if err := c.Create(ctx, job); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)) }()
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(2 * time.Second) {
		var j batchv1.Job
		if err := c.Get(ctx, client.ObjectKeyFromObject(job), &j); err != nil {
			t.Fatal(err)
		}
		if j.Status.Succeeded > 0 || j.Status.Failed > 0 {
			var pods corev1.PodList
			if err := c.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{batchv1.JobNameLabel: job.Name}); err != nil || len(pods.Items) == 0 {
				t.Fatalf("pods of %s: %v", job.Name, err)
			}
			log := podLog(t, &pods.Items[0])
			if j.Status.Failed > 0 {
				t.Fatalf("%v failed:\n%s", argv, log)
			}
			return log
		}
		if time.Now().After(deadline) {
			t.Fatalf("%v did not finish: %+v", argv, j.Status)
		}
	}
}
