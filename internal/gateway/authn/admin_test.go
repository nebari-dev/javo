package authn

import (
	"context"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/nebari-dev/javo/internal/config"
)

func TestRegisterAdminRefusesAPathOutsideAdmin(t *testing.T) {
	t.Parallel()
	api := humago.New(http.NewServeMux(), huma.DefaultConfig("test", "v1"))
	defer func() {
		if recover() == nil {
			t.Fatal("registering /api/workspaces/x through RegisterAdmin did not panic")
		}
	}()
	RegisterAdmin(api, config.Roles{}, huma.Operation{
		OperationID: "notAdmin", Method: http.MethodGet, Path: "/api/workspaces/x",
	}, func(context.Context, *struct{}) (*struct{}, error) { return &struct{}{}, nil })
}
