package apitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
)

// TestClock is a clock a test moves by hand.
type TestClock struct {
	Mu sync.Mutex
	T  time.Time
}

func (c *TestClock) Now() time.Time {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.T
}

func (c *TestClock) Add(d time.Duration) {
	c.Mu.Lock()
	c.T = c.T.Add(d)
	c.Mu.Unlock()
}

type ListingBody struct {
	Entries []struct {
		Name string `json:"name"`
	} `json:"entries"`
	Stale      bool `json:"stale"`
	AgeSeconds int  `json:"ageSeconds"`
}

func DecodeListing(t *testing.T, rec *httptest.ResponseRecorder) ListingBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var b ListingBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func Names(b ListingBody) []string {
	out := []string{}
	for _, e := range b.Entries {
		out = append(out, e.Name)
	}
	return out
}

// Set replaces what the stub lists, as if the folder changed.
func (f *StubFilesLister) Set(entries []files.Entry, err error) {
	f.Mu.Lock()
	f.Entries, f.Err = entries, err
	f.Mu.Unlock()
}

func (f *StubFilesLister) ListCalls() int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Calls
}

// WaitFor fails the test unless cond holds within a second.
func WaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func NewListingFixture(t *testing.T, l *StubFilesLister) (*Fixture, *TestClock) {
	clk := &TestClock{T: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	return New(t, Options{Files: l, Now: clk.Now}), clk
}
