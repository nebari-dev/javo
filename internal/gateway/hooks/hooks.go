// Package hooks receives code hosts' webhook deliveries for app previews.
package hooks

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// hookLimit bounds webhook deliveries per client address. Looser than a
// login: a code host sends from a few addresses for many repositories.
var hookLimit = ratelimit.Spec{Every: 500 * time.Millisecond, Burst: 60}

// maxHookBody is the largest delivery read (1 MiB).
const maxHookBody = 1 << 20

// Route is where a code host delivers: its registry id, then the random
// address SetHook made, so the path names neither owner nor app.
const Route = "/hooks/{host}/{hook}"

// Receiver receives a code host's webhook deliveries. A delivery is only a
// trigger: once its signature checks out it wakes the preview reconciler,
// which lists the pull requests again; nothing in the body is read as state.
// It sits outside /api, so no session, origin or token check applies; the
// signature is the credential.
type Receiver struct {
	DB      *store.Store
	Hosts   *registry.Registry[codehost.Host]
	Trusted []netip.Prefix
	Limiter *ratelimit.Buckets
	Now     func() time.Time
}

// ServeHTTP checks one delivery's rate and signature and records it for
// the app's previews.
func (h *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Every delivery counts, right or wrong, before any database work.
	if wait, ok := h.Limiter.Take(ratelimit.Bucket{Key: ratelimit.ClientIP(r, h.Trusted), Spec: hookLimit}); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		problem.Write(w, http.StatusTooManyRequests, reasons.Msg(reasons.MsgWebhookRateLimited))
		return
	}
	host, ok := h.Hosts.Get(r.PathValue("host"))
	if !ok {
		problem.Write(w, http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
		return
	}
	if r.ContentLength > maxHookBody {
		problem.Write(w, http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgWebhookTooLarge))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBody)
	hookID := r.PathValue("hook")
	_, secret, err := h.DB.Previews().ByHook(r.Context(), hookID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem.Write(w, http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
		return
	case err != nil:
		slog.Error("read the webhook of an app", "err", err)
		problem.Write(w, http.StatusInternalServerError, reasons.Msg(reasons.MsgServerError))
		return
	}
	event, err := host.VerifyWebhook(r, secret)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		problem.Write(w, http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgWebhookTooLarge))
		return
	case err != nil:
		problem.Write(w, http.StatusUnauthorized, reasons.Msg(reasons.MsgWebhookSignature))
		return
	}
	// Only these can change which pull requests are open or prove the address
	// works; any other signed event is accepted and ignored. (GitHub's names:
	// a second code host needs its own.)
	if event != "pull_request" && event != "ping" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch err := h.DB.Previews().Poke(r.Context(), hookID, h.Now()); {
	case errors.Is(err, store.ErrNotFound): // the owner made a new address meanwhile
		problem.Write(w, http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
	case err != nil:
		slog.Error("record a webhook delivery", "err", err)
		problem.Write(w, http.StatusInternalServerError, reasons.Msg(reasons.MsgServerError))
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}
