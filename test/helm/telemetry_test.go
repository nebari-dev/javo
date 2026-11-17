package helm

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// The OpenTelemetry collector named in telemetry.otlpEndpoint reaches every
// Javo container, the gateway's and the controller's alike; with none named
// no container is told one, so nothing is sent.
func TestTheCollectorReachesEveryJavoContainerOnlyWhenNamed(t *testing.T) {
	t.Parallel()
	const collector = "http://otel-collector.monitoring:4318"
	for _, engine := range engines {
		components := []string{"gateway"}
		if engine == "postgres" {
			components = append(components, "controller")
		}
		off, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, off)
		}
		on, err := render(t, append(engineArgs(engine), "--set", "telemetry.otlpEndpoint="+collector)...)
		if err != nil {
			t.Fatalf("render: %v\n%s", err, on)
		}
		for _, c := range components {
			if v, ok := envValue(deploymentOf(t, off, engine, c).Spec.Template.Spec.Containers[0], "OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
				t.Errorf("%s %s: no collector named, but told %q", engine, c, v)
			}
			if v, _ := envValue(deploymentOf(t, on, engine, c).Spec.Template.Spec.Containers[0], "OTEL_EXPORTER_OTLP_ENDPOINT"); v != collector {
				t.Errorf("%s %s: OTEL_EXPORTER_OTLP_ENDPOINT %q, want %q", engine, c, v, collector)
			}
		}
	}
}
