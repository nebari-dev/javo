package workspace

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// The Secrets the agent app of these tests reads its credentials from.
var (
	agentApp          = &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice"}
	testTokenSecret   = APITokenSecretName(agentApp.ObjectName())
	testSecretsSecret = AppSecretsName(agentApp.ObjectName())
)

// credentialPod is the pod of an app from the shipped template name with an
// API token, the agent model of mode and, in own mode, the owner's key
// among the app's secrets, as the reconciler builds it.
func credentialPod(t *testing.T, name, mode string) *corev1.Pod {
	t.Helper()
	tpl := shippedTemplate(t, name)
	s := agentSpec(t, name)
	app := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name}
	addAPIToken(&s, tpl, Inputs{APIAccess: model.APIAccessReadWrite, APIURL: "https://javo.example.com", APIInternalURL: "http://gateway.javo:8080"})
	m := agentModel{mode: mode, model: "vendor/m1", baseURL: "http://gateway.javo:8080/api/ai/openai/v1"}
	var set secretenv.Set
	if mode == model.AgentModeOwn {
		m.baseURL = "https://llm.example.com/v1"
		set = secretenv.Set{Bindings: []store.SealedBinding{{Env: envAgentKey}, {Env: "MY_OWN_SECRET"}}, Fingerprint: "fp"}
	}
	addAppSecrets(&s, set)
	addAgent(&s, app, tpl, m, true)
	addAgentHelper(&s, tpl, testImages.Runner)
	return podOf(s)
}

func envNamed(c *corev1.Container, name string) *corev1.EnvVar {
	for i := range c.Env {
		if c.Env[i].Name == name {
			return &c.Env[i]
		}
	}
	return nil
}

// credentialRef is whether e reads the API token or the owner's agent key.
func credentialRef(e corev1.EnvVar) bool {
	r := e.ValueFrom
	if r == nil || r.SecretKeyRef == nil {
		return false
	}
	return r.SecretKeyRef.Name == testTokenSecret || (r.SecretKeyRef.Name == testSecretsSecret && r.SecretKeyRef.Key == envAgentKey)
}

func appContainers(pod *corev1.Pod) []*corev1.Container {
	var out []*corev1.Container
	for _, cs := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for i := range cs {
			if cs[i].Name != AgentHelperContainer {
				out = append(out, &cs[i])
			}
		}
	}
	return out
}

// A coding agent's container holds no credential: the API token and the
// model key are only in the helper sidecar, which the agent reaches on a
// socket (javo-api) and on a local address (the model).
func TestCodingAgentsHoldNoCredentials(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ template, mode string }{
		{"pi", model.AgentModePlatform}, {"pi", model.AgentModeOwn}, {"pi", model.AgentModeLogin}, {"claude-code", ""}, {"herdr", ""},
	} {
		t.Run(c.template+"-"+c.mode, func(t *testing.T) {
			pod := credentialPod(t, c.template, c.mode)
			for _, ctr := range appContainers(pod) {
				for _, e := range ctr.Env {
					if credentialRef(e) || e.Name == envToken || e.Name == envAgentKey {
						t.Errorf("container %s gets %s (%+v)", ctr.Name, e.Name, e.ValueFrom)
					}
				}
			}
			_, helper := initContainer(pod, AgentHelperContainer)
			if helper == nil {
				t.Fatal("no helper sidecar")
			}
			if helper.RestartPolicy == nil || *helper.RestartPolicy != corev1.ContainerRestartPolicyAlways || helper.Image != testImages.Runner.Ref {
				t.Fatalf("helper %+v", helper)
			}
			sc := helper.SecurityContext
			if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.Capabilities == nil || !slices.Contains(sc.Capabilities.Drop, "ALL") {
				t.Fatalf("helper security %+v", sc)
			}
			if tok := envNamed(helper, envToken); tok == nil || !credentialRef(*tok) {
				t.Fatalf("helper token %+v", tok)
			}
			app := &pod.Spec.Containers[0]
			if e := envNamed(app, envAPISocket); e == nil || e.Value != agentHelperSocket {
				t.Fatalf("app %s = %+v", envAPISocket, e)
			}
			if m := mountAtPath(app, agentHelperDir); m == nil || !m.ReadOnly {
				t.Fatalf("app mount of %s: %+v", agentHelperDir, m)
			}
			key, base := envNamed(helper, envAgentKey), envNamed(helper, envAgentBaseURL)
			switch c.mode {
			case model.AgentModePlatform, model.AgentModeOwn:
				if key == nil || !credentialRef(*key) {
					t.Fatalf("helper key %+v", key)
				}
				if got, _ := plainEnv(pod, envAgentBaseURL); got != agentHelperModelURL {
					t.Fatalf("app model URL %q, want the helper's", got)
				}
				want := "http://gateway.javo:8080/api/ai/openai/v1"
				if c.mode == model.AgentModeOwn {
					want = "https://llm.example.com/v1"
				}
				if base == nil || base.Value != want {
					t.Fatalf("helper model URL %+v, want %s", base, want)
				}
			default:
				if key != nil || base != nil {
					t.Fatalf("helper has a model: %+v %+v", key, base)
				}
			}
			if c.mode == model.AgentModeOwn && envNamed(app, "MY_OWN_SECRET") == nil {
				t.Fatal("the person's own secret left the app")
			}
		})
	}
}

