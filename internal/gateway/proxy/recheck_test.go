package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// An open websocket is checked again every RecheckEvery: it keeps working
// while the person may open the app, and is closed once they may not.
func TestOpenWebsocketClosesWhenAccessEnds(t *testing.T) {
	t.Parallel()
	f := setup(t, http.HandlerFunc(gatewaytest.EchoBackend))
	u, _ := url.Parse(f.backend.URL)
	const every = 20 * time.Millisecond
	f.proxy = New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity, RecheckEvery: every,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) { return Backend{URL: u}, nil }})
	srv := httptest.NewServer(f.proxy)
	defer srv.Close()
	cookie := f.login(t, "alice-aaaaaaaa", "alice")

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /u/"+f.aliceSl+"/nb1/ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nCookie: "+cookie.String()+"\r\n\r\n")
	br := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	echo := func() error {
		if _, err := io.WriteString(conn, "ping"); err != nil {
			return err
		}
		buf := make([]byte, 4)
		_, err := io.ReadFull(br, buf)
		return err
	}
	// Several rechecks pass while the session lives.
	time.Sleep(5 * every)
	if err := echo(); err != nil {
		t.Fatalf("websocket closed while access holds: %v", err)
	}

	if err := f.store.Delete(context.Background(), cookie.Value); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err = br.Read(buf)
	if errors.Is(err, os.ErrDeadlineExceeded) || err == nil {
		t.Fatalf("websocket still open after the session ended: %v", err)
	}
}

// A response that stays open (server-sent events, a long download) is
// checked again every RecheckEvery like a websocket, and ends once the
// person may no longer open the app.
func TestOpenStreamingResponseEndsWhenAccessEnds(t *testing.T) {
	t.Parallel()
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			if _, err := io.WriteString(w, "data: tick\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	f := setup(t, backend)
	u, _ := url.Parse(f.backend.URL)
	const every = 20 * time.Millisecond
	f.proxy = New(Config{Store: f.db, Sessions: f.store, Revalidator: confirming(f.store), Exposure: gatewaytest.OneHostExposure, Activity: f.activity, RecheckEvery: every,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) { return Backend{URL: u}, nil }})
	srv := httptest.NewServer(f.proxy)
	defer srv.Close()
	cookie := f.login(t, "alice-aaaaaaaa", "alice")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/u/"+f.aliceSl+"/nb1/events", nil)
	req.Host = "x"
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	read := make(chan error, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		// Several rechecks pass while the session lives.
		deadline := time.Now().Add(5 * every)
		for time.Now().Before(deadline) {
			if _, err := br.ReadString('\n'); err != nil {
				read <- errors.New("stream ended while access holds: " + err.Error())
				return
			}
		}
		if err := f.store.Delete(context.Background(), cookie.Value); err != nil {
			read <- err
			return
		}
		_, err := io.Copy(io.Discard, br)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil && err.Error() != "unexpected EOF" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after the session ended")
	}
}
