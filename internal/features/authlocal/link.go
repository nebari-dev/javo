package authlocal

import (
	"crypto/rand"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/url"
	"sync"

	"github.com/nebari-dev/javo/internal/auth/session"
)

// LinkPath is where a sign-in link is opened.
const LinkPath = "/signin"

// Link signs one local account in without a password, for a Javo that
// serves one person on their own machine (`javo local`): whoever opens
// LinkPath with the token it holds gets a session of that account, as
// the password form gives one. The pattern is Jupyter's, made stricter:
//
//   - a token is 130 random bits, kept in memory only and compared in
//     constant time, so it cannot be guessed and is in no file;
//   - it works once. Using it makes the next one, which is told to
//     announce only when someone opens LinkPath without a token that
//     works: so the person gets a new link by opening that page, and
//     nobody can make Javo print more links than were used.
//
// The link is announced to the person who started Javo, in their
// terminal, and nowhere else: not in a log line and not in a file.
// Whoever can read that terminal is that person. A link that was opened
// stays in the browser's history and is worth nothing there.
type Link struct {
	username string
	announce func(link string)

	mu        sync.Mutex
	token     string
	announced bool
}

// NewLink is a sign-in link for the account username. announce is told
// each link once, as the path and query to open on this Javo's address.
func NewLink(username string, announce func(link string)) *Link {
	return &Link{username: username, announce: announce, token: rand.Text()}
}

// Announce tells the link that works next, unless it was told before.
func (l *Link) Announce() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.announced {
		return
	}
	l.announced = true
	l.announce(LinkPath + "?" + url.Values{"token": {l.token}}.Encode())
}

// spend reports whether token is the one that works, which then no
// longer does.
func (l *Link) spend(token string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(token), []byte(l.token)) != 1 {
		return false
	}
	l.token, l.announced = rand.Text(), false
	return true
}

// signInByLink answers LinkPath: a session and the way to the UI for the
// token that works, and for any other a refusal and the next link in the
// terminal.
func (p *Provider) signInByLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !p.link.spend(r.URL.Query().Get("token")) {
		p.link.Announce()
		http.Error(w, "This sign-in link does not work: each one works once. Look for a new one in the terminal Javo was started in.", http.StatusForbidden)
		return
	}
	u, err := p.users.Lookup(r.Context(), p.link.username)
	if err != nil {
		slog.Error("sign in by link", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	signed, err := p.host.Complete(r.Context(), claims(u), session.Session{
		Provider: ID, Kind: session.KindUI, RevalidateAt: p.host.Now().Add(p.every),
	})
	if err != nil {
		slog.Error("complete sign-in by link", "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &signed.Cookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
