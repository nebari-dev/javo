package storage_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/storage"
)

func TestFilesAnswersARepeatOpenFromTheCacheAndRefreshesIt(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.py"}}}
	f, clk := apitest.NewListingFixture(t, l)
	alice := f.Session("alice")

	if b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", alice)); b.Stale || len(b.Entries) != 1 {
		t.Fatalf("first open: %+v", b)
	}

	// Ten seconds later the folder has a new file. The answer is the listing
	// already known, marked stale with its age, and a refresh starts.
	clk.Add(10 * time.Second)
	l.Set([]files.Entry{{Name: "a.py"}, {Name: "b.py"}}, nil)
	b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", alice))
	if !b.Stale || b.AgeSeconds != 10 || len(b.Entries) != 1 {
		t.Fatalf("repeat open: %+v", b)
	}
	apitest.WaitFor(t, "the background refresh", func() bool { return l.ListCalls() == 2 })

	apitest.WaitFor(t, "the fresh listing", func() bool {
		b = apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", alice))
		return !b.Stale
	})
	if len(b.Entries) != 2 || b.AgeSeconds != 0 {
		t.Fatalf("after the refresh: %+v", b)
	}
}

func TestFilesNeverAnswersOneUserWithAnothersListing(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "alice-notes.md"}}}
	f, _ := apitest.NewListingFixture(t, l)
	if b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", f.Session("alice"))); apitest.Names(b)[0] != "alice-notes.md" {
		t.Fatalf("alice: %+v", b)
	}
	l.Set([]files.Entry{{Name: "bob-notes.md"}}, nil)
	b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", f.Session("bob")))
	if b.Stale || len(b.Entries) != 1 || b.Entries[0].Name != "bob-notes.md" || l.User != "bob" {
		t.Fatalf("bob got %+v (lister asked for %q)", b, l.User)
	}
}

func TestFilesForgetsTheListingsOfAUserWhoseHomeAnAppIsAboutToChange(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "app.py"}}}
	clk := &apitest.TestClock{T: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	f := apitest.New(t, apitest.Options{Files: l, Now: clk.Now}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", alice))
	if w := f.Do(http.MethodPost, "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"app.py"}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	// The app writes a sample into the home.
	clk.Add(10 * time.Second)
	l.Set([]files.Entry{{Name: "app.py"}, {Name: "sample"}}, nil)
	b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", alice))
	if b.Stale || len(b.Entries) != 2 {
		t.Fatalf("after create: %+v", b)
	}
}

func TestFilesDropsAListingThatCannotBeReadAnyMore(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.py"}}}
	f, clk := apitest.NewListingFixture(t, l)
	alice := f.Session("alice")
	apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=old", "", alice))
	clk.Add(10 * time.Second)
	l.Set(nil, files.ErrNotFound)
	apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=old", "", alice))
	apitest.WaitFor(t, "the folder to be reported missing", func() bool {
		return f.Do(http.MethodGet, "/api/files?path=old", "", alice).Code == http.StatusNotFound
	})
}

func TestFilesFillsTheCacheOnceAStartingHomeOpens(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Err: files.ErrHomeStarting}
	f, _ := apitest.NewListingFixture(t, l)
	alice := f.Session("alice")
	if rec := f.Do(http.MethodGet, "/api/files?path=", "", alice); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("starting: %d", rec.Code)
	}
	// The pod becomes ready: the gateway, still asking in the background,
	// lists the folder before the picker asks again.
	l.Set([]files.Entry{{Name: "a.py"}}, nil)
	apitest.WaitFor(t, "the background fill", func() bool { return l.ListCalls() >= 2 })
	apitest.WaitFor(t, "the listing to be cached", func() bool {
		calls := l.ListCalls()
		rec := f.Do(http.MethodGet, "/api/files?path=", "", alice)
		return rec.Code == http.StatusOK && l.ListCalls() == calls
	})
}

func TestWarmListsTheHomeFolderAheadOfThePickerAtMostOnceInAWhile(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.py"}}}
	f, clk := apitest.NewListingFixture(t, l)
	alice := f.Session("alice")
	for range 3 {
		if rec := f.Do(http.MethodPost, "/api/files/warm", "", alice); rec.Code != http.StatusNoContent {
			t.Fatalf("warm: %d %s", rec.Code, rec.Body)
		}
	}
	apitest.WaitFor(t, "the warm listing", func() bool { return l.ListCalls() == 1 })
	if l.User != "alice" || l.Path != "" {
		t.Fatalf("warmed %q %q", l.User, l.Path)
	}
	// The picker opens: the listing is there already.
	if b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", alice)); b.Stale || l.ListCalls() != 1 {
		t.Fatalf("open after warm: %+v, %d calls", b, l.ListCalls())
	}
	time.Sleep(20 * time.Millisecond)
	if n := l.ListCalls(); n != 1 {
		t.Fatalf("three warm calls listed the home %d times", n)
	}

	clk.Add(storage.WarmEvery)
	f.Do(http.MethodPost, "/api/files/warm", "", alice)
	apitest.WaitFor(t, "a warm call after the limit", func() bool { return l.ListCalls() == 2 })
}

func TestWarmRequiresSignIn(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f, _ := apitest.NewListingFixture(t, l)
	if rec := f.Do(http.MethodPost, "/api/files/warm", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous warm: %d", rec.Code)
	}
}

func TestUsingTheAPIKeepsTheUsersHomeOpen(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{}
	f, _ := apitest.NewListingFixture(t, l)
	if rec := f.Do(http.MethodGet, "/api/workspaces", "", f.Session("alice")); rec.Code != http.StatusOK {
		t.Fatalf("list apps: %d", rec.Code)
	}
	if rec := f.Do(http.MethodGet, "/api/workspaces", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	l.Mu.Lock()
	defer l.Mu.Unlock()
	if len(l.Touched) != 1 || l.Touched[0] != "alice" {
		t.Fatalf("touched %v", l.Touched)
	}
}

func TestFilesAnswersAColdOpenAsSoonAsTheHomeOpens(t *testing.T) {
	t.Parallel()
	// The file pod is ready on the third time the gateway asks.
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "a.py"}}, StartingFor: 2}
	f, _ := apitest.NewListingFixture(t, l)
	// One request: the gateway keeps asking while the pod starts and answers
	// with the listing instead of telling the picker to come back.
	if b := apitest.DecodeListing(t, f.Do(http.MethodGet, "/api/files?path=", "", f.Session("alice"))); len(b.Entries) != 1 || b.Stale {
		t.Fatalf("cold open: %+v", b)
	}
}

func TestWarmAfterAnAppTookTheHomeIsNotHeldBack(t *testing.T) {
	t.Parallel()
	l := &apitest.StubFilesLister{Entries: []files.Entry{{Name: "app.py"}}}
	clk := &apitest.TestClock{T: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	f := apitest.New(t, apitest.Options{Files: l, Now: clk.Now}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	f.Do(http.MethodPost, "/api/files/warm", "", alice)
	apitest.WaitFor(t, "the first warm-up", func() bool { return l.ListCalls() == 1 })
	// Creating an app frees the home (the file pod is deleted); the New app
	// page that opens next warms it up again at once.
	if w := f.Do(http.MethodPost, "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"app.py"}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	f.Do(http.MethodPost, "/api/files/warm", "", alice)
	apitest.WaitFor(t, "the warm-up after the app", func() bool { return l.ListCalls() == 2 })
}
