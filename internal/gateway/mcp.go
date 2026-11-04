package gateway

import (
	"context"
	"errors"
	"fmt"
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
