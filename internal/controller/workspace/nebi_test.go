package workspace

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
)

const testNebiURL = "http://nebi.nebi.svc:8460"

func volumeOf(pod *corev1.Pod, name string) *corev1.Volume {
	for i, v := range pod.Spec.Volumes {
		if v.Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

// An app whose template asks for its owner's Nebi token gets it as a file
// from a Secret of its own (never in the pod spec), Nebi's address, and the
// nebi CLI copied from the platform's image. The Secret may not exist yet:
// the gateway writes it when the owner opens the app, so the pod does not
// wait for it.
func TestAnAppWithNebiAccessGetsTheTokenFileAndTheCLI(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-nebi1", func(s *model.TemplateSpec) { s.NebiToken = true })
	app := fx.newApp(t, "ws-nebi1", "alice", "lab-nebi1")
	var pod *corev1.Pod
	eventually(t, "a pod", func() bool { pod = fx.podOf(app); return pod != nil })
	v := volumeOf(pod, nebiVolume)
	if v == nil || v.Secret == nil || v.Secret.SecretName != NebiTokenSecretName(app.ObjectName()) || v.Secret.Optional == nil || !*v.Secret.Optional {
		t.Fatalf("token volume: %+v", v)
	}
	// The kubelet writes the file as root, and app pods have no fsGroup:
	// the app's own uid reads it only when others may.
	if m := v.Secret.DefaultMode; m != nil && *m&0o004 == 0 {
		t.Fatalf("token file mode %o: the app's user cannot read it", *m)
	}
	c := pod.Spec.Containers[0]
	env := map[string]string{}
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == NebiTokenSecretName(app.ObjectName()) {
			t.Fatal("the Nebi token is in the environment")
		}
		env[e.Name] = e.Value
	}
	if env["NEBI_REMOTE_URL"] != testNebiURL || env["JAVO_NEBI_TOKEN_FILE"] != NebiTokenDir+"/"+NebiTokenKey {
		t.Fatalf("env: %v", env)
	}
	mounted := false
	for _, m := range c.VolumeMounts {
		mounted = mounted || (m.Name == nebiVolume && m.MountPath == NebiTokenDir && m.ReadOnly && m.SubPath == "")
	}
	if !mounted {
		t.Fatalf("mounts: %+v", c.VolumeMounts)
	}
	cli := false
	for _, ic := range pod.Spec.InitContainers {
		cli = cli || (ic.Name == NebiCLIContainer && ic.Image == testImages.Runner.Ref)
	}
	if !cli {
		t.Fatalf("no step copies the nebi CLI: %+v", pod.Spec.InitContainers)
	}
}

// Without the template asking, and for a preview, nothing about Nebi.
func TestNoNebiTokenUnlessTheTemplateAsksAndNeverForAPreview(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-nebi2")
	fx.newTemplate(t, "lab-nebi3", func(s *model.TemplateSpec) { s.NebiToken = true })
	plain := fx.newApp(t, "ws-nebi2", "alice", "lab-nebi2")
	parent := fx.newApp(t, "ws-nebi3", "alice", "lab-nebi3")
	child, err := fx.db.Previews().Put(bg, &model.App{Namespace: fx.ns, Name: "ws-nebi3-pr-1", Owner: "alice", Template: "lab-nebi3", ParentID: parent.ID, PullNumber: 1,
		Source: &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []model.App{plain, child} {
		var pod *corev1.Pod
		eventually(t, "a pod for "+a.Name, func() bool { pod = fx.podOf(a); return pod != nil })
		if volumeOf(pod, nebiVolume) != nil {
			t.Fatalf("%s got the Nebi token", a.Name)
		}
	}
}

func (fx *env) putNebiToken(t *testing.T, app model.App, token string, expires time.Time) {
	t.Helper()
	ks, _, err := fx.db.SecretKeys().Get(bg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := secrets.NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal([]byte(token), secrets.NebiTokenAD(app.ID, app.Owner))
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.db.NebiTokens().Put(bg, app.ID, sealed, expires); err != nil {
		t.Fatal(err)
	}
}

// The token the gateway stored (sealed) is opened and written to the app's
// Secret, and a renewal replaces it there without restarting the app.
func TestTheStoredNebiTokenReachesTheAppsSecretAndIsRenewedInPlace(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-nebi4", func(s *model.TemplateSpec) { s.NebiToken = true })
	app := fx.newApp(t, "ws-nebi4", "alice", "lab-nebi4")
	var pod *corev1.Pod
	eventually(t, "a pod", func() bool { pod = fx.podOf(app); return pod != nil })
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Millisecond)
	fx.putNebiToken(t, app, "nebi-jwt-1", exp)
	sec := &corev1.Secret{}
	eventually(t, "the token in the app's Secret", func() bool {
		return fx.exists(sec, NebiTokenSecretName(app.ObjectName())) && string(sec.Data[NebiTokenKey]) == "nebi-jwt-1"
	})
	if sec.Labels[model.LabelApp] != app.ID {
		t.Fatalf("labels %v", sec.Labels)
	}
	fx.putNebiToken(t, app, "nebi-jwt-2", exp.Add(time.Hour))
	eventually(t, "the renewed token", func() bool {
		return fx.exists(sec, NebiTokenSecretName(app.ObjectName())) && string(sec.Data[NebiTokenKey]) == "nebi-jwt-2"
	})
	if now := fx.podOf(app); now == nil || now.UID != pod.UID {
		t.Fatal("a renewal restarted the app")
	}
}
