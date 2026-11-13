package docker

import (
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// frame is one frame of the daemon's log stream.
func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func TestLogsJoinOutputAndErrorsInOrder(t *testing.T) {
	big := strings.Repeat("x", 70_000) + "\n"
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame(1, "out 1\n"))
		w.Write(frame(2, "err 1\n"))
		w.Write(frame(1, ""))
		w.Write(frame(1, big))
		w.Write(frame(2, "err 2\n"))
	})
	rc, err := c.ContainerLogs(context.Background(), "abc", LogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	// One byte at a time: a frame split across reads must still be whole.
	got, err := io.ReadAll(iotest.OneByteReader(rc))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "out 1\nerr 1\n"+big+"err 2\n" {
		t.Fatalf("got %d bytes: %.60q", len(got), got)
	}
}

func TestLogsRefuseAStreamThatIsNotFramed(t *testing.T) {
	for name, raw := range map[string][]byte{
		"plain text":   []byte("just some text, not frames\n"),
		"cut header":   frame(1, "ok\n")[:4],
		"cut payload":  frame(1, "0123456789")[:12],
		"bad stream":   frame(9, "x"),
		"after a good": append(frame(1, "ok\n"), "garbage!!"...),
	} {
		c := fake(t, func(w http.ResponseWriter, r *http.Request) { w.Write(raw) })
		rc, err := c.ContainerLogs(context.Background(), "abc", LogOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(rc); err == nil {
			t.Errorf("%s: no error", name)
		}
		rc.Close()
	}
}

func TestLogOptionsReachTheDaemon(t *testing.T) {
	var query string
	c := fake(t, func(w http.ResponseWriter, r *http.Request) { query = r.URL.RawQuery })
	since := time.Unix(1700000000, 5)
	rc, err := c.ContainerLogs(context.Background(), "abc", LogOptions{Follow: true, Tail: 20, Since: since, Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if query != "follow=1&since=1700000000.000000005&stderr=1&stdout=1&tail=20&timestamps=1" {
		t.Fatalf("query = %s", query)
	}
	rc, _ = c.ContainerLogs(context.Background(), "abc", LogOptions{})
	rc.Close()
	if query != "stderr=1&stdout=1" {
		t.Fatalf("query = %s", query)
	}
}

func TestFollowedLogsEndWhenClosed(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(frame(1, "first\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	rc, err := c.ContainerLogs(context.Background(), "abc", LogOptions{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := rc.Read(buf)
	if err != nil || string(buf[:n]) != "first\n" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
	rc.Close()
	if _, err := rc.Read(buf); err == nil {
		t.Fatal("read after close succeeded")
	}
}
