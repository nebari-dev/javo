package openapicmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/gateway"
)

func TestOpenAPIPrintsTheServedDocumentWithoutACluster(t *testing.T) {
	root := clitest.Root(New())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"openapi"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var printed map[string]any
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}

	rec := httptest.NewRecorder()
	gateway.NewServer(docConfig()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))
	var served map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("served document: %v", err)
	}
	if !reflect.DeepEqual(printed, served) {
		t.Fatal("printed document differs from the one the gateway serves")
	}
	// The UI's generated client calls every operation, including the file
	// picker's, so the printed document must describe it.
	paths, _ := printed["paths"].(map[string]any)
	if _, ok := paths["/api/files"]; !ok {
		t.Fatal("document has no /api/files operation")
	}
	for _, p := range []string{"/api/auth/local/login", "/api/auth/options"} {
		if _, ok := paths[p]; !ok {
			t.Fatalf("document has no %s operation", p)
		}
	}
	if printed["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", printed["openapi"])
	}
}
