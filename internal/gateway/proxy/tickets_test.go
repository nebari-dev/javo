package proxy

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The proxy finds an app in its owner's namespace from the address alone.
func TestAppLookupFindsPerUserApp(t *testing.T) {
	ws := gatewaytest.Workspace("report", aliceID, "Report")
	ws.Namespace = "javo-" + aliceID
	db := storetest.New(t)
	gatewaytest.Seed(t, db, []any{ws, gatewaytest.Template("streamlit", "streamlit")})
	l := &AppLookup{DB: db}

	got, tpl := l.load(context.Background(), exposure.Target{ObjectName: ws.ObjectName(), Owner: aliceID, Name: "report"})
	if got == nil || got.Namespace != ws.Namespace || tpl == nil || tpl.Name != "streamlit" {
		t.Fatalf("lookup = %+v, %+v", got, tpl)
	}
	if other, _ := l.load(context.Background(), exposure.Target{ObjectName: ws.ObjectName(), Owner: bobID, Name: "report"}); other != nil {
		t.Fatal("bob's address opened alice's app")
	}
}

// An app being deleted is not served, though its row is still there until
// the controller has removed its Pod.
func TestAppLookupSkipsAnAppBeingDeleted(t *testing.T) {
	db := storetest.New(t)
	gatewaytest.Seed(t, db, []any{gatewaytest.Workspace("report", aliceID, "Report")})
	app, err := db.Apps().GetByOwnerName(context.Background(), aliceID, "report")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Apps().MarkDeleted(context.Background(), gatewaytest.Actor, app.ID); err != nil {
		t.Fatal(err)
	}
	l := &AppLookup{DB: db}
	for _, t2 := range []exposure.Target{
		{ObjectName: app.ObjectName(), Owner: aliceID, Name: "report"}, {ObjectName: app.ObjectName()},
	} {
		if got, _ := l.load(context.Background(), t2); got != nil {
			t.Fatalf("served an app that is being deleted: %+v", got)
		}
	}
}

// With an address that does not name the owner (subdomains), the object name
// finds the one app that carries it, whatever namespace it is in and however
// many other people have an app of the same short name.
func TestLookupWithoutOwnerFindsTheAppByObjectName(t *testing.T) {
	objs := []any{}
	for _, owner := range []string{"carol-3c4d5e6f", "dave-4d5e6f7a", aliceID, "erin-5e6f7a8b"} {
		ws := gatewaytest.Workspace("report", owner, "Report")
		ws.Namespace = "javo-" + owner
		objs = append(objs, ws)
	}
	db := storetest.New(t)
	gatewaytest.Seed(t, db, objs)
	l := &AppLookup{DB: db}

	got, _ := l.load(context.Background(), exposure.Target{ObjectName: naming.ObjectName("erin-5e6f7a8b", "report")})
	if got == nil || got.Namespace != "javo-erin-5e6f7a8b" || got.Owner != "erin-5e6f7a8b" {
		t.Fatalf("lookup = %+v", got)
	}
	if got, _ := l.load(context.Background(), exposure.Target{ObjectName: "nobody-report"}); got != nil {
		t.Fatalf("an unknown object name found %+v", got)
	}
}

const (
	aliceID = "alice-1a2b3c4d"
	bobID   = "bob-2b3c4d5e"
)
