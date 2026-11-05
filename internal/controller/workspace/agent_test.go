package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

func (fx *env) newAgentTemplate(t *testing.T, name string) {
	t.Helper()
	fx.newTemplate(t, name, func(s *model.TemplateSpec) {
		s.Kind, s.Image, s.Port = "claude-code", "example.com/agents:1", 7681
		s.Command, s.Args = []string{"ttyd"}, nil
		s.APIToken, s.AgentDeploy, s.AgentModel = true, true, true
	})
}

func plainEnv(pod *corev1.Pod, name string) (string, bool) {
	e := secretEnv(pod, name)
	if e == nil || e.ValueFrom != nil {
		return "", false
	}
	return e.Value, true
}

// With the admin's agent model on, an agent app is pointed at the gateway's
// endpoint and authenticates there with its own token; its first prompt
// comes in a variable.
func TestAgentAppGetsThePlatformModel(t *testing.T) {
	t.Parallel()
	fx := newEnv(t, func(r *Reconciler) {
		r.AgentModel = func(context.Context) (string, error) { return "admin/model", nil }
	})
	fx.newAgentTemplate(t, "pi-plat")
	alice := fx.signedIn(t, "alice-plat")
	// A prompt stored with characters nobody sees reaches the pod without them.
	app := fx.newApp(t, "ws-pi-plat", alice, "pi-plat", func(a *model.App) { a.AgentPrompt = "build a\u200b dashboard\U000E0041\u202e" })
	pod := fx.appPod(t, app.ObjectName(), "pod", hasTokenEnv)
	for name, want := range map[string]string{
		"JAVO_AGENT_MODE": model.AgentModePlatform, "JAVO_AGENT_MODEL": "admin/model",
		"JAVO_AGENT_BASE_URL": "https://javo.example.com/api/ai/openai/v1", "JAVO_AGENT_PROMPT": "build a dashboard",
	} {
		if got, ok := plainEnv(pod, name); !ok || got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	key := secretEnv(pod, "JAVO_AGENT_KEY")
	if key == nil || key.ValueFrom == nil || key.ValueFrom.SecretKeyRef.Name != APITokenSecretName(app.ObjectName()) || key.ValueFrom.SecretKeyRef.Key != APITokenKey {
		t.Fatalf("JAVO_AGENT_KEY %+v, want the app's own token", key)
	}
}

// Without the admin's model, the owner's own key reaches their agent app,
// never another person's, and never a plain app of theirs.
func TestOwnAgentKeyReachesOnlyTheOwnersAgentApps(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAgentTemplate(t, "pi-own")
	fx.newAPITokenTemplate(t, "plain-own")
	alice := fx.signedIn(t, "alice-own")
	bob := fx.signedIn(t, "bob-own")
	fx.putSecret(t, alice, model.AgentKeySecret, testValue)
	if err := fx.db.AgentSettings().Put(bg, store.AgentSetting{Owner: alice, BaseURL: "https://llm.example.com/v1", Model: "vendor/m1", KeyLast4: "0451"}); err != nil {
		t.Fatal(err)
	}
	mine := fx.newApp(t, "ws-pi-own", alice, "pi-own")
	pod := fx.appPod(t, mine.ObjectName(), "pod", func(p *corev1.Pod) bool { return secretEnv(p, "JAVO_AGENT_KEY") != nil })
	if got, _ := plainEnv(pod, "JAVO_AGENT_MODE"); got != model.AgentModeOwn {
		t.Fatalf("mode %q", got)
	}
	if got, _ := plainEnv(pod, "JAVO_AGENT_BASE_URL"); got != "https://llm.example.com/v1" {
		t.Fatalf("base URL %q", got)
	}
	key := secretEnv(pod, "JAVO_AGENT_KEY")
	if key.ValueFrom == nil || key.ValueFrom.SecretKeyRef.Name != AppSecretsName(mine.ObjectName()) {
		t.Fatalf("JAVO_AGENT_KEY %+v", key)
	}
	if sec := fx.envSecret(mine); sec == nil || string(sec.Data["JAVO_AGENT_KEY"]) != testValue {
		t.Fatal("the key is not in the app's Secret")
	}
	raw, _ := json.Marshal(pod)
	if strings.Contains(string(raw), testValue) {
		t.Fatal("the key is in the pod spec")
	}

	plain := fx.newApp(t, "ws-plain-own", alice, "plain-own")
	bobs := fx.newApp(t, "ws-pi-bob", bob, "pi-own")
	for _, app := range []model.App{plain, bobs} {
		p := fx.appPod(t, app.ObjectName(), "pod", hasTokenEnv)
		if secretEnv(p, "JAVO_AGENT_KEY") != nil {
			t.Errorf("%s got an agent key", app.Name)
		}
		if sec := fx.envSecret(app); sec != nil && len(sec.Data["JAVO_AGENT_KEY"]) > 0 {
			t.Errorf("%s's Secret holds the key", app.Name)
		}
	}
	if got, _ := plainEnv(fx.appPod(t, bobs.ObjectName(), "pod", hasTokenEnv), "JAVO_AGENT_MODE"); got != model.AgentModeLogin {
		t.Fatalf("bob's mode %q", got)
	}
}
