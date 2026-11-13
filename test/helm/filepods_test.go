//go:build !local

package helm

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

// The gateway's file pods are made with the Kubernetes client, which a
// binary built with the tag local leaves out (files/pods.go); so is this
// test of what the chart lets the gateway make.

// noStorage gives a file pod just the owner's home claim, prepared.
type noStorage struct{}

func (s noStorage) Input(ctx context.Context, ns, owner string) (podstorage.Input, error) {
	h, err := s.Home(ctx, ns, owner)
	return podstorage.Input{Home: &h}, err
}

func (noStorage) Home(_ context.Context, _, owner string) (model.Home, error) {
	return model.Home{Spec: model.HomeSpec{Owner: owner}, Status: model.HomeStatus{ClaimName: naming.HomeClaimName(owner), Prepared: true}}, nil
}

// The gateway may create pods in workspace namespaces only for the file
// picker: a pod creator there could otherwise mount any Secret or claim, or
// run any image. Its own file pods (made by the real code) still start;
// anything else from its account is refused.
// fixedUID gives every owner the same uid, as a single-user install would,
// and the people's group, as the gateway's ids.People does.
type fixedUID struct{}

func (fixedUID) UID(context.Context, string) (int64, int64, error) {
	return 100000, podspec.FixedGID, nil
}

