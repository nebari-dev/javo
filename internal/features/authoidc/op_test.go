package authoidc_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	opHost     = "idp.test:8080" // never resolves: every test reaches it through config.OIDC.Dial
	opPrefix   = "/auth/realms/javo"
	opIssuer   = "http://" + opHost + opPrefix
	opClientID = "javo"
)

var (
	keyOnce sync.Once
	opKey   *rsa.PrivateKey
)

func signingKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() {
		var err error
		if opKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			t.Fatal(err)
		}
	})
	return opKey
}

// op is a minimal OpenID provider: discovery, JWKS and a token endpoint that
// enforces PKCE, signs real ID tokens and supports refresh.
type op struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// knobs, set by tests
	down          bool                 // every request answers 503
	noEndSession  bool                 // discovery omits end_session_endpoint
	tokenMod      func(map[string]any) // edits the ID token claims before signing
	refreshStatus int                  // when set, the refresh grant answers this status
	refreshError  string               // OAuth error code in that answer
	refreshDelay  time.Duration        // the refresh grant stalls this long
	refreshGroups []string             // groups in refreshed ID tokens; nil means no id_token
	refreshRoles  []string             // a "roles" claim in refreshed ID tokens
	// state
	codes     map[string]grant
	refresh   string
	refreshes int
	issued    []string // every secret value handed out, to prove none is logged
	pkceSeen  []string
}

type grant struct {
	challenge, nonce string
	claims           map[string]any
}

func newOP(t *testing.T) *op {
	t.Helper()
	o := &op{t: t, codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", o.discovery)
	mux.HandleFunc("GET "+opPrefix+"/certs", o.certs)
	mux.HandleFunc("POST "+opPrefix+"/token", o.token)
	o.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		down := o.down
		o.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(o.srv.Close)
	return o
}

// dial maps the unresolvable issuer host to the test server.
func (o *op) dial() map[string]string {
	return map[string]string{opHost: o.srv.Listener.Addr().String()}
}

func (o *op) set(f func()) { o.mu.Lock(); defer o.mu.Unlock(); f() }

// discovery answers at any path that ends in the well-known suffix, always
// with the real issuer, so a wrongly configured issuer still gets a document
// (as Keycloak does) whose issuer differs.
func (o *op) discovery(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
		http.NotFound(w, r)
		return
	}
	doc := map[string]any{
		"issuer":                                opIssuer,
		"authorization_endpoint":                opIssuer + "/protocol/openid-connect/auth",
		"token_endpoint":                        opIssuer + "/token",
		"jwks_uri":                              opIssuer + "/certs",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	o.mu.Lock()
	if !o.noEndSession {
		doc["end_session_endpoint"] = opIssuer + "/protocol/openid-connect/logout"
	}
	o.mu.Unlock()
	writeJSON(w, http.StatusOK, doc)
}

func (o *op) certs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
		{Key: &signingKey(o.t).PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
	}})
}

// authorize plays the browser's trip to the authorization endpoint: it takes
// the redirect the gateway sent, records what the login asked for and returns
// the code the provider would send back.
func (o *op) authorize(loc string, claims map[string]any) string {
	o.t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		o.t.Fatal(err)
	}
	q := u.Query()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pkceSeen = append(o.pkceSeen, q.Get("code_challenge_method"))
	code := rand.Text()
	o.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), claims: claims}
	o.issued = append(o.issued, code)
	return code
}

func (o *op) idToken(claims map[string]any) string {
	o.t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: signingKey(o.t), KeyID: "k1"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		o.t.Fatal(err)
	}
	raw, err := jwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		o.t.Fatal(err)
	}
	return raw
}

func baseClaims(sub string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": opIssuer, "aud": opClientID, "sub": sub,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

func (o *op) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		o.mu.Lock()
		g, ok := o.codes[r.PostForm.Get("code")]
		delete(o.codes, r.PostForm.Get("code"))
		o.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		claims := baseClaims("u-alice")
		claims["nonce"] = g.nonce
		for k, v := range g.claims {
			claims[k] = v
		}
		o.mu.Lock()
		if o.tokenMod != nil {
			o.tokenMod(claims)
		}
		o.refresh = rand.Text()
		refresh := o.refresh
		id := o.idToken(claims)
		o.issued = append(o.issued, id, refresh)
		o.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": "at-" + rand.Text(), "token_type": "Bearer", "expires_in": 300,
			"refresh_token": refresh, "id_token": id,
		})
	case "refresh_token":
		o.mu.Lock()
		o.refreshes++
		status, code, delay, groups, roles := o.refreshStatus, o.refreshError, o.refreshDelay, o.refreshGroups, o.refreshRoles
		o.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if status != 0 {
			writeJSON(w, status, map[string]string{"error": code})
			return
		}
		o.mu.Lock()
		if r.PostForm.Get("refresh_token") != o.refresh {
			o.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		o.refresh = rand.Text() // rotation: the old token is dead
		out := map[string]any{"access_token": "at-" + rand.Text(), "token_type": "Bearer", "expires_in": 300, "refresh_token": o.refresh}
		o.issued = append(o.issued, o.refresh)
		if groups != nil {
			claims := baseClaims("u-alice")
			claims["groups"] = groups
			if roles != nil {
				claims["roles"] = roles
			}
			id := o.idToken(claims)
			out["id_token"] = id
			o.issued = append(o.issued, id)
		}
		o.mu.Unlock()
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
