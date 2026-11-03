// Command fakegithub is a test-only stand-in for the one GitHub endpoint the
// preview reconciler calls, GET /repos/{owner}/{repo}/pulls. The end-to-end
// tests set what it lists with PUT /_state (a JSON object from "owner/repo" to
// its open pull requests), so a test can open, update and close pull requests
// without GitHub. PUT /_delay holds a repository's list for a while and
// GET /_waiting counts the held calls, so a test can act in the middle of a
// sync. It is never shipped.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

// pull is one open pull request as a test describes it.
type pull struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	HeadSHA   string    `json:"headSha"`
	Fork      bool      `json:"fork,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type server struct {
	mu    sync.Mutex
	state map[string][]pull
	delay map[string]time.Duration
	// waiting is the number of list calls held by a delay right now.
	waiting int
}

func newHandler() http.Handler {
	s := &server{state: map[string][]pull{}, delay: map[string]time.Duration{}}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /_state", s.put)
	mux.HandleFunc("PUT /_delay", s.putDelay)
	mux.HandleFunc("GET /_waiting", s.getWaiting)
	mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", s.pulls)
	return mux
}

func (s *server) put(w http.ResponseWriter, r *http.Request) {
	var state map[string][]pull
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&state); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// putDelay sets how long the pull request list of each repository is held
// before it answers (a JSON object from "owner/repo" to a duration such as
// "20s"); it replaces all delays, so {} clears them.
func (s *server) putDelay(w http.ResponseWriter, r *http.Request) {
	var raw map[string]string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	delay := map[string]time.Duration{}
	for repo, v := range raw {
		d, err := time.ParseDuration(v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		delay[repo] = d
	}
	s.mu.Lock()
	s.delay = delay
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// getWaiting answers how many list calls a delay is holding now.
func (s *server) getWaiting(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	n := s.waiting
	s.mu.Unlock()
	fmt.Fprintln(w, n)
}

// repoRef and pullJSON are the fields of GitHub's pull request object that
// the client reads.
type repoRef struct {
	FullName string `json:"full_name"`
}

type branch struct {
	SHA  string  `json:"sha"`
	Repo repoRef `json:"repo"`
}

type pullJSON struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
	Head      branch    `json:"head"`
	Base      branch    `json:"base"`
}

func (s *server) pulls(w http.ResponseWriter, r *http.Request) {
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	s.mu.Lock()
	hold := s.delay[full]
	if hold > 0 {
		s.waiting++
	}
	s.mu.Unlock()
	if hold > 0 {
		select {
		case <-time.After(hold):
		case <-r.Context().Done():
		}
		s.mu.Lock()
		s.waiting--
		s.mu.Unlock()
	}
	s.mu.Lock()
	list := append([]pull(nil), s.state[full]...)
	s.mu.Unlock()
	// The real API with sort=updated&direction=desc, which the client asks for.
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	out := make([]pullJSON, 0, len(list))
	for _, p := range list {
		head := full
		if p.Fork {
			head = "someone-else/" + r.PathValue("repo")
		}
		out = append(out, pullJSON{
			Number: p.Number, Title: p.Title, State: "open", UpdatedAt: p.UpdatedAt,
			Head: branch{SHA: p.HeadSHA, Repo: repoRef{FullName: head}},
			Base: branch{Repo: repoRef{FullName: full}},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	log.Fatal((&http.Server{Addr: *addr, Handler: newHandler(), ReadHeaderTimeout: 10 * time.Second}).ListenAndServe())
}
