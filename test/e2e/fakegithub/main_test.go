package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/features/codehostgithub"
)

// The real GitHub host reads what the test sets through PUT /_state, so the
// end-to-end previews run the production client against this server.
func TestTheGitHubHostReadsTheStateTheTestSets(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	host := codehostgithub.New(codehostgithub.Config{APIURL: srv.URL})
	const repo = "https://github.com/example-org/site"

	got, err := host.OpenPulls(context.Background(), repo, "")
	if err != nil || len(got) != 0 {
		t.Fatalf("before any state: %v, %v", got, err)
	}

	state := `{"example-org/site":[
		{"number":3,"title":"third","headSha":"c3","updatedAt":"2026-01-03T00:00:00Z"},
		{"number":4,"title":"from a fork","headSha":"c4","fork":true,"updatedAt":"2026-01-04T00:00:00Z"}]}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/_state", strings.NewReader(state))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT /_state: %v %v", resp, err)
	}

	got, err = host.OpenPulls(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []codehost.PullRequest{{Number: 4, Title: "from a fork", HeadSHA: "c4", Fork: true}, {Number: 3, Title: "third", HeadSHA: "c3"}}
	if len(got) != 2 {
		t.Fatalf("pulls = %+v", got)
	}
	for i := range want {
		g := got[i]
		if g.Number != want[i].Number || g.Title != want[i].Title || g.HeadSHA != want[i].HeadSHA || g.Fork != want[i].Fork {
			t.Fatalf("pull %d = %+v, want %+v", i, g, want[i])
		}
	}
	other, err := host.OpenPulls(context.Background(), "https://github.com/example-org/other", "")
	if err != nil || len(other) != 0 {
		t.Fatalf("another repo: %v, %v", other, err)
	}
}

// A test can hold a repository's pull request list for a while and see that a
// call is waiting, so it can act in the middle of a sync.
func TestTheListCanBeHeldAndTheWaitingCallSeen(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	defer srv.Close()
	put := func(path, body string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader(body))
		if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
			t.Fatalf("PUT %s: %v %v", path, resp, err)
		}
	}
	waiting := func() string {
		t.Helper()
		resp, err := http.Get(srv.URL + "/_waiting")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strings.TrimSpace(string(b))
	}

	put("/_delay", `{"example-org/site":"300ms"}`)
	done := make(chan time.Duration)
	go func() {
		start := time.Now()
		resp, err := http.Get(srv.URL + "/repos/example-org/site/pulls")
		if err == nil {
			resp.Body.Close()
		}
		done <- time.Since(start)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for waiting() != "1" {
		if time.Now().After(deadline) {
			t.Fatal("the held call was never seen as waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if took := <-done; took < 250*time.Millisecond {
		t.Fatalf("the list answered after %s, want it held for 300ms", took)
	}
	if got := waiting(); got != "0" {
		t.Fatalf("waiting = %s after the call ended, want 0", got)
	}
}