func TestGatewayCreatesOnlyFilePods(t *testing.T) {
	t.Parallel()
	admin, as, out := admissionCluster(t, "postgres")
	gw := as("javo-gateway")
	ctx := context.Background()
	image := controllerArg(deploymentOf(t, out, "postgres", "gateway"), "files-image")
	nsName, owner := own("javo-ivan"), "alice"

	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: map[string]string{"javo.nebari.dev/managed": "true"}}}); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create(ctx, appRoleBinding(nsName, "javo-javo-system-javo-gateway-apps")); err != nil {
		t.Fatal(err)
	}
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: naming.HomeClaimName(owner), Annotations: map[string]string{model.AnnotationOwnerID: owner}},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	if err := admin.Create(ctx, claim); err != nil {
		t.Fatal(err)
	}
	other := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: name},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "example.org/app:1"}}},
		}
	}
	waitForPolicy(t, func() error {
		return gw.Create(ctx, other("javo-test-probe-"+strconv.FormatInt(time.Now().UnixNano(), 36)))
	})

	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	pods, err := files.NewPods(files.PodsConfig{Client: gw, Namespace: nsName, Image: image, Storage: noStorage{}, UIDs: fixedUID{}, Seal: key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pods.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("the gateway's own file pod: %v, want it created", err)
	}
	var made corev1.Pod
	if err := admin.Get(ctx, client.ObjectKey{Namespace: nsName, Name: model.FilesPodPrefix + owner}, &made); err != nil {
		t.Fatalf("file pod not created: %v", err)
	}
	var secretList corev1.SecretList
	if err := admin.List(ctx, &secretList, client.InNamespace(nsName)); err != nil || len(secretList.Items) != 1 {
		t.Fatalf("the file pod's password Secret: %v, %d Secrets", err, len(secretList.Items))
	}

	// Each case is the real file pod with one thing changed, made for
	// another owner (its name, owner label, home claim and password Secret
	// follow), so only the change can make the policy refuse it.
	n := 0
	variant := func(change func(*corev1.Pod)) *corev1.Pod {
		n++
		o := "v" + strconv.Itoa(n)
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: model.FilesPodPrefix + o, Labels: maps.Clone(made.Labels)}, Spec: *made.Spec.DeepCopy()}
		p.Labels[model.LabelOwner] = o
		p.Spec.NodeName = ""
		for i := range p.Spec.Volumes {
			if v := p.Spec.Volumes[i]; v.Name == "home" {
				v.PersistentVolumeClaim.ClaimName = naming.HomeClaimName(o)
			}
		}
		for i, e := range p.Spec.Containers[0].Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				ref := e.ValueFrom.SecretKeyRef.DeepCopy()
				ref.Name = p.Name + "-rc-0123456789abcdef"
				p.Spec.Containers[0].Env[i].ValueFrom = &corev1.EnvVarSource{SecretKeyRef: ref}
			}
		}
		change(p)
		return p
	}
	ctr := func(p *corev1.Pod) *corev1.Container { return &p.Spec.Containers[0] }
	homeClaim := func(p *corev1.Pod) *corev1.PersistentVolumeClaimVolumeSource {
		for i := range p.Spec.Volumes {
			if p.Spec.Volumes[i].Name == "home" {
				return p.Spec.Volumes[i].PersistentVolumeClaim
			}
		}
		t.Fatal("no home volume")
		return nil
	}
	if err := gw.Create(ctx, variant(func(*corev1.Pod) {})); err != nil {
		t.Fatalf("an unchanged file pod for another owner: %v", err)
	}
	if err := gw.Create(ctx, variant(func(p *corev1.Pod) { homeClaim(p).ClaimName += "-2" })); err != nil {
		t.Fatalf("a home's second generation: %v", err)
	}
	// A person with group folders: the folders' gids, from identity.gidRange.
	if err := gw.Create(ctx, variant(func(p *corev1.Pod) {
		p.Spec.SecurityContext.SupplementalGroups = []int64{200000, 299999}
		p.Spec.SecurityContext.SupplementalGroupsPolicy = ptr.To(corev1.SupplementalGroupsPolicyStrict)
	})); err != nil {
		t.Fatalf("a file pod with group folders: %v", err)
	}
	high := &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "javo-test-high"}, Value: 1000000}
	if err := admin.Create(ctx, high); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(context.Background(), high) })
	runtimeClass := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "javo-test-runtime"}, Handler: "other"}
	if err := admin.Create(ctx, runtimeClass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(context.Background(), runtimeClass) })
	for _, c := range []struct {
		name   string
		change func(*corev1.Pod)
	}{
		{"no-role-label", func(p *corev1.Pod) { delete(p.Labels, "javo.nebari.dev/role") }},
		{"other-owner-label", func(p *corev1.Pod) { p.Labels[model.LabelOwner] = owner }},
		// Another app's Service selects its pods by this label.
		{"app-label", func(p *corev1.Pod) { p.Labels[model.LabelApp] = "a-1" }},
		{"not-named-for-an-owner", func(p *corev1.Pod) { p.Name = "javo-test-x" }},
		{"other-image", func(p *corev1.Pod) { ctr(p).Image = "example.org/tool:1" }},
		{"command", func(p *corev1.Pod) { ctr(p).Command = []string{"sh", "-c", "id"} }},
		{"other-args", func(p *corev1.Pod) { ctr(p).Args = []string{"serve", "http", "/"} }},
		{"rcd-without-auth", func(p *corev1.Pod) { ctr(p).Args = append(ctr(p).Args, "--rc-no-auth") }},
		{"second-container", func(p *corev1.Pod) {
			p.Spec.Containers = append(p.Spec.Containers, *ctr(p).DeepCopy())
			p.Spec.Containers[1].Name = "two"
		}},
		{"init-container", func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "i", Image: image}}
		}},
		{"secret-volume", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "s", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "javo-db"}}})
		}},
		{"writable-claim", func(p *corev1.Pod) {
			for i := range p.Spec.Volumes {
				if pvc := p.Spec.Volumes[i].PersistentVolumeClaim; pvc != nil && p.Spec.Volumes[i].Name != "home" {
					pvc.ReadOnly = false
				}
			}
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "x", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "folder-x"}}})
		}},
		// The home may be writable only in the file pod of the home's own owner.
		{"writable-other-home", func(p *corev1.Pod) { homeClaim(p).ClaimName = naming.HomeClaimName(owner) }},
		// A person named after another's slug: home-<slug>-<8 hex> is theirs,
		// even when the hex happens to be all digits.
		{"writable-home-of-a-longer-name", func(p *corev1.Pod) { homeClaim(p).ClaimName += "-12345678" }},
		{"writable-folder-claim-named-home", func(p *corev1.Pod) {
			p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: "home2", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home-bob"}}})
		}},
		{"env-from-secret", func(p *corev1.Pod) {
			ctr(p).EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "javo-db"}}}}
		}},
		{"env-value-from-secret", func(p *corev1.Pod) {
			ctr(p).Env = append(ctr(p).Env, corev1.EnvVar{Name: "X", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "javo-db"}, Key: "dsn"},
			}})
		}},
		{"password-from-another-secret", func(p *corev1.Pod) {
			for i, e := range ctr(p).Env {
				if e.ValueFrom != nil {
					ctr(p).Env[i].ValueFrom.SecretKeyRef.Name = "javo-db"
				}
			}
		}},
		{"service-account-token", func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = nil }},
		{"root", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr.To[int64](0) }},
		{"uid-outside-the-range", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsUser = ptr.To[int64](5000) }},
		{"container-uid", func(p *corev1.Pod) { ctr(p).SecurityContext.RunAsUser = ptr.To[int64](100001) }},
		{"root-group", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsGroup = ptr.To[int64](0) }},
		{"no-group", func(p *corev1.Pod) { p.Spec.SecurityContext.RunAsGroup = nil }},
		{"container-group", func(p *corev1.Pod) { ctr(p).SecurityContext.RunAsGroup = ptr.To[int64](0) }},
		{"supplemental-group-outside-the-range", func(p *corev1.Pod) { p.Spec.SecurityContext.SupplementalGroups = []int64{200000, 4} }},
		{"supplemental-people-group", func(p *corev1.Pod) { p.Spec.SecurityContext.SupplementalGroups = []int64{100} }},
		{"fs-group", func(p *corev1.Pod) { p.Spec.SecurityContext.FSGroup = ptr.To[int64](200000) }},
		{"priority-class", func(p *corev1.Pod) { p.Spec.PriorityClassName, p.Spec.Priority = "javo-test-high", nil }},
		{"scheduler", func(p *corev1.Pod) { p.Spec.SchedulerName = "other-scheduler" }},
		{"runtime-class", func(p *corev1.Pod) { p.Spec.RuntimeClassName = ptr.To("javo-test-runtime") }},
		{"node-name", func(p *corev1.Pod) { p.Spec.NodeName = "node-1" }},
		{"toleration", func(p *corev1.Pod) {
			p.Spec.Tolerations = append(p.Spec.Tolerations, corev1.Toleration{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists})
		}},
	} {
		if err := gw.Create(ctx, variant(c.change)); !deniedByPolicy(err) {
			t.Errorf("%s: %v, want a policy denial", c.name, err)
		}
	}

	// Secrets: only a file pod's own password.
	podRef := func(name string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: name, UID: made.UID}}
	}
	pw := map[string][]byte{"password": []byte("x")}
	for _, c := range []struct {
		name string
		sec  *corev1.Secret
	}{
		{"app-secrets-name", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alice-1a2b3c4d-lab-javo-secrets", OwnerReferences: podRef(made.Name)}, Data: pw}},
		{"no-owner", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000aa"}, Data: pw}},
		{"owned-by-another-pod", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000bb", OwnerReferences: podRef("javo-files-bob")}, Data: pw}},
		{"other-key", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000cc", OwnerReferences: podRef(made.Name)}, Data: map[string][]byte{"token": []byte("x")}}},
		{"token-type", &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: made.Name + "-rc-00000000000000dd", OwnerReferences: podRef(made.Name),
			Annotations: map[string]string{corev1.ServiceAccountNameKey: "default"}}, Type: corev1.SecretTypeServiceAccountToken}},
	} {
		c.sec.Namespace = nsName
		if err := gw.Create(ctx, c.sec); !deniedByPolicy(err) {
			t.Errorf("secret %s: %v, want a policy denial", c.name, err)
		}
	}
}
