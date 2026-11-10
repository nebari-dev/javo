package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// A device login, in the shape of RFC 8628: a coding agent with no browser
// starts one, the person approves it in the browser at /device, and the
// agent's next poll gets a read-write token of that person.
const (
	deviceRoute         = "/api/auth/device"
	deviceLifetime      = 10 * time.Minute
	devicePollInterval  = 5 * time.Second
	deviceTokenLifetime = 8 * time.Hour
	deviceTokenName     = "Agent login"
	// userCodeAlphabet has no vowels, so no words, and no look-alikes.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLen      = 8
)

// deviceStartLimit is how many device logins one client address may start:
// ten a minute.
var deviceStartLimit = ratelimit.Spec{Every: 6 * time.Second, Burst: 10}

// The RFC 8628 error names a poll answers with.
const (
	deviceErrPending = "authorization_pending"
	deviceErrSlow    = "slow_down"
	deviceErrDenied  = "access_denied"
	deviceErrExpired = "expired_token"
)

// DeviceAPI serves the device login routes. MaxLifetime is
// api.tokens.maxLifetime: the token lives deviceTokenLifetime, or that when
// shorter. PublicURL is where the person's browser reaches the gateway.
type DeviceAPI struct {
	DB          *store.Store
	Tokens      *token.DBStore
	MaxLifetime time.Duration
	PublicURL   string
	Limiter     *ratelimit.Buckets
	Now         func() time.Time
}

// DeviceStart is a new device login: the agent keeps DeviceCode and shows
// the person UserCode and the address.
type DeviceStart struct {
	DeviceCode              string `json:"deviceCode" doc:"The secret the agent polls with; never show it to anyone"`
	UserCode                string `json:"userCode" doc:"The code the person checks on the approval page, such as WDJB-MJHT"`
	VerificationURL         string `json:"verificationURL" doc:"The approval page"`
	VerificationURLComplete string `json:"verificationURLComplete" doc:"The approval page with the code filled in"`
	ExpiresIn               int    `json:"expiresIn" doc:"Seconds until the request expires"`
	Interval                int    `json:"interval" doc:"Seconds to wait between polls"`
}

// DeviceToken is the token an approved device login gives, once.
type DeviceToken struct {
	Token     string    `json:"token" doc:"The token. Send it as Authorization: Bearer <token>. It is not stored and cannot be shown again."`
	ExpiresAt time.Time `json:"expiresAt"`
}

