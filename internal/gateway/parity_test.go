package gateway_test

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// TestEveryUIOperationAcceptsToken walks the OpenAPI document: every
// operation a session may call answers a read-write token exactly as it
// answers the session, and a read token the same for GET and 403 for the
// rest. Only the token operations themselves and plan approval need a session. A route added
// for the UI that refuses tokens fails here.
func TestEveryUIOperationAcceptsToken(t *testing.T) {
	for _, op := range apiOperations(t) {
		if op.public() {
			continue
		}
		t.Run(op.id, func(t *testing.T) {
			// Each operation has fixtures of its own.
			t.Parallel()
			viaSession, c := parityFixture(t)
			want := call(viaSession, op, c, "")
			if want.Code == http.StatusUnauthorized {
				t.Fatalf("%s: a session gets 401: %s", op, want.Body)
			}
			for _, scope := range []string{"read-write", "read"} {
				viaToken, c := parityFixture(t)
				tok := viaToken.NewToken(c, `{"name":"parity","scope":"`+scope+`"}`).Token
				got := call(viaToken, op, nil, tok)
				var wantCode int
				var wantTitle string
				switch {
				case op.sessionOnly() && authn.IsPlanDecision(op.path):
					// Approving a proposed change is for a person in the browser.
					wantCode, wantTitle = http.StatusForbidden, reasons.Msg(reasons.MsgPlanSessionOnly)
				case op.sessionOnly():
					if op.path != "/api/tokens" && op.path != "/api/tokens/{id}" {
						t.Fatalf("%s needs a browser session; only the token and plan decision operations may", op)
					}
					wantCode, wantTitle = http.StatusForbidden, reasons.Msg(reasons.MsgTokenCannotManageTokens)
				case scope == "read" && op.method != http.MethodGet:
					wantCode, wantTitle = http.StatusForbidden, reasons.Msg(reasons.MsgTokenReadOnly)
				default:
					wantCode = want.Code
				}
				if got.Code != wantCode {
					t.Fatalf("%s with a %s token: %d %s, want %d (session got %d %s)", op, scope, got.Code, got.Body, wantCode, want.Code, want.Body)
				}
				if wantTitle != "" && apitest.Decode[apitest.Problem](t, got).Title != wantTitle {
					t.Fatalf("%s with a %s token: %s, want %q", op, scope, got.Body, wantTitle)
				}
			}
		})
	}
}

// TestUICallsOnlyDocumentedOperations finds every /api path the UI source
// names outside the generated client and checks it is an operation of the
// document, so the UI cannot reach a route a token cannot.
func TestUICallsOnlyDocumentedOperations(t *testing.T) {
	var docPaths []*regexp.Regexp
	for _, op := range apiOperations(t) {
		re := regexp.MustCompile(`\\\{[^}]+\\\}`).ReplaceAllString(regexp.QuoteMeta(op.path), `[^/]+`)
		docPaths = append(docPaths, regexp.MustCompile("^"+re+"$"))
	}
	root := filepath.Join("..", "..", "ui", "src")
	found := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if strings.HasSuffix(name, ".gen.ts") || strings.Contains(name, ".test.") || !(strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".tsx")) {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range uiAPIPath.FindAllStringSubmatch(string(src), -1) {
			// ${...} parts of a template string stand for one path segment.
			path := regexp.MustCompile(`\$\{[^}]*\}`).ReplaceAllString(m[1], "x")
			if path == "/api/" || path == "/api/docs/" {
				continue // a prefix test and the docs page, not calls
			}
			found++
			ok := false
			for _, re := range docPaths {
				ok = ok || re.MatchString(path)
			}
			if !ok {
				t.Errorf("%s calls %s, which is not an operation of the API document", p, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("found no hand-built /api paths in the UI; is ui/src there?")
	}
}

// TestCommittedSpecMatchesServer compares ui/openapi.json, which the generated
// client is built from, with the document the server serves. A renamed or
// removed operation with a stale client fails here; run `make openapi`.
func TestCommittedSpecMatchesServer(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", "ui", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	served, err := json.Marshal(apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{}, Thumbnails: true}).Srv.API.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(committed, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(served, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("ui/openapi.json differs from the served API document; run `make openapi` and `pnpm generate` in ui")
	}
}

// parityOp is one operation of the OpenAPI document.
type parityOp struct {
	method, path, id string
	body             bool
	security         []map[string][]string
}

func (o parityOp) String() string { return o.method + " " + o.path + " (" + o.id + ")" }

// public reports whether the operation needs no sign-in at all.
func (o parityOp) public() bool { return o.security != nil && len(o.security) == 0 }

// sessionOnly reports whether only a browser session may call the operation.
func (o parityOp) sessionOnly() bool {
	for _, req := range o.security {
		if _, ok := req[authn.BearerScheme]; ok {
			return false
		}
	}
	return len(o.security) > 0
}

// apiOperations reads every operation the server documents.
func apiOperations(t *testing.T) []parityOp {
	t.Helper()
	doc := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{}}).Srv.API.OpenAPI()
	var ops []parityOp
	for path, item := range doc.Paths {
		for method, op := range map[string]*huma.Operation{
			http.MethodGet: item.Get, http.MethodPost: item.Post, http.MethodPut: item.Put,
			http.MethodPatch: item.Patch, http.MethodDelete: item.Delete,
		} {
			if op == nil {
				continue
			}
			sec := op.Security
			if sec == nil {
				sec = doc.Security
			}
			ops = append(ops, parityOp{method, path, op.OperationID, op.RequestBody != nil, sec})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].path+ops[i].method < ops[j].path+ops[j].method })
	if len(ops) < 30 {
		t.Fatalf("only %d operations in the document", len(ops))
	}
	return ops
}

// parityPath fills an operation's path parameters with names of the objects
// parityFixture seeds.
func parityPath(p string) string {
	return strings.NewReplacer("{name}", "sales", "{template}", "streamlit", "{run}", "r1", "{id}", "nope").Replace(p)
}

// parityFixture is a gateway with alice signed in, a template and an app of
// hers, the same every time it is built.
func parityFixture(t *testing.T) (*apitest.Fixture, *http.Cookie) {
	t.Helper()
	f := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{}}, gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Workspace("sales", apitest.LocalAlice(), "Sales"))
	return f, f.LoginAlice()
}

// call makes one request with a session cookie (tok empty) or a token.
func call(f *apitest.Fixture, op parityOp, c *http.Cookie, tok string) *httptest.ResponseRecorder {
	f.T.Helper()
	body := ""
	if op.body {
		body = "{}"
	}
	if tok == "" {
		return f.Do(op.method, parityPath(op.path), body, c)
	}
	return f.Bearer(op.method, parityPath(op.path), body, tok, nil)
}

// uiAPIPath matches a path the UI builds by hand: '/api/...' or `/api/...`.
var uiAPIPath = regexp.MustCompile("['`\"](/api/[^'`\"?]*)")
