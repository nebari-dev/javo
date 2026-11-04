// Package streams caps how many followed logs one person holds open in the
// gateway at once. Each holds a goroutine and a connection to the kubelet
// for as long as it is open.
package streams

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Limit is the per-person cap shared by every streaming operation.
type Limit struct{ open *ratelimit.Concurrent }

// New allows each person perPerson open streams, of all kinds together.
func New(perPerson int) *Limit { return &Limit{open: ratelimit.NewConcurrent(perPerson)} }

// retryAfter is the Retry-After, in seconds, of a refused stream.
const retryAfter = "5"

// Op returns op with a middleware that holds one of the caller's places for
// as long as the operation runs (a stream runs until it ends), and answers
// 429 when they hold every place already.
func (l *Limit) Op(api huma.API, op huma.Operation) huma.Operation {
	op.Errors = append(op.Errors, http.StatusTooManyRequests)
	op.Middlewares = append(op.Middlewares, func(hctx huma.Context, next func(huma.Context)) {
		release, ok := l.open.Acquire(authn.User(hctx.Context()).ID)
		if !ok {
			hctx.SetHeader("Content-Type", "application/problem+json")
			hctx.SetHeader("Retry-After", retryAfter)
			hctx.SetStatus(http.StatusTooManyRequests)
			_ = api.Marshal(hctx.BodyWriter(), "application/problem+json",
				problem.New(http.StatusTooManyRequests, reasons.Msg(reasons.MsgTooManyStreams, l.open.Max())))
			return
		}
		defer release()
		next(hctx)
	})
	return op
}
