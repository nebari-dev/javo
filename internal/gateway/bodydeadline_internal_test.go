package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A request whose body arrived in time may take as long as it needs to
// answer: the deadline ends once the body is read.
func TestBodyDeadlineEndsWhenTheBodyIsRead(t *testing.T) {
	t.Parallel()
	const timeout = 100 * time.Millisecond
	srv := httptest.NewServer(bodyDeadline(timeout, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		time.Sleep(5 * timeout)
		if r.Context().Err() != nil {
			http.Error(w, "request cancelled", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, "done")
	})))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/api/x", "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(b) != "done" {
		t.Fatalf("got %d %q", res.StatusCode, b)
	}
}