// DeviceView is a device login as the approval page shows it.
type DeviceView struct {
	UserCode   string    `json:"userCode"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	TokenHours int       `json:"tokenHours" doc:"How many hours the token an approval gives lasts"`
	State      string    `json:"state" enum:"pending,approved,denied,spent"`
}

// deviceProblem is a poll's answer while there is no token: a problem with
// the RFC 8628 error name.
type deviceProblem struct {
	*problem.Details
	Code string `json:"error"`
}

func deviceError(name string, code reasons.MessageCode) error {
	return &deviceProblem{Details: problem.New(http.StatusBadRequest, reasons.Msg(code)), Code: name}
}

// IsDeviceDecision is true for the routes that read, approve or deny a
// device login by its user code, which only a signed-in person in the
// browser may use: no token can approve a login and so make itself a new
// token. The path is cleaned first, as IsPlanDecision does.
func IsDeviceDecision(p string) bool {
	rest, ok := strings.CutPrefix(path.Clean("/"+p), deviceRoute+"/")
	return ok && rest != "token"
}

// deviceSessionRequired refuses a request made with an API token, checked
// again here after RequireUser.
func deviceSessionRequired(ctx context.Context) error {
	if viaToken(ctx) {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgDeviceSessionOnly))
	}
	return nil
}

// tokenLifetime is how long the token of an approved login lasts.
func (d *DeviceAPI) tokenLifetime() time.Duration {
	if d.MaxLifetime > 0 && d.MaxLifetime < deviceTokenLifetime {
		return d.MaxLifetime
	}
	return deviceTokenLifetime
}

// newUserCode is userCodeLen letters of userCodeAlphabet, shown as XXXX-XXXX.
func newUserCode() (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < userCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		// Bytes past the last whole multiple of the alphabet are skipped,
		// so every letter is equally likely.
		if int(buf[0]) >= 256-256%len(userCodeAlphabet) {
			continue
		}
		b.WriteByte(userCodeAlphabet[int(buf[0])%len(userCodeAlphabet)])
	}
	s := b.String()
	return s[:4] + "-" + s[4:], nil
}

// normalUserCode is code as stored (XXXX-XXXX), whatever case and dash the
// person typed it with, or false when it cannot be a user code.
func normalUserCode(code string) (string, bool) {
	s := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))
	if len(s) != userCodeLen || strings.Trim(s, userCodeAlphabet) != "" {
		return "", false
	}
	return s[:4] + "-" + s[4:], true
}

func deviceCodeHash(code string) []byte {
	h := sha256.Sum256([]byte(code))
	return h[:]
}

// Register adds the device login routes to api.
func (d *DeviceAPI) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "startDeviceLogin", Method: http.MethodPost, Path: deviceRoute,
		Summary: "Start a sign-in for an agent that a person approves", Tags: []string{"auth"}, Security: NoSession,
		Description: "For a coding agent without a browser. Show the person userCode and verificationURL, then poll " +
			"POST /api/auth/device/token with deviceCode every interval seconds until the person approves or the request expires.",
		Errors: []int{http.StatusTooManyRequests},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body DeviceStart }, error) {
		ip, _ := ctx.Value(clientKey{}).(string)
		if wait, ok := d.Limiter.Take(ratelimit.Bucket{Key: "device:" + ip, Spec: deviceStartLimit}); !ok {
			return nil, problem.New(http.StatusTooManyRequests, reasons.Msg(reasons.MsgDeviceTooManyStarts)).
				WithHeader("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, problem.Internal("device code", err)
		}
		deviceCode := base64.RawURLEncoding.EncodeToString(secret)
		userCode, err := newUserCode()
		if err != nil {
			return nil, problem.Internal("user code", err)
		}
		now := d.Now()
		err = d.DB.DeviceLogins().Create(ctx, store.DeviceLogin{
			UserCode: userCode, CodeHash: deviceCodeHash(deviceCode), CreatedAt: now, ExpiresAt: now.Add(deviceLifetime),
			State: store.DevicePending,
		})
		if err != nil {
			return nil, problem.Internal("start device login", err)
		}
		page := strings.TrimSuffix(d.PublicURL, "/") + "/device"
		return &struct{ Body DeviceStart }{Body: DeviceStart{
			DeviceCode: deviceCode, UserCode: userCode, VerificationURL: page, VerificationURLComplete: page + "?code=" + userCode,
			ExpiresIn: int(deviceLifetime / time.Second), Interval: int(devicePollInterval / time.Second),
		}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "pollDeviceLogin", Method: http.MethodPost, Path: deviceRoute + "/token",
		Summary: "Ask for the token of an agent sign-in", Tags: []string{"auth"}, Security: NoSession,
		Description: "Until there is a token the answer is 400 with error authorization_pending (keep polling), slow_down " +
			"(polled sooner than interval), access_denied or expired_token (stop). Once approved, the next poll gets the " +
			"token, read-write, named Agent login, for 8 hours or api.tokens.maxLifetime when shorter; later polls get expired_token.",
		Errors: []int{http.StatusBadRequest},
	}, func(ctx context.Context, in *struct {
		Body struct {
			DeviceCode string `json:"deviceCode" maxLength:"128"`
		}
	}) (*struct{ Body DeviceToken }, error) {
		l, err := d.DB.DeviceLogins().ByCodeHash(ctx, deviceCodeHash(in.Body.DeviceCode))
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, deviceError(deviceErrExpired, reasons.MsgDeviceExpired)
		case err != nil:
			return nil, problem.Internal("read device login", err)
		}
		now := d.Now()
		if !now.Before(l.ExpiresAt) {
			return nil, deviceError(deviceErrExpired, reasons.MsgDeviceExpired)
		}
		switch l.State {
		case store.DevicePending:
			if err := d.DB.DeviceLogins().Polled(ctx, l.ID, now); err != nil {
				return nil, problem.Internal("record device poll", err)
			}
			if !l.LastPollAt.IsZero() && now.Sub(l.LastPollAt) < devicePollInterval {
				return nil, deviceError(deviceErrSlow, reasons.MsgDeviceSlowDown)
			}
			return nil, deviceError(deviceErrPending, reasons.MsgDevicePending)
		case store.DeviceDenied:
			return nil, deviceError(deviceErrDenied, reasons.MsgDeviceDenied)
		case store.DeviceApproved:
		default:
			return nil, deviceError(deviceErrExpired, reasons.MsgDeviceExpired)
		}
		spent, err := d.DB.DeviceLogins().Spend(ctx, l.ID)
		if err != nil {
			return nil, problem.Internal("spend device login", err)
		}
		if !spent {
			return nil, deviceError(deviceErrExpired, reasons.MsgDeviceExpired)
		}
		expires := now.UTC().Truncate(time.Second).Add(d.tokenLifetime())
		text, _, err := d.Tokens.Create(ctx, model.Actor{Person: l.Owner}, token.Token{
			Name: deviceTokenName, Owner: l.Owner, Scope: token.ScopeReadWrite, ExpiresAt: &expires,
		})
		if err != nil {
			return nil, problem.Internal("create device token", err)
		}
		return &struct{ Body DeviceToken }{Body: DeviceToken{Token: text, ExpiresAt: expires}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getDeviceLogin", Method: http.MethodGet, Path: deviceRoute + "/{userCode}",
		Summary: "An agent sign-in waiting for approval", Tags: []string{"auth"}, Security: SessionOnly,
		Description: "Needs a browser session: no API token can read, approve or deny an agent's sign-in.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone},
	}, func(ctx context.Context, in *struct {
		UserCode string `path:"userCode" maxLength:"16"`
	}) (*struct{ Body DeviceView }, error) {
		l, err := d.live(ctx, in.UserCode)
		if err != nil {
			return nil, err
		}
		return &struct{ Body DeviceView }{Body: DeviceView{
			UserCode: l.UserCode, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt,
			TokenHours: int(d.tokenLifetime() / time.Hour), State: l.State,
		}}, nil
	})

	for _, decision := range []struct{ verb, id, summary, state string }{
		{"approve", "approveDeviceLogin", "Approve an agent sign-in: the agent gets a token that acts as you", store.DeviceApproved},
		{"deny", "denyDeviceLogin", "Deny an agent sign-in", store.DeviceDenied},
	} {
		huma.Register(api, huma.Operation{
			OperationID: decision.id, Method: http.MethodPost, Path: deviceRoute + "/{userCode}/" + decision.verb,
			Summary: decision.summary, Tags: []string{"auth"}, Security: SessionOnly,
			Description:   "A request is approved or denied once. Needs a browser session.",
			DefaultStatus: http.StatusNoContent,
			Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone},
		}, func(ctx context.Context, in *struct {
			UserCode string `path:"userCode" maxLength:"16"`
		}) (*struct{}, error) {
			l, err := d.live(ctx, in.UserCode)
			if err != nil {
				return nil, err
			}
			decided, err := d.DB.DeviceLogins().Decide(ctx, Actor(ctx), l.ID, decision.state, User(ctx).ID)
			switch {
			case err != nil:
				return nil, problem.Internal("decide device login", err)
			case !decided:
				return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgDeviceDecided))
			}
			return nil, nil
		})
	}
}

// live is the unexpired device login with this user code, for a person in
// the browser: 404 when there is none, 410 when it expired.
func (d *DeviceAPI) live(ctx context.Context, userCode string) (store.DeviceLogin, error) {
	if err := deviceSessionRequired(ctx); err != nil {
		return store.DeviceLogin{}, err
	}
	code, ok := normalUserCode(userCode)
	if !ok {
		return store.DeviceLogin{}, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgDeviceUnknown))
	}
	l, err := d.DB.DeviceLogins().ByUserCode(ctx, code)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.DeviceLogin{}, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgDeviceUnknown))
	case err != nil:
		return store.DeviceLogin{}, problem.Internal("read device login", err)
	case !d.Now().Before(l.ExpiresAt):
		return store.DeviceLogin{}, problem.New(http.StatusGone, reasons.Msg(reasons.MsgDeviceExpired))
	}
	return l, nil
}

// RunDeviceJanitor deletes expired device logins every interval until ctx
// is done. Every gateway replica runs it; deletes are idempotent.
func RunDeviceJanitor(ctx context.Context, db *store.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := db.DeviceLogins().Sweep(ctx, time.Now()); err != nil {
				slog.Error("sweep device logins", "err", err)
			}
		}
	}
}
