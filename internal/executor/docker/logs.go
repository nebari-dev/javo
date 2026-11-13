package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

// LogOptions picks the part of a container's output to read.
type LogOptions struct {
	// Follow keeps the stream open for new lines until the container ends
	// or the stream is closed.
	Follow bool
	// Tail limits the stream to the last lines; zero is all of them.
	Tail int64
	// Since leaves out lines printed before it; zero is all of them.
	Since time.Time
	// Timestamps starts each line with when it was printed (RFC 3339).
	Timestamps bool
}

// ContainerLogs streams what a container printed, standard output and
// standard error together in the order the daemon recorded them. The
// caller closes the stream.
func (c *Client) ContainerLogs(ctx context.Context, id string, o LogOptions) (io.ReadCloser, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	if o.Follow {
		q.Set("follow", "1")
	}
	if o.Tail > 0 {
		q.Set("tail", strconv.FormatInt(o.Tail, 10))
	}
	if !o.Since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", o.Since.Unix(), o.Since.Nanosecond()))
	}
	if o.Timestamps {
		q.Set("timestamps", "1")
	}
	resp, err := c.do(ctx, request{method: "GET", path: "/containers/" + url.PathEscape(id) + "/logs", query: q})
	if err != nil {
		return nil, err
	}
	return &demux{src: bufio.NewReader(resp.Body), body: resp.Body}, nil
}

// demux reads the daemon's log stream of a container without a terminal
// (Javo starts none with one): frames of an 8 byte header, which names the
// stream (0 input, 1 output, 2 errors) and, in its last 4 bytes, how long
// the frame is, followed by that many bytes of output.
type demux struct {
	src  *bufio.Reader
	body io.Closer
	// left is how much of the current frame is not read yet.
	left uint32
}

func (d *demux) Read(p []byte) (int, error) {
	for d.left == 0 {
		var header [8]byte
		if _, err := io.ReadFull(d.src, header[:]); err != nil {
			if err == io.ErrUnexpectedEOF {
				return 0, fmt.Errorf("docker: the log stream ended inside a frame header")
			}
			return 0, err
		}
		if header[0] > 2 || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return 0, fmt.Errorf("docker: the log stream is not in frames (header % x)", header)
		}
		d.left = binary.BigEndian.Uint32(header[4:])
	}
	if uint32(len(p)) > d.left {
		p = p[:d.left]
	}
	n, err := d.src.Read(p)
	d.left -= uint32(n)
	if err == io.EOF && d.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	if n > 0 {
		// The error, if any, comes back on the next Read.
		return n, nil
	}
	return 0, err
}

func (d *demux) Close() error { return d.body.Close() }
