package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/store"
)

// Platform admins read the audit log, newest first; nobody else may.
func TestAdminReadsTheAuditLog(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	admin, member := f.Session(apitest.AdminID, "javo-admins"), f.Session(apitest.AliceID)
	for _, action := range []string{"test.first", "test.second"} {
		if err := f.Db.Audit().Append(context.Background(), store.AuditEvent{
			Actor: apitest.AliceID, Via: "session", Action: action, ObjectKind: "file", ObjectName: "a.md", Allowed: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if w := f.Do(http.MethodGet, "/api/admin/audit", "", member); w.Code != http.StatusForbidden {
		t.Fatalf("a member: %d", w.Code)
	}
	w := f.Do(http.MethodGet, "/api/admin/audit?limit=1", "", admin)
	if w.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", w.Code, w.Body)
	}
	var got struct {
		Items []struct {
			Action, Actor, Via, ObjectKind, ObjectName string
			Allowed                                    bool
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Action != "test.second" || got.Items[0].Actor != apitest.AliceID || got.Items[0].ObjectName != "a.md" {
		t.Fatalf("items = %+v", got.Items)
	}
}
