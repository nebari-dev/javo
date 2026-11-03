package imageref

import (
	"errors"
	"testing"
)

func TestCheckAcceptsImageReferences(t *testing.T) {
	for _, ref := range []string{
		"nginx",
		"nginxinc/nginx-unprivileged:1.27-alpine",
		"registry.example.com:5000/team/app:1.0",
		"ghcr.io/example/app@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	} {
		if err := Check(ref, nil); err != nil {
			t.Fatalf("Check(%q): %v", ref, err)
		}
	}
}

func TestCheckRefusesWhatIsNotAnImageReference(t *testing.T) {
	for _, ref := range []string{"", "Nginx", "nginx:", "a b", "http://example.com/app", "app@sha256:abc"} {
		if err := Check(ref, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Check(%q) = %v, want ErrInvalid", ref, err)
		}
	}
}

// An entry is a registry host, or a host and path prefix; Docker Hub short
// names belong to docker.io.
func TestCheckKeepsToTheAllowedRegistries(t *testing.T) {
	allowed := []string{"ghcr.io/example", "registry.example.com:5000", "docker.io"}
	for _, ref := range []string{"ghcr.io/example/app:1", "registry.example.com:5000/x/y", "nginx", "nginxinc/nginx-unprivileged"} {
		if err := Check(ref, allowed); err != nil {
			t.Fatalf("Check(%q): %v", ref, err)
		}
	}
	for ref, registry := range map[string]string{
		"ghcr.io/other/app:1":      "ghcr.io",
		"ghcr.io/examplefake/app":  "ghcr.io",
		"registry.example.com/x/y": "registry.example.com",
		"quay.io/example/app@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": "quay.io",
	} {
		var na *NotAllowedError
		if err := Check(ref, allowed); !errors.As(err, &na) || na.Registry != registry {
			t.Fatalf("Check(%q) = %v, want NotAllowedError for %s", ref, err, registry)
		}
	}
}
