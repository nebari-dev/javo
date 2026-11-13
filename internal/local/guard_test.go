//go:build linux || darwin

package local

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuardAnswersOnlyItsOwnAddressAndItsOwnPages(t *testing.T) {
	const addr = "127.0.0.1:8686"
	guarded := Guard(addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	type req struct {
		method, host string
		headers      map[string]string
	}
	link := map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	for name, c := range map[string]struct {
		req
		ok bool
	}{
		"the address it listens on":           {req{"GET", addr, nil}, true},
		"localhost and its port":              {req{"GET", "localhost:8686", nil}, true},
		"LOCALHOST, as some clients write it": {req{"GET", "LOCALHOST:8686", nil}, true},
		"a name that resolves to loopback":    {req{"GET", "rebind.example.com:8686", nil}, false},
		"its address on another port":         {req{"GET", "127.0.0.1:9999", nil}, false},
		"its address without the port":        {req{"GET", "127.0.0.1", nil}, false},
		"no Host":                             {req{"GET", "", nil}, false},

		"its own page":                  {req{"POST", addr, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://" + addr}}, true},
		"a typed address":               {req{"GET", addr, map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate"}}, true},
		"a link on another site":        {req{"GET", addr, link}, true},
		"a form posted by another site": {req{"POST", addr, link}, false},
		"another site's fetch":          {req{"GET", addr, map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"}}, false},
		"a page on another local port":  {req{"GET", addr, map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}}, false},
		"another site's frame":          {req{"GET", addr, map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}}, false},
		"another site's websocket":      {req{"GET", addr, map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "websocket"}}, false},

		// A browser that sends no Sec-Fetch headers still names the origin.
		"its own origin, no Sec-Fetch":         {req{"POST", addr, map[string]string{"Origin": "http://" + addr}}, true},
		"localhost as origin, no Sec-Fetch":    {req{"POST", "localhost:8686", map[string]string{"Origin": "http://localhost:8686"}}, true},
		"another origin, no Sec-Fetch":         {req{"GET", addr, map[string]string{"Origin": "http://evil.example.com"}}, false},
		"another local port, no Sec-Fetch":     {req{"GET", addr, map[string]string{"Origin": "http://127.0.0.1:3000"}}, false},
		"an opaque origin, no Sec-Fetch":       {req{"POST", addr, map[string]string{"Origin": "null"}}, false},
		"its address over https, no Sec-Fetch": {req{"POST", addr, map[string]string{"Origin": "https://" + addr}}, false},
		// An app calling the API, curl: no browser, no cookie of the person's.
		"a program that is not a browser": {req{"POST", addr, nil}, true},
	} {
		r := httptest.NewRequest(c.method, "/api/me", nil)
		r.Host = c.host
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		guarded.ServeHTTP(w, r)
		if got := w.Code == http.StatusNoContent; got != c.ok || !got && w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want answered: %v", name, w.Code, c.ok)
		}
	}
}
