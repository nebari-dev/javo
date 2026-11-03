package netprobe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDialReportsWhichURLsAnswer(t *testing.T) {
	up := httptest.NewServer(Listener())
	defer up.Close()
	down := httptest.NewServer(Listener())
	down.Close()

	got := Dial(context.Background(), up.URL, down.URL, time.Second)
	if !got.Control || got.Target {
		t.Fatalf("control up, target down: got %+v", got)
	}
	got = Dial(context.Background(), up.URL, up.URL, time.Second)
	if !got.Control || !got.Target {
		t.Fatalf("both up: got %+v", got)
	}
}

func TestDialTreatsNon2xxAsUnreachable(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer bad.Close()
	if got := Dial(context.Background(), bad.URL, bad.URL, time.Second); got.Control || got.Target {
		t.Fatalf("502 counted as reachable: %+v", got)
	}
}
