//go:build linux || darwin

package process

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// stampLayout is the time each log line starts with: RFC 3339 with
	// nanoseconds at a fixed width, in UTC.
	stampLayout = "2006-01-02T15:04:05.000000000Z"
	// maxLine is the most of one line kept together; a longer one is cut
	// into several.
	maxLine = 64 << 10
	// followEvery is how often a followed log is looked at for new lines.
	followEvery = 100 * time.Millisecond
)

// newLog opens an empty log file for the unit of the workload in wd; the
// log of its last run becomes the previous one.
func (e *engine) newLog(wd, unit string) (*os.File, error) {
	file := path.Join(wd, "logs", unit+".log")
	if err := e.root.Rename(file, path.Join(wd, "logs", unit+".previous.log")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return e.root.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// stamp copies r to w line by line, each line after the time it was read,
// until r ends.
func stamp(w io.Writer, r io.Reader) {
	br := bufio.NewReaderSize(r, maxLine)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			out := append([]byte(time.Now().UTC().Format(stampLayout)+" "), line...)
			if out[len(out)-1] != '\n' {
				out = append(out, '\n')
			}
			if _, werr := w.Write(out); werr != nil {
				// Keep reading: a unit must never block on its output.
				_, _ = io.Copy(io.Discard, br)
				return
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return
		}
	}
}

// Logs streams the log of a unit of the workload: the part o picks, then,
// when following, each new line until ctx ends or the stream is closed.
func (e *engine) Logs(ctx context.Context, ref workload.Ref, o executor.LogOptions) (io.ReadCloser, error) {
	wd, err := folder(ref)
	if err != nil {
		return nil, err
	}
	name := o.Unit
	if name == "" {
		name = workload.MainUnit
	}
	if !plain(name) {
		return nil, os.ErrNotExist
	}
	if o.Previous {
		name += ".previous"
	}
	f, err := e.root.Open(path.Join(wd, "logs", name+".log"))
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		defer f.Close()
		br := bufio.NewReader(f)
		// rest is the start of a line whose end is not written yet.
		var rest string
		read := func() (lines []string) {
			for {
				s, err := br.ReadString('\n')
				if err != nil {
					rest += s
					return lines
				}
				when, text, _ := strings.Cut(rest+s, " ")
				rest = ""
				if at, err := time.Parse(stampLayout, when); err == nil && at.Before(o.Since) {
					continue
				}
				if o.Timestamps {
					text = when + " " + text
				}
				lines = append(lines, text)
			}
		}
		lines := read()
		if o.Tail > 0 && int64(len(lines)) > o.Tail {
			lines = lines[int64(len(lines))-o.Tail:]
		}
		for {
			if _, err := io.WriteString(pw, strings.Join(lines, "")); err != nil {
				return
			}
			if !o.Follow {
				_ = pw.Close()
				return
			}
			select {
			case <-ctx.Done():
				_ = pw.CloseWithError(ctx.Err())
				return
			case <-time.After(followEvery):
			}
			lines = read()
		}
	}()
	return pr, nil
}
