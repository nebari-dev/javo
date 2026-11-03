package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// Sessions written before the provider's tokens were sealed hold them in
// clear: the upgrade ends them (their app sessions with them), and keeps the
// sessions that hold none.
func TestUpgradeEndsSessionsWithTokensInClear(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.Open(t, storetest.Config(t))
	if err := s.MigrateTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	insert := func(id, parent, refresh, idToken string) {
		var p any
		if parent != "" {
			p = parent
		}
		storetest.Exec(t, s, `INSERT INTO sessions (id, kind, parent_id, person, principal, provider, workspace,
			refresh, id_token, expires_at, revalidate_at, version) VALUES (?, ?, ?, 'alice', '{}', 'oidc', '', ?, ?, 0, 0, 1)`,
			id, map[bool]string{true: "ui", false: "app"}[parent == ""], p, refresh, idToken)
	}
	insert("oidc-ui", "", "refresh-1", "id-1")
	insert("oidc-app", "oidc-ui", "", "")
	insert("only-id-token", "", "", "id-2")
	insert("local-ui", "", "", "")
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for id, kept := range map[string]bool{"oidc-ui": false, "oidc-app": false, "only-id-token": false, "local-ui": true} {
		_, err := s.Sessions().Get(ctx, id)
		if kept && err != nil {
			t.Errorf("%s: %v, want kept", id, err)
		}
		if !kept && !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v, want ended", id, err)
		}
	}
}
