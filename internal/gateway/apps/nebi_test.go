package apps_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
)

// Creating, starting or restarting an app that gets a Nebi token stores the
// owner's token at once, from the session doing it, so the pod finds the
// file when it first starts rather than after the kubelet's next refresh.
func TestCreatingOrStartingAnAppStoresTheOwnersNebiToken(t *testing.T) {
	tpl := gatewaytest.Template("lab", "jupyterlab")
	tpl.Spec.NebiToken = true
	src := &gatewaytest.NebiSource{Now: time.Now}
	seededApp := gatewaytest.App("alice-aaaaaaaa", "seeded")
	seededApp.Template = "lab"
	f := apitest.New(t, apitest.Options{Nebi: src}, tpl, seededApp)
	opener := gatewaytest.Keyset(t, f.Db)
	tok, err := f.Store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: "alice-aaaaaaaa", Username: "alice"},
		Provider: "local", Kind: session.KindUI, IDToken: "id-alice", RevalidateAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: model.SessionCookieName, Value: tok}
	stored := func(appID string) bool {
		sealed, _, err := f.Db.NebiTokens().Get(context.Background(), appID)
		if err != nil {
			return false
		}
		got, err := opener.Open(sealed, secrets.NebiTokenAD(appID, "alice-aaaaaaaa"))
		return err == nil && strings.HasPrefix(string(got), "nebi-")
	}
	w := f.Do(http.MethodPost, "/api/workspaces", `{"displayName":"Lab","template":"lab"}`, cookie)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct{ Name string }
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if a := f.GetWorkspace("alice-aaaaaaaa", created.Name); a == nil || !stored(a.ID) {
		t.Fatal("no Nebi token stored when the app was created")
	}
	seeded := f.GetWorkspace("alice-aaaaaaaa", "seeded")
	if w := f.Do(http.MethodPost, "/api/workspaces/seeded/stop", "", cookie); w.Code != http.StatusOK || stored(seeded.ID) {
		t.Fatalf("stop: %d %s (a stop must not mint)", w.Code, w.Body)
	}
	if w := f.Do(http.MethodPost, "/api/workspaces/seeded/start", "", cookie); w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if !stored(seeded.ID) {
		t.Fatal("no Nebi token stored when the app was started")
	}
}
