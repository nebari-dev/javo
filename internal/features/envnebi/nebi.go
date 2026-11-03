// Package envnebi lists and fetches Nebi environments (Nebi workspaces) as
// the signed-in person. It trades the person's OpenID Connect ID token for a
// Nebi token at Nebi's POST /api/v1/auth/device-token, so it sees exactly
// what that person sees in Nebi; it holds no credential of its own.
package envnebi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/ext/envsource"
)

// Config is the Nebi server the gateway talks to.
type Config struct {
	// URL is Nebi's address with its scheme and any path prefix, such as
	// https://nebi.example.com.
	URL string
	// Timeout bounds one request; zero means 15 seconds.
	Timeout time.Duration
}

// maxBody caps what one Nebi answer may be; a pixi.lock is the largest.
const maxBody = 16 << 20

// Source is the Nebi envsource.Source.
type Source struct {
	base   string
	client *http.Client
}

var _ envsource.Source = (*Source)(nil)

// New returns the Source for cfg.
func New(cfg Config) *Source {
	t := cfg.Timeout
	if t == 0 {
		t = 15 * time.Second
	}
	return &Source{base: strings.TrimSuffix(cfg.URL, "/"), client: &http.Client{Timeout: t}}
}

// ID is the name apps and jobs store for environments from Nebi.
func (s *Source) ID() string { return "nebi" }

type workspace struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Owner  struct {
		Username string `json:"username"`
	} `json:"owner"`
}

func (w workspace) env() envsource.Environment {
	return envsource.Environment{ID: w.ID, Name: w.Name, Owner: w.Owner.Username}
}

// List returns the ready workspaces the person owns or that are shared with
// them; others have no lockfile to run.
func (s *Source) List(ctx context.Context, idToken string) ([]envsource.Environment, error) {
	tok, err := s.signIn(ctx, idToken)
	if err != nil {
		return nil, err
	}
	var all []workspace
	if err := s.getJSON(ctx, tok, "/api/v1/workspaces", &all); err != nil {
		return nil, err
	}
	out := []envsource.Environment{}
	for _, w := range all {
		if w.Status == "ready" {
			out = append(out, w.env())
		}
	}
	return out, nil
}

// workspaceID is what Nebi's ids look like; anything else never reaches a URL.
var workspaceID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Fetch returns the newest version of workspace id with its files.
func (s *Source) Fetch(ctx context.Context, idToken, id string) (envsource.Pinned, error) {
	if !workspaceID.MatchString(id) {
		return envsource.Pinned{}, envsource.ErrNotFound
	}
	tok, err := s.signIn(ctx, idToken)
	if err != nil {
		return envsource.Pinned{}, err
	}
	var w workspace
	if err := s.getJSON(ctx, tok, "/api/v1/workspaces/"+id, &w); err != nil {
		return envsource.Pinned{}, err
	}
	var versions []struct {
		Number int `json:"version_number"`
	}
	if err := s.getJSON(ctx, tok, "/api/v1/workspaces/"+id+"/versions", &versions); err != nil {
		return envsource.Pinned{}, err
	}
	newest := 0
	for _, v := range versions {
		newest = max(newest, v.Number)
	}
	if newest == 0 {
		return envsource.Pinned{}, envsource.ErrNotFound
	}
	n := strconv.Itoa(newest)
	p := envsource.Pinned{Environment: w.env(), Version: n}
	if p.Manifest, err = s.getText(ctx, tok, "/api/v1/workspaces/"+id+"/versions/"+n+"/pixi-toml"); err != nil {
		return envsource.Pinned{}, err
	}
	if p.Lock, err = s.getText(ctx, tok, "/api/v1/workspaces/"+id+"/versions/"+n+"/pixi-lock"); err != nil {
		return envsource.Pinned{}, err
	}
	return p, nil
}

// TokenLifetime is how long a Nebi token lasts when its expiry cannot be
// read from it: Nebi's own lifetime for the tokens it signs.
const TokenLifetime = 24 * time.Hour

// Token is a Nebi token of the person whose ID token is idToken, for an app
// to call Nebi as that person, with when it expires (the JWT's exp claim,
// else TokenLifetime from now). A refused sign-in is envsource.ErrRefused.
func (s *Source) Token(ctx context.Context, idToken string) (string, time.Time, error) {
	tok, err := s.signIn(ctx, idToken)
	if err != nil {
		return "", time.Time{}, err
	}
	exp, ok := jwtExpiry(tok)
	if !ok {
		exp = time.Now().Add(TokenLifetime)
	}
	return tok, exp, nil
}

// jwtExpiry reads the exp claim of a JWT without checking its signature:
// only to know when to ask for a new one.
func jwtExpiry(raw string) (time.Time, bool) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp *int64 `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil || c.Exp == nil {
		return time.Time{}, false
	}
	return time.Unix(*c.Exp, 0).UTC(), true
}

// signIn trades the person's ID token for a Nebi token. Nebi verifies the ID
// token against its own issuer and client id, and finds or creates the Nebi
// user bound to its issuer and subject.
func (s *Source) signIn(ctx context.Context, idToken string) (string, error) {
	if idToken == "" {
		return "", envsource.ErrRefused
	}
	body, _ := json.Marshal(map[string]string{"id_token": idToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/api/v1/auth/device-token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := s.do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("nebi: sign-in answer has no token")
	}
	return out.Token, nil
}

func (s *Source) getJSON(ctx context.Context, tok, path string, into any) error {
	raw, err := s.get(ctx, tok, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("nebi: GET %s: %w", path, err)
	}
	return nil
}

func (s *Source) getText(ctx context.Context, tok, path string) (string, error) {
	raw, err := s.get(ctx, tok, path)
	return string(raw), err
}

func (s *Source) get(ctx context.Context, tok, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return s.do(req)
}

// do sends req and maps Nebi's refusals onto the envsource errors. Error
// texts carry the path, never a token.
func (s *Source) do(req *http.Request) ([]byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nebi: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("nebi: %s %s: %w", req.Method, req.URL.Path, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, envsource.ErrRefused
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return nil, envsource.ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("nebi: %s %s answered %d", req.Method, req.URL.Path, resp.StatusCode)
	case len(raw) > maxBody:
		return nil, fmt.Errorf("nebi: %s %s: answer larger than %d bytes", req.Method, req.URL.Path, maxBody)
	}
	return raw, nil
}
