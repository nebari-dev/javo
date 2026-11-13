//go:build linux || darwin

package process

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"path"
	"strconv"
	"strings"

	"github.com/nebari-dev/javo/internal/workload"
)

// Addr is the loopback address the workload serves on. The port is the
// workload's own, picked the first time it is asked for, whatever port
// the caller names.
func (e *engine) Addr(_ context.Context, ref workload.Ref, _ int32) (string, error) {
	port, err := e.port(ref)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
}

// port is the workload's port, read from its folder or picked and written
// there.
func (e *engine) port(ref workload.Ref) (int, error) {
	wd, err := folder(ref)
	if err != nil {
		return 0, err
	}
	e.ports.Lock()
	defer e.ports.Unlock()
	file := path.Join(wd, "port")
	if port, ok := e.readPort(file); ok {
		return port, nil
	}
	taken := map[int]bool{}
	files, _ := fs.Glob(e.root.FS(), "w/*/*/port")
	for _, f := range files {
		if port, ok := e.readPort(f); ok {
			taken[port] = true
		}
	}
	for range 20 {
		// The kernel names a port that is free now.
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := lis.Addr().(*net.TCPAddr).Port
		_ = lis.Close()
		if taken[port] {
			continue
		}
		if err := e.root.MkdirAll(wd, 0o700); err != nil {
			return 0, err
		}
		return port, e.root.WriteFile(file, []byte(strconv.Itoa(port)+"\n"), 0o600)
	}
	return 0, errors.New("process executor: no free port")
}

func (e *engine) readPort(file string) (int, bool) {
	b, err := e.root.ReadFile(file)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return port, err == nil && port > 0 && port < 65536
}
