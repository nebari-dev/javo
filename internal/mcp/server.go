// Package mcp serves the platform to agents (Claude Code, Cursor) over the
// Model Context Protocol, at /mcp with Streamable HTTP, under the person's
// own API token. Tools are generated from the API document: each read
// operation is a tool that calls the same API route as the token's owner,
// and each catalog action is a tool that only proposes the change. There is
// no tool that applies a change; a person approves it in the browser.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/model"
)

// Route is where the server is mounted; MetadataRoute serves its RFC 9728
// protected resource metadata.
const (
	Route         = "/mcp"
	MetadataRoute = "/.well-known/oauth-protected-resource"
)

// Scopes an API token carries, as MCP sees them.
const (
	ScopeRead  = "workloads:read"
	ScopeWrite = "workloads:write"
)

// Config wires the server.
type Config struct {
	// API is the gateway. Tool calls are requests to its /api routes with the
	// caller's own Authorization header, so they pass the same token,
	// scope and owner checks as any API call.
	API http.Handler
	// Doc is the API document the read tools are generated from.
	Doc *huma.OpenAPI
	// Actions are the changes the propose tools offer.
	Actions []actions.Info
	// Verify checks a bearer token: the go-sdk auth middleware calls it.
	Verify auth.TokenVerifier
	// PublicURL is where agents reach the gateway, for approve links and
	// the resource metadata.
	PublicURL string
	// Version is reported to clients.
	Version string
}

// skipped are read operations that are not tools: streams and documents.
var skipped = map[string]bool{"streamWorkspaceLogs": true}

// NewHandler returns the /mcp handler, behind bearer-token auth and
// cross-origin protection. A cookie alone is never enough.
func NewHandler(cfg Config) http.Handler {
	server := sdk.NewServer(&sdk.Implementation{Name: model.ProductName, Version: cfg.Version}, nil)
	for _, t := range readTools(cfg) {
		server.AddTool(t.tool, t.handle)
	}
	for _, a := range cfg.Actions {
		t := proposeTool(cfg, a)
		server.AddTool(t.tool, t.handle)
	}
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	authed := auth.RequireBearerToken(cfg.Verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: strings.TrimRight(cfg.PublicURL, "/") + MetadataRoute,
		Scopes:              []string{ScopeRead},
		// Tokens without an expiry are allowed; the verifier checks revocation.
		AllowMissingExpiration: true,
	})(h)
	return http.NewCrossOriginProtection().Handler(authed)
}

// MetadataHandler serves the protected resource metadata (RFC 9728).
func MetadataHandler(publicURL string) http.Handler {
	base := strings.TrimRight(publicURL, "/")
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               base + Route,
		ScopesSupported:        []string{ScopeRead, ScopeWrite},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           model.ProductName,
		ResourceDocumentation:  base + "/settings",
	})
}

type tool struct {
	tool   *sdk.Tool
	handle sdk.ToolHandler
}

var upper = regexp.MustCompile(`[A-Z]`)

// snake turns an operation id into a tool name: listWorkspaces is list_workspaces.
func snake(id string) string {
	return upper.ReplaceAllStringFunc(id, func(s string) string { return "_" + string(unicode.ToLower(rune(s[0]))) })
}

// readTools are the GET operations a token may call.
func readTools(cfg Config) []tool {
	var out []tool
	paths := make([]string, 0, len(cfg.Doc.Paths))
	for p := range cfg.Doc.Paths {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, path := range paths {
		op := cfg.Doc.Paths[path].Get
		if op == nil || skipped[op.OperationID] || !tokenMay(cfg.Doc, op) {
			continue
		}
		out = append(out, readTool(cfg, path, op))
	}
	return out
}

// tokenMay is true when a personal token may call op.
func tokenMay(doc *huma.OpenAPI, op *huma.Operation) bool {
	sec := op.Security
	if sec == nil {
		sec = doc.Security
	}
	for _, req := range sec {
		for name := range req {
			if s := doc.Components.SecuritySchemes[name]; s != nil && s.Type == "http" && s.Scheme == "bearer" {
				return true
			}
		}
	}
	return false
}

func readTool(cfg Config, path string, op *huma.Operation) tool {
	props := map[string]any{}
	var required []string
	for _, p := range op.Parameters {
		if p.In != "path" && p.In != "query" {
			continue
		}
		s, _ := json.Marshal(p.Schema)
		var schema map[string]any
		_ = json.Unmarshal(s, &schema)
		if schema == nil {
			schema = map[string]any{"type": "string"}
		}
		if p.Description != "" {
			schema["description"] = p.Description
		}
		props[p.Name] = schema
		if p.In == "path" {
			required = append(required, p.Name)
		}
	}
	input := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		input["required"] = required
	}
	t := &sdk.Tool{
		Name: snake(op.OperationID), Description: op.Summary, InputSchema: input,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}
	return tool{tool: t, handle: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult("The arguments are not a JSON object."), nil
			}
		}
		target := path
		q := url.Values{}
		for _, p := range op.Parameters {
			v, ok := args[p.Name]
			if !ok {
				continue
			}
			s := fmt.Sprint(v)
			switch p.In {
			case "path":
				target = strings.ReplaceAll(target, "{"+p.Name+"}", url.PathEscape(s))
			case "query":
				q.Set(p.Name, s)
			}
		}
		if strings.Contains(target, "{") {
			return errorResult("A required argument is missing."), nil
		}
		if len(q) > 0 {
			target += "?" + q.Encode()
		}
		return cfg.call(ctx, req, http.MethodGet, target, nil)
	}}
}

