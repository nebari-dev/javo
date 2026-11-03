// Package ctrltest starts the API server that controller tests run against.
package ctrltest

import (
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// allowAll lets every request through, including the unauthenticated health
// checks envtest makes while the server starts.
const allowAll = `{"apiVersion":"abac.authorization.kubernetes.io/v1beta1","kind":"Policy","spec":{"user":"*","namespace":"*","resource":"*","apiGroup":"*","nonResourcePath":"*"}}
{"apiVersion":"abac.authorization.kubernetes.io/v1beta1","kind":"Policy","spec":{"group":"system:unauthenticated","nonResourcePath":"*","readonly":true}}
`

// Start starts etcd and kube-apiserver and returns the config of an admin
// client and a function that stops them.
//
// The server serves no RBAC and no PriorityClass API: making their default
// objects keeps it from becoming ready for over a second, and nothing the
// controllers do uses them (the admin client is in system:masters, which RBAC
// never checks). Requests are authorized by an allow-all ABAC policy instead.
func Start() (*rest.Config, func(), error) {
	dir, err := os.MkdirTemp("", "ctrltest")
	if err != nil {
		return nil, nil, err
	}
	policy := filepath.Join(dir, "policy.jsonl")
	if err := os.WriteFile(policy, []byte(allowAll), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	env := &envtest.Environment{}
	env.ControlPlane.GetAPIServer().Configure().
		Set("authorization-mode", "ABAC").
		Set("authorization-policy-file", policy).
		Set("runtime-config", "rbac.authorization.k8s.io/v1=false,scheduling.k8s.io/v1=false").
		// Room for the Services of every test of a -count run: no namespace
		// controller runs, so they are never deleted.
		Set("service-cluster-ip-range", "10.0.0.0/16")
	cfg, err := env.Start()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	return cfg, func() {
		_ = env.Stop()
		_ = os.RemoveAll(dir)
	}, nil
}
