package apps_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// heldLogs is a followed log that stays open, writing nothing, until the
// request ends: each open stream holds a kubelet connection.
type heldLogs struct{}

func (heldLogs) Stream(ctx context.Context, _, _ string, _ *corev1.PodLogOptions) (io.ReadCloser, error) {
	r, w := io.Pipe()
	go func() {
		<-ctx.Done()
		w.CloseWithError(ctx.Err())
	}()
	return r, nil
}

// open starts a GET as the cookie's person and returns its response, which
// stays open until cancel.
func open(t *testing.T, srv *httptest.Server, path string, c *http.Cookie) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
	req.AddCookie(c)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); res.Body.Close() })
	return res, cancel
}

// One person may follow only streamsPerPerson logs at once, of every kind
// together (each holds a kubelet connection); another person is not held to the first person's count, and a
// closed stream frees its place.
func TestOnePersonMayFollowOnlySoManyLogsAtOnce(t *testing.T) {
	t.Parallel()
	alice, bob := "alice-aaaaaaaa", apitest.BobID
	f := apitest.New(t, apitest.Options{Logs: heldLogs{}, Limits: config.LimitsCfg{StreamsPerPerson: 2},
		People: []apitest.TestPerson{apitest.SignedIn("bob", time.Time{})}},
		gatewaytest.Workspace("app", alice, "App"), gatewaytest.Workspace("app", bob, "App"),
		gatewaytest.Template("streamlit", "streamlit"),
		apitest.Running(gatewaytest.LivePod(alice, "app", "uid-a"), 0), apitest.Running(gatewaytest.LivePod(bob, "app", "uid-b"), 0))
	srv := httptest.NewServer(f.Srv)
	t.Cleanup(srv.Close) // after the streams' cleanups close them
	aliceC, bobC := f.Session(alice), f.Session(apitest.BobID)

	first, closeFirst := open(t, srv, "/api/workspaces/app/logs/stream", aliceC)
	second, _ := open(t, srv, "/api/workspaces/app/logs/stream", aliceC)
	if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusOK {
		t.Fatalf("first two streams: %d, %d", first.StatusCode, second.StatusCode)
	}
	// Every kind of followed log counts: app, startup step and job run logs.
	for _, path := range []string{"/api/workspaces/app/logs/stream", "/api/workspaces/app/steps/Started/log", "/api/jobs/nightly/runs/r1/log"} {
		res, _ := open(t, srv, path, aliceC)
		if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
			t.Fatalf("third stream %s: %d", path, res.StatusCode)
		}
		var p struct{ Title string }
		if err := json.NewDecoder(res.Body).Decode(&p); err != nil || p.Title != reasons.Msg(reasons.MsgTooManyStreams, 2) {
			t.Fatalf("third stream title %q, %v", p.Title, err)
		}
	}
	if res, _ := open(t, srv, "/api/workspaces/app/logs/stream", bobC); res.StatusCode != http.StatusOK {
		t.Fatalf("bob's stream: %d", res.StatusCode)
	}
	closeFirst()
	first.Body.Close()
	var again *http.Response
	for range 50 { // the server notices the closed stream a moment later
		if again, _ = open(t, srv, "/api/workspaces/app/logs/stream", aliceC); again.StatusCode == http.StatusOK {
			break
		}
	}
	if again.StatusCode != http.StatusOK {
		t.Fatalf("stream after one closed: %d", again.StatusCode)
	}
}

// bigPNG is a 3000 by 3000 picture: a few MB of compressed PNG that
// decodes to 36 MB of pixels.
func bigPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 3000, 3000))
	for i := range img.Pix {
		img.Pix[i] = byte(i % 251)
	}
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The gateway decodes at most thumbnailDecodes pictures at once; uploads
// past that are told to retry instead of each decoding into memory.
func TestPictureDecodesAreBoundedAcrossTheGateway(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts, Thumbnails: true,
		Limits: config.LimitsCfg{ThumbnailDecodes: 1}})
	alice := f.Session(apitest.AliceID)
	pic := bigPNG(t)
	const n = 8
	codes := make(chan *httptest.ResponseRecorder, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			codes <- f.PutThumb(apitest.ThumbPath, "image/png", pic, alice)
		}()
	}
	close(start)
	var ok, busy int
	for range n {
		w := <-codes
		switch {
		case w.Code == http.StatusOK:
			ok++
		case w.Code == http.StatusServiceUnavailable && w.Header().Get("Retry-After") != "" &&
			apitest.DecodeFilesBody(t, w)["title"] == reasons.Msg(reasons.MsgThumbnailBusy):
			busy++
		default:
			t.Fatalf("upload: %d %s", w.Code, w.Body)
		}
	}
	if ok == 0 || busy == 0 {
		t.Fatalf("%d decoded, %d told to retry; want both", ok, busy)
	}
}
