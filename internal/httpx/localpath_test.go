package httpx_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/httpx"
)

func TestIsLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/":                          true,
		"/apps/sales?a=b":            true,
		"/u/alice/app/lab":           true,
		"":                           false,
		"apps":                       false,
		"//evil.example.com":         false,
		`/\evil.example.com`:         false,
		"https://evil.example.com/x": false,
		"/ok\n":                      false,
		"/ok\r":                      false,
		"/ok\tmore":                  false,
		"/%zz":                       false,
	} {
		if got := httpx.IsLocalPath(p); got != want {
			t.Errorf("IsLocalPath(%q) = %v, want %v", p, got, want)
		}
	}
}
