//go:build !local

package gateway

import (
	"context"
	"errors"
	"fmt"
	"github.com/danielgtaylor/huma/v2"
	"github.com/nebari-dev/javo/internal/actions"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/mcp"
)

// tokenVerifier checks an MCP bearer token as the API does (live reports
// whether it works now). Its scope becomes the MCP scopes: write when it may
// propose changes; the API call each tool makes checks the owner and scope
// again.
func tokenVerifier(live func(context.Context, string) (token.Token, error)) auth.TokenVerifier {
	return func(ctx context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
		tok, err := live(ctx, raw)
		if errors.Is(err, token.ErrInvalid) {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		if err != nil {
			return nil, err
		}
		info := &auth.TokenInfo{UserID: tok.Owner, Scopes: []string{mcp.ScopeRead}}
		if tok.Scope.Allows(http.MethodPost, token.PlansRoute) {
			info.Scopes = append(info.Scopes, mcp.ScopeWrite)
		}
		if tok.ExpiresAt != nil {
			info.Expiration = *tok.ExpiresAt
		}
		return info, nil
	}
}

// mountMCP serves the agent endpoint at /mcp on mux, over the API mux
// serves, and the metadata that says where it is. A binary built with the
// tag local carries neither (mcp_local.go).
func mountMCP(mux *http.ServeMux, doc *huma.OpenAPI, acts []actions.Info, live func(context.Context, string) (token.Token, error), publicURL string) {
	mux.Handle(mcp.Route, mcp.NewHandler(mcp.Config{
		API: mux, Doc: doc, Actions: acts,
		Verify: tokenVerifier(live), PublicURL: publicURL, Version: "v1alpha1",
	}))
	mux.Handle("GET "+mcp.MetadataRoute, mcp.MetadataHandler(publicURL))
}
