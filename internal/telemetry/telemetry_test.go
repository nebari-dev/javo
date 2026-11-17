package telemetry_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/telemetry"
)

// A trace the caller started (W3C traceparent): its id and the id of the
// caller's span.
const (
	callerTrace       = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan        = "00f067aa0ba902b7"
	callerTraceparent = "00-" + callerTrace + "-" + callerSpan + "-01"
)

// lines are the JSON records written to buf, one per line.
func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("not a JSON record: %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

// A line logged while serving a request names the trace the request came
// in with, so the logs of one request can be found across services.
func TestALogLineOfARequestCarriesItsTrace(t *testing.T) {
	var buf bytes.Buffer
	log, err := telemetry.NewLogger(&buf, "json", slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	h := telemetry.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.InfoContext(r.Context(), "app started", "app", "sales")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces", nil)
	req.Header.Set("traceparent", callerTraceparent)
	h.ServeHTTP(httptest.NewRecorder(), req)
	log.Info("no request")

	got := lines(t, &buf)
	if len(got) != 2 {
		t.Fatalf("%d records: %v", len(got), got)
	}
	if got[0]["msg"] != "app started" || got[0]["app"] != "sales" || got[0]["level"] != "INFO" {
		t.Fatalf("record: %v", got[0])
	}
	if got[0]["trace_id"] != callerTrace {
		t.Fatalf("trace_id %v, want the caller's %s", got[0]["trace_id"], callerTrace)
	}
	if _, ok := got[0]["span_id"].(string); !ok {
		t.Fatalf("no span_id: %v", got[0])
	}
	if _, ok := got[1]["trace_id"]; ok {
		t.Fatalf("a line outside any request has a trace: %v", got[1])
	}
}

// collector stands in for an OpenTelemetry collector: it records the OTLP
// paths it is sent to.
func collector(t *testing.T) (url string, got func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(seen)
	}
}

// With the standard OTEL_EXPORTER_OTLP_ENDPOINT set, a request's span and
// the HTTP server metrics are sent there over OTLP; Stop sends what is
// left.
func TestStartSendsTracesAndMetricsToTheCollectorTheEnvironmentNames(t *testing.T) {
	url, got := collector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
	stop, err := telemetry.Start(t.Context(), "javo", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	h := telemetry.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if err := stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if seen := got(); seen["/v1/traces"] == 0 || seen["/v1/metrics"] == 0 {
		t.Fatalf("the collector was sent %v", seen)
	}
}

// With no OTLP endpoint set nothing is sent and nothing fails: telemetry is
// off unless someone names a collector.
func TestStartWithNoCollectorSendsNothing(t *testing.T) {
	_, got := collector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	stop, err := telemetry.Start(t.Context(), "javo", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Handler(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err := stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if seen := got(); len(seen) != 0 {
		t.Fatalf("sent %v", seen)
	}
}
