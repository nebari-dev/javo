package admin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Every operation under /api/admin/ refuses members with 403 and the
// catalog text, and never refuses a platform admin with 403. The walk reads
// the OpenAPI document, so routes added later are covered without a change
// here; one stub route is registered so the walk is never empty.
func TestEveryAdminRouteRejectsMembers(t *testing.T) {
	f := apitest.New(t, apitest.Options{})
	var roles config.Roles
	roles.PlatformAdmin.Groups = []string{"javo-admins"}
	authn.RegisterAdmin(f.Srv.API, roles, huma.Operation{
		OperationID: "adminStub", Method: http.MethodPost, Path: "/api/admin/stub/{name}",
	}, func(context.Context, *struct {
		Name string `path:"name"`
	}) (*struct{}, error) {
		return &struct{}{}, nil
	})

	member := f.Session(apitest.MemberID)
	admin := f.Session(apitest.AdminID, "javo-admins")
	seen := 0
	for p, item := range f.Srv.API.OpenAPI().Paths {
		if !strings.HasPrefix(p, "/api/admin/") {
			continue
		}
		path := apitest.PathParam.ReplaceAllString(p, "x")
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op == nil {
				continue
			}
			seen++
			body := ""
			if method != http.MethodGet && method != http.MethodDelete {
				body = "{}"
			}
			w := f.Do(method, path, body, member)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s as a member: %d %s, want 403", method, p, w.Code, w.Body)
				continue
			}
			if got := apitest.Decode[apitest.Problem](t, w).Title; got != reasons.Msg(reasons.MsgAdminOnly) {
				t.Errorf("%s %s as a member: title %q", method, p, got)
			}
			if w := f.Do(method, path, body, admin); w.Code == http.StatusForbidden {
				t.Errorf("%s %s as an admin: 403 %s", method, p, w.Body)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no admin operation found in the OpenAPI document")
	}
}
