// Package secretenv is what the controller hands of a person's secrets to an
// app's pod or a job's run: which bindings pass authz, their
// values opened for the Kubernetes Secret the container reads, and the
// container's variables, which name that Secret and never hold a value.
package secretenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

// Set is the secrets an app or a job's run gets.
type Set struct {
	Bindings []store.SealedBinding
	// Fingerprint names the bindings and the value versions, never a value;
	// "" when there are none. What was built for another one is out of date.
	Fingerprint string
}

// For keeps the bindings in all that target (an app or a job, as an authz
// object) may get. Each goes through authz (target and secret share an owner,
// and an app is no preview), so a binding written straight into the database
// cannot hand a person's secret to someone else or to a pull request; and a
// variable the platform sets is left out, so a secret never replaces one.
// What is left out is logged, without a value.
func For(ctx context.Context, all []store.SealedBinding, target authz.Object) Set {
	var out Set
	type part struct {
		Env, ID string
		Version int64
	}
	var parts []part
	for _, b := range all {
		sec := b.Secret
		if b.Env == model.GitTokenEnv {
			continue // the clone's, not a variable (GitToken)
		}
		if model.EnvNameProblem(b.Env) != model.EnvNameOK {
			log.FromContext(ctx).Info("secret not given: reserved variable", "secret", sec.ID, "env", b.Env)
			continue
		}
		obj := target
		obj.Secret = &sec
		if d := authz.Decide(authz.Principal{}, nil, authz.ActInjectSecret, obj); !d.Allowed {
			log.FromContext(ctx).Info("secret not given", "secret", sec.ID, "env", b.Env, "reason", d.Code)
			continue
		}
		out.Bindings = append(out.Bindings, b)
		parts = append(parts, part{b.Env, sec.ID, sec.Version})
	}
	if len(parts) > 0 {
		raw, _ := json.Marshal(parts) // the store orders bindings by variable name
		sum := sha256.Sum256(raw)
		out.Fingerprint = hex.EncodeToString(sum[:8])
	}
	return out
}

// GitToken is the repository token in all (model.GitTokenEnv, decision
// 70), if authz lets the controller hand it to target's clone; its
// fingerprint names the secret and its value version. Open keys its value by
// model.GitTokenEnv.
func GitToken(ctx context.Context, all []store.SealedBinding, target authz.Object) Set {
	for _, b := range all {
		if b.Env != model.GitTokenEnv {
			continue
		}
		sec := b.Secret
		obj := target
		obj.Secret = &sec
		if d := authz.Decide(authz.Principal{}, nil, authz.ActInjectSecret, obj); !d.Allowed {
			log.FromContext(ctx).Info("repository token not given", "secret", sec.ID, "reason", d.Code)
			return Set{}
		}
		raw, _ := json.Marshal([]any{sec.ID, sec.Version})
		sum := sha256.Sum256(raw)
		return Set{Bindings: []store.SealedBinding{b}, Fingerprint: hex.EncodeToString(sum[:8])}
	}
	return Set{}
}

// ErrNoKey is the evidence for secrets on a controller started without the
// key-encryption key.
var ErrNoKey = errors.New("secrets are given, but the controller has no secrets key (--secrets-key-file or --secrets-key-secret)")

// Open opens each value with keys (nil: the controller has no key), keyed by
// variable. unreadable is why they could not be opened (never a value), for
// a status; data is then nil. No error or log line holds a value.
func (s Set) Open(ctx context.Context, keys *secrets.Keys) (data map[string][]byte, unreadable string) {
	if keys == nil {
		return nil, ErrNoKey.Error()
	}
	opener, err := keys.Opener(ctx)
	if err != nil {
		return nil, err.Error()
	}
	data = make(map[string][]byte, len(s.Bindings))
	for _, b := range s.Bindings {
		v, err := opener.Open(b.Ciphertext, secrets.AD{ID: b.Secret.ID, Owner: b.Secret.Owner, Name: b.Secret.Name})
		if err != nil {
			return nil, fmt.Sprintf("secret %s: %v", b.Secret.Name, err)
		}
		data[b.Env] = v
	}
	return data, ""
}

// Env is one variable per binding, each read from its key in the Secret
// secretName: by reference, so no value is ever in a pod or Job spec.
func (s Set) Env(secretName string) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(s.Bindings))
	for _, b := range s.Bindings {
		out = append(out, corev1.EnvVar{Name: b.Env, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: b.Env,
		}}})
	}
	return out
}
