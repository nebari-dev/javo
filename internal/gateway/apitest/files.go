package apitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	pathpkg "path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
)

type StubFilesLister struct {
	// Mu guards every field: the gateway refreshes listings in the background.
	Mu        sync.Mutex
	Entries   []files.Entry
	Truncated bool
	Err       error
	Calls     int
	User      string
	Path      string
	Group     string // dir of the last ListGroup call
	Released  []string
	Touched   []string
	// StartingFor answers ErrHomeStarting to that many List calls first.
	StartingFor int

	// Open serves content as the file named in the path, with size set to
	// the file's listed size (len(content) when zero).
	Content string
	Size    int64
	Opened  []string
	// Body, when set, replaces the content reader.
	Body io.ReadCloser
	// ModTime is the opened file's modification time.
	ModTime time.Time

	// Save records each call as "user:path:version:data" and answers
	// SaveErr, or the saved file with SavedAt as its modification time.
	Saved   []string
	SaveErr error
	SavedAt time.Time

	// Unpack reads the whole archive, records each call as
	// "user:path:limit:replace:what it read" and answers UnpackErr, or the
	// folder with one file of the bytes read.
	Unpacks   []string
	UnpackErr error
}

func (f *StubFilesLister) Unpack(_ context.Context, userID, path string, archive io.Reader, limit int64, replace bool) (files.Unpacked, error) {
	data, err := io.ReadAll(archive)
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Unpacks = append(f.Unpacks, fmt.Sprintf("%s:%s:%d:%t:%s", userID, path, limit, replace, data))
	if err != nil {
		return files.Unpacked{}, err
	}
	if f.UnpackErr != nil {
		return files.Unpacked{}, f.UnpackErr
	}
	return files.Unpacked{Path: path, Files: 1, Bytes: int64(len(data)), Replaced: replace}, nil
}

func (f *StubFilesLister) Save(_ context.Context, userID, path string, data []byte, version string) (files.FileInfo, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Saved = append(f.Saved, userID+":"+path+":"+version+":"+string(data))
	if f.SaveErr != nil {
		return files.FileInfo{}, f.SaveErr
	}
	return files.FileInfo{Name: pathpkg.Base(path), Size: int64(len(data)), ModTime: f.SavedAt}, nil
}

func (f *StubFilesLister) Open(_ context.Context, userID, path string) (io.ReadCloser, files.FileInfo, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Opened = append(f.Opened, userID+":"+path)
	if f.Err != nil {
		return nil, files.FileInfo{}, f.Err
	}
	size := f.Size
	if size == 0 {
		size = int64(len(f.Content))
	}
	info := files.FileInfo{Name: pathpkg.Base(path), Size: size, ModTime: f.ModTime}
	if f.Body != nil {
		return f.Body, info, nil
	}
	return io.NopCloser(strings.NewReader(f.Content)), info, nil
}

func (f *StubFilesLister) Release(_ context.Context, userID string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Released = append(f.Released, userID)
	return nil
}

func (f *StubFilesLister) Touch(userID string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Touched = append(f.Touched, userID)
}

func (f *StubFilesLister) List(_ context.Context, userID, path string) (files.Listing, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Calls++
	f.User, f.Path = userID, path
	if f.StartingFor > 0 {
		f.StartingFor--
		return files.Listing{}, files.ErrHomeStarting
	}
	return files.Listing{Entries: f.Entries, Truncated: f.Truncated}, f.Err
}

func (f *StubFilesLister) ListGroup(_ context.Context, userID, dir, path string) (files.Listing, error) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Calls++
	f.User, f.Group, f.Path = userID, dir, path
	return files.Listing{Entries: f.Entries, Truncated: f.Truncated}, f.Err
}

func GroupFilesFixture(t *testing.T, l *StubFilesLister, folders ...any) *Fixture {
	t.Helper()
	return New(t, Options{Files: l, Groups: StubGroups{"/org/data-science"}}, folders...)
}

// GetFiles sends one request through the whole gateway, signed in as
// alice unless anonymous is set.
func GetFiles(t *testing.T, l *StubFilesLister, anonymous bool, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	f := New(t, Options{Files: l})
	var c *http.Cookie
	if !anonymous {
		c = f.Session("alice")
	}
	return f.Do(method, target, "", c)
}

func DecodeFilesBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return m
}

type BrokenBody struct{ Sent bool }

func (b *BrokenBody) Read(p []byte) (int, error) {
	if b.Sent {
		return 0, errors.New("pod released")
	}
	b.Sent = true
	return copy(p, "partial"), nil
}

func (b *BrokenBody) Close() error { return nil }
