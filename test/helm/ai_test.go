package helm

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func aiKeyEnv(d corev1.Container) *corev1.EnvVar {
	for i := range d.Env {
		if d.Env[i].Name == "JAVO_AI_API_KEY" {
			return &d.Env[i]
		}
	}
	return nil
}

// The model API key reaches only the gateway, only from the Secret named in
// ai.apiKeySecret, and only when AI is on.
func TestAIKeyComesFromItsSecretOnlyWhenAIIsOn(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		base := engineArgs(engine)
		out, err := render(t, base...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		if e := aiKeyEnv(deploymentOf(t, out, engine, "gateway").Spec.Template.Spec.Containers[0]); e != nil {
			t.Fatalf("%s: AI off but the gateway gets a key: %+v", engine, e)
		}
		on := append(append([]string{}, base...), "--set", "config.ai.enabled=true", "--set", "config.ai.provider=anthropic",
			"--set", "ai.apiKeySecret.name=javo-ai", "--set", "ai.apiKeySecret.key=api-key")
		out, err = render(t, on...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, out)
		}
		e := aiKeyEnv(deploymentOf(t, out, engine, "gateway").Spec.Template.Spec.Containers[0])
		if e == nil || e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
			e.ValueFrom.SecretKeyRef.Name != "javo-ai" || e.ValueFrom.SecretKeyRef.Key != "api-key" {
			t.Fatalf("%s: key env %+v", engine, e)
		}
		if engine == "postgres" {
			if aiKeyEnv(deploymentOf(t, out, engine, "controller").Spec.Template.Spec.Containers[0]) != nil {
				t.Fatal("the controller gets the model key")
			}
		}
	}
}
