package servercmd

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The gateway makes its sessions key in the named Secret on its first start
// and every later gateway reads the same key, so a restart or another
// replica opens the sessions the first one sealed.
func TestTheSessionsKeyIsMadeOnceAndShared(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	o := gatewayOptions{sessionsKeySecret: "javo-sessions-key", sessionsKeyCreate: true}
	first, err := o.sessionsKeyOf(ctx, c, "javo-system")
	if err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "javo-system", Name: "javo-sessions-key"}, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Data["key"]) != 32 {
		t.Fatalf("key is %d bytes", len(s.Data["key"]))
	}
	second, err := (&gatewayOptions{sessionsKeySecret: "javo-sessions-key"}).sessionsKeyOf(ctx, c, "javo-system")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := first.Encrypt([]byte("refresh"), []byte("ad"))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := second.Decrypt(ct, []byte("ad")); err != nil || string(pt) != "refresh" {
		t.Fatalf("another gateway does not open it: %v", err)
	}
}

// The controller and the gateway read one fingerprint key, whichever of
// them makes it, so the gateway finds what the controller fingerprinted.
func TestBothComponentsReadOneFingerprintKey(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	flags := commonFlags{namespace: "javo-system", fingerprintKeySecret: "javo-secret-fingerprint-key", fingerprintKeyCreate: true}
	ctl, err := flags.fingerprinter(ctx, c)
	if err != nil || ctl == nil {
		t.Fatalf("controller: %v, %v", ctl, err)
	}
	gw, err := (&commonFlags{namespace: "javo-system", fingerprintKeySecret: "javo-secret-fingerprint-key"}).fingerprinter(ctx, c)
	if err != nil || gw == nil || gw.KeyID() != ctl.KeyID() {
		t.Fatalf("gateway: %v, %v", gw, err)
	}
	if none, err := (&commonFlags{}).fingerprinter(ctx, c); none != nil || err != nil {
		t.Fatalf("without a Secret: %v, %v", none, err)
	}
}
