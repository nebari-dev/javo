// Package codehostgithub is the code host for repositories on github.com: it
// lists open pull requests through google/go-github and checks webhook
// signatures with its ValidatePayload.
package codehostgithub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/model"
)

// ID is the code host's registry id.
const ID = "github"

const (
	// pageSize is how many pull requests one request asks for; OpenPulls reads
	// pages until codehost.MaxPulls.
	pageSize = 50
	// requestTimeout bounds one request to the API.
	requestTimeout = 30 * time.Second
	// secondaryWait is how long a secondary rate limit that names no wait is
	// treated as lasting.
	secondaryWait = time.Minute
)

// Config is what the platform config gives the host (source.previews.github).
type Config struct {
	// APIURL is the REST API base; empty means https://api.github.com.
	APIURL string
	// TokenFile holds the platform's own token, used only for public
	// repositories whose owner pasted none; empty means anonymous requests.
	// The token should have public-repository read access only.
	TokenFile string
}

// Host is the GitHub code host.
type Host struct{ cfg Config }

// New returns the GitHub code host.
func New(cfg Config) *Host { return &Host{cfg: cfg} }

// ID implements codehost.Host.
func (*Host) ID() string { return ID }

// Owns implements codehost.Host: a repository address on github.com.
func (*Host) Owns(repoURL string) bool {
	_, _, ok := model.GitHubRepo(repoURL)
	return ok
}

// OpenPulls implements codehost.Host. The token is the owner's, else the
// platform file's (for a public repository only), else none.
func (h *Host) OpenPulls(ctx context.Context, repoURL, token string) ([]codehost.PullRequest, error) {
	owner, repo, ok := model.GitHubRepo(repoURL)
	if !ok {
		return nil, errors.New("not a repository on github.com")
	}
	client, err := h.client(token)
	if err != nil {
		return nil, err
	}
	if token == "" && h.cfg.TokenFile != "" {
		// The platform token may see private repositories; never let an app
		// owner borrow it to read one.
		info, _, err := client.Repositories.Get(ctx, owner, repo)
		if err != nil {
			return nil, classify(err)
		}
		if info.GetPrivate() || (info.GetVisibility() != "" && info.GetVisibility() != "public") {
			return nil, fmt.Errorf("%w: it is not public and the app has no token for it", codehost.ErrNotFound)
		}
	}
	opts := &github.PullRequestListOptions{
		State: "open", Sort: "updated", Direction: "desc",
		ListOptions: github.ListOptions{PerPage: pageSize},
	}
	var out []codehost.PullRequest
	for {
		page, resp, err := client.PullRequests.List(ctx, owner, repo, opts)
		if err != nil {
			return nil, classify(err)
		}
		for _, pr := range page {
			out = append(out, codehost.PullRequest{
				Number: int32(pr.GetNumber()), Title: pr.GetTitle(), HeadSHA: pr.GetHead().GetSHA(),
				// A deleted fork has no head repository, which also differs.
				Fork:      !strings.EqualFold(pr.GetHead().GetRepo().GetFullName(), pr.GetBase().GetRepo().GetFullName()),
				UpdatedAt: pr.GetUpdatedAt().Time,
			})
		}
		if len(out) >= codehost.MaxPulls {
			return out[:codehost.MaxPulls], nil
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// VerifyWebhook implements codehost.Host: the HMAC SHA-256 signature in
// X-Hub-Signature-256 over the body, and the event named in X-GitHub-Event.
func (*Host) VerifyWebhook(r *http.Request, secret []byte) (string, error) {
	if _, err := github.ValidatePayload(r, secret); err != nil {
		return "", err
	}
	return github.WebHookType(r), nil
}

func (h *Host) client(ownerToken string) (*github.Client, error) {
	token := ownerToken
	if token == "" && h.cfg.TokenFile != "" {
		b, err := os.ReadFile(h.cfg.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read the platform GitHub token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	opts := []github.ClientOptionsFunc{github.WithTimeout(requestTimeout)}
	if token != "" {
		opts = append(opts, github.WithAuthToken(token))
	}
	if h.cfg.APIURL != "" {
		opts = append(opts, github.WithURLs(&h.cfg.APIURL, nil))
	}
	client, err := github.NewClient(opts...)
	if err != nil {
		// go-github's messages name the setting, never the token.
		return nil, fmt.Errorf("source.previews.github: %w", err)
	}
	return client, nil
}

// classify turns go-github's two rate limit errors into one typed error, and
// the refusals the app owner can fix into codehost.ErrNotFound (404: wrong
// address, or private and the token cannot see it) and codehost.ErrAuth (401
// and any other 403: the token is bad or may not read the repository).
func classify(err error) error {
	var primary *github.RateLimitError
	if errors.As(err, &primary) {
		return &codehost.RateLimitError{Reset: primary.Rate.Reset.Time}
	}
	var secondary *github.AbuseRateLimitError
	if errors.As(err, &secondary) {
		wait := secondaryWait
		if secondary.RetryAfter != nil {
			wait = *secondary.RetryAfter
		}
		return &codehost.RateLimitError{Reset: time.Now().Add(wait)}
	}
	var resp *github.ErrorResponse
	if errors.As(err, &resp) && resp.Response != nil {
		switch resp.Response.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", codehost.ErrNotFound, err)
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %w", codehost.ErrAuth, err)
		}
	}
	return err
}
