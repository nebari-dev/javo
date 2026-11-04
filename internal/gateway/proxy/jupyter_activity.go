package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
)

// jupyterProbe asks the Jupyter server behind b when it was last used, by
// its own account: /api/status last_activity (API requests, kernel output,
// terminal use), or now while a kernel runs code, which can be silent for
// hours. Neither request counts as use to Jupyter (status never does; the
// kernel list is asked with no_track_activity). An open but unused
// JupyterLab tab only polls, and stops polling when hidden.
// probeReplyLimit bounds what the probe reads from the app's server, which
// its owner controls; a kernel list or status is far smaller.
const probeReplyLimit = 1 << 20

// The probe never follows a redirect: the app's owner controls the reply, and
// a Location would send the gateway, with the template's proxy headers, to
// any address it can reach (cloud metadata, in-cluster services). A redirect
// is a non-200 answer like any other.
func jupyterProbe(client *http.Client, b Backend, now func() time.Time) activity.Probe {
	noRedirects := *client
	noRedirects.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client = &noRedirects
	api := *b.URL
	if !b.StripPrefix {
		api.Path = b.Prefix
	}
	api.Path += "/api/"
	get := func(ctx context.Context, path string, into any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.String()+path, nil)
		if err != nil {
			return err
		}
		for k, v := range b.Headers {
			req.Header[k] = v
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: %s", req.URL.Path, res.Status)
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, probeReplyLimit+1))
		if err != nil {
			return err
		}
		if len(body) > probeReplyLimit {
			return fmt.Errorf("GET %s: reply over %d bytes", req.URL.Path, probeReplyLimit)
		}
		return json.Unmarshal(body, into)
	}
	return func(ctx context.Context) (time.Time, error) {
		var kernels []struct {
			State string `json:"execution_state"`
		}
		if err := get(ctx, "kernels?no_track_activity=1", &kernels); err != nil {
			return time.Time{}, err
		}
		for _, k := range kernels {
			if k.State == "busy" {
				return now(), nil
			}
		}
		var status struct {
			LastActivity time.Time `json:"last_activity"`
		}
		if err := get(ctx, "status", &status); err != nil {
			return time.Time{}, err
		}
		return status.LastActivity, nil
	}
}