// An agent with no credentials (herdr without API access) gets no helper,
// and other templates keep their token as before.
func TestNoHelperWithoutCredentialsOrOutsideAgents(t *testing.T) {
	t.Parallel()
	herdr := shippedTemplate(t, "herdr")
	s := agentSpec(t, "herdr")
	addAPIToken(&s, herdr, Inputs{APIAccess: model.APIAccessNone, APIURL: "https://javo.example.com"})
	addAgentHelper(&s, herdr, testImages.Runner)
	pod := podOf(s)
	if _, h := initContainer(pod, AgentHelperContainer); h != nil {
		t.Fatal("herdr got a helper")
	}
	tpl := shippedTemplate(t, "jupyterlab")
	s = workload.Spec{Main: workload.Unit{Name: workload.MainUnit}}
	addAPIToken(&s, tpl, Inputs{APIAccess: model.APIAccessReadWrite, APIURL: "https://javo.example.com"})
	addAgentHelper(&s, tpl, testImages.Runner)
	pod = podOf(s)
	if _, h := initContainer(pod, AgentHelperContainer); h != nil || envNamed(&pod.Spec.Containers[0], envToken) == nil {
		t.Fatalf("a JupyterLab app changed: %+v", pod.Spec)
	}
}

// The reconciler builds a coding agent's pod with the helper: the API token
// and the model key are in the sidecar only.
func TestReconcilerMovesAgentCredentialsToTheHelper(t *testing.T) {
	t.Parallel()
	fx := newEnv(t, func(r *Reconciler) {
		r.AgentModel = func(context.Context) (string, error) { return "admin/model", nil }
	})
	fx.newTemplate(t, "pi-helper", func(s *model.TemplateSpec) {
		s.Kind, s.Image, s.Port, s.Category = "pi", "example.com/agents:1", 7681, model.CategoryAgents
		s.Command, s.Args = []string{"ttyd"}, nil
		s.APIToken, s.AgentDeploy, s.AgentModel = true, true, true
	})
	alice := fx.signedIn(t, "alice-helper")
	app := fx.newApp(t, "ws-pi-helper", alice, "pi-helper")
	pod := fx.appPod(t, app.ObjectName(), "pod", func(p *corev1.Pod) bool { return len(p.Spec.Containers) > 0 })
	if hasTokenEnv(pod) || secretEnv(pod, envAgentKey) != nil {
		t.Fatalf("the agent container holds a credential: %+v", pod.Spec.Containers[0].Env)
	}
	_, helper := initContainer(pod, AgentHelperContainer)
	if helper == nil || envNamed(helper, envToken) == nil || envNamed(helper, envAgentKey) == nil {
		t.Fatalf("helper %+v", helper)
	}
}