// proposeTool is the tool for one catalog action: it stores a plan and
// returns the link a person approves it at. It applies nothing.
func proposeTool(cfg Config, a actions.Info) tool {
	props := map[string]any{"name": map[string]any{"type": "string", "description": "the short name of your app"}}
	for _, p := range a.Params {
		props[p.Name] = map[string]any{"type": "string", "description": p.Doc}
	}
	t := &sdk.Tool{
		Name:        a.ID + "_workspace",
		Description: a.Summary + ". Proposes the change only: it waits for you to approve it in the browser at the approveUrl returned.",
		InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"name"}, "additionalProperties": false},
		Annotations: &sdk.ToolAnnotations{DestructiveHint: ptr(a.ID == actions.Delete)},
	}
	return tool{tool: t, handle: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]string
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil || args["name"] == "" {
			return errorResult("Give the app's short name as name."), nil
		}
		prop := actions.Proposal{Action: a.ID, Target: args["name"]}
		for _, p := range a.Params {
			if v := args[p.Name]; v != "" {
				if prop.Params == nil {
					prop.Params = map[string]string{}
				}
				prop.Params[p.Name] = v
			}
		}
		body, _ := json.Marshal(map[string]any{"source": "mcp:" + clientName(req), "proposal": prop})
		res, err := cfg.call(ctx, req, http.MethodPost, "/api/plans", body)
		if err != nil || res.IsError {
			return res, err
		}
		var plan struct {
			ID      string          `json:"id"`
			Planned actions.Planned `json:"planned"`
		}
		_ = json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &plan)
		out := map[string]any{
			"planId": plan.ID, "state": "awaiting_approval", "summary": plan.Planned.Summary, "changes": plan.Planned.Changes,
			"approveUrl": strings.TrimRight(cfg.PublicURL, "/") + "/plans/" + url.PathEscape(plan.ID),
		}
		b, _ := json.Marshal(out)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}, StructuredContent: out}, nil
	}}
}

func ptr[T any](v T) *T { return &v }

// clientName is the agent's own name from its initialize request, kept to
// letters, digits and a few marks.
func clientName(req *sdk.CallToolRequest) string {
	name := "agent"
	if req.Session != nil {
		if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil && p.ClientInfo.Name != "" {
			name = p.ClientInfo.Name
		}
	}
	clean := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" ._-", r) {
			return r
		}
		return -1
	}, name)
	if len(clean) > 64 {
		clean = clean[:64]
	}
	if clean == "" {
		clean = "agent"
	}
	return clean
}

// call makes an API request with the caller's own Authorization header and
// returns the answer as the tool result: the JSON body, or the problem's
// title as a tool error.
func (cfg Config) call(ctx context.Context, req *sdk.CallToolRequest, method, target string, body []byte) (*sdk.CallToolResult, error) {
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	r, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	if req.Extra != nil {
		r.Header.Set("Authorization", req.Extra.Header.Get("Authorization"))
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	cfg.API.ServeHTTP(w, r)
	text := w.Body.String()
	if w.Code >= 400 {
		var p struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(w.Body.Bytes(), &p) == nil && p.Title != "" {
			text = p.Title
		}
		return errorResult(text), nil
	}
	res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
	var structured map[string]any
	if json.Unmarshal(w.Body.Bytes(), &structured) == nil {
		res.StructuredContent = structured
	}
	return res, nil
}

func errorResult(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}
