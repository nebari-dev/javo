package netprobe

import (
	"context"
	"net/http"
	"time"
)

// Outcome is what the dialer pod saw: whether the control URL (the gateway,
// which the cluster must let it reach) and the target URL (the listener in
// the apps namespace, which the cluster must refuse) answered. The dialer
// writes it as its termination message.
//
// The Docker probe's dialer also says how each address it was told to
// connect to answered (TCP, see Connect) and which names resolved
// (Resolved): on Docker more than one listener must be out of reach.
type Outcome struct {
	Control  bool              `json:"control"`
	Target   bool              `json:"target"`
	TCP      map[string]string `json:"tcp,omitempty"`
	Resolved []string          `json:"resolved,omitempty"`
}

// Listener answers every request with 200. It runs in the listener pod.
func Listener() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// Dial requests both URLs, each within timeout, and reports which gave a
// 2xx answer.
func Dial(ctx context.Context, control, target string, timeout time.Duration) Outcome {
	return Outcome{Control: reaches(ctx, control, timeout), Target: reaches(ctx, target, timeout)}
}

func reaches(ctx context.Context, url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
