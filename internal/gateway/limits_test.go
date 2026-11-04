package gateway_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

// A client that sends a request body slowly is cut off after
// bodyReadTimeout instead of holding a connection forever, on the public
// sign-in route too.
func TestASlowRequestBodyIsCutOff(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{APILimits: config.APILimitsCfg{BodyReadTimeout: metav1.Duration{Duration: 200 * time.Millisecond}}})
	srv := httptest.NewServer(f.Srv)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "POST /api/auth/local/login HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"username\":")
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	// The server answers or closes; either way the read returns before the
	// client's own deadline.
	_, err = io.ReadAll(conn)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the gateway still waits for the body")
	}
}

// The body deadline is for reading the body only: a log stream, which has
// no body, keeps flowing long after it.
func TestALogStreamOutlivesTheBodyDeadline(t *testing.T) {
	t.Parallel()
	const timeout = 100 * time.Millisecond
	logs := apitest.NewPipeLogs()
	f := apitest.New(t, apitest.Options{Logs: logs, APILimits: config.APILimitsCfg{BodyReadTimeout: metav1.Duration{Duration: timeout}}},
		gatewaytest.Workspace("app", "alice-aaaaaaaa", "App"),
		gatewaytest.Template("streamlit", "streamlit"), apitest.Running(gatewaytest.LivePod("alice-aaaaaaaa", "app", "uid-live"), 0))
	srv := httptest.NewServer(f.Srv)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/workspaces/app/logs/stream", nil)
	req.AddCookie(f.Session("alice-aaaaaaaa"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	<-logs.Opts
	time.Sleep(5 * timeout)
	select {
	case <-logs.Closed:
		t.Fatal("the stream ended at the body deadline")
	default:
	}
	go io.WriteString(logs.W, "2026-10-30T12:00:01Z still here\n")
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "still here") {
			return
		}
	}
	t.Fatalf("stream ended: %v", sc.Err())
}
