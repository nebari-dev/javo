// Package imageref checks a container image reference a person typed
// (github.com/distribution/reference, the grammar registries and the
// container runtime use) against the registries an admin allows.
package imageref

import (
	"errors"
	"strings"

	"github.com/distribution/reference"
)

// ErrInvalid is a reference that is not an image name.
var ErrInvalid = errors.New("not an image reference")

// NotAllowedError is a valid reference to a registry the admin does not allow.
type NotAllowedError struct{ Registry string }

func (e *NotAllowedError) Error() string { return "images from " + e.Registry + " are not allowed" }

// Check returns nil when ref is an image reference that allowed permits.
// Each allowed entry is a registry host (docker.io, registry.example.com:5000)
// or a host and path prefix (ghcr.io/example); empty allows any registry.
// Short Docker Hub names count as docker.io/library/...
func Check(ref string, allowed []string) error {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return ErrInvalid
	}
	if len(allowed) == 0 {
		return nil
	}
	name := named.Name()
	for _, a := range allowed {
		a = strings.TrimSuffix(a, "/")
		if name == a || strings.HasPrefix(name, a+"/") {
			return nil
		}
	}
	return &NotAllowedError{Registry: reference.Domain(named)}
}
