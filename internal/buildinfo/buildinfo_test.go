package buildinfo

import (
	"runtime/debug"
	"testing"
)

func stamped(rev string) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: rev}}}, true
	}
}

func none() (*debug.BuildInfo, bool) { return nil, false }

func TestResolve(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version, commit string
		read                  func() (*debug.BuildInfo, bool)
		want                  Info
	}{
		{"release, linked", "v0.2.22", "abc123", stamped("ignored"), Info{Version: "v0.2.22", Commit: "abc123"}},
		{"dev, linked commit", "", "abc123", none, Info{Version: "dev", Commit: "abc123"}},
		{"dev, commit from vcs stamp", "", "", stamped("def456"), Info{Version: "dev", Commit: "def456"}},
		{"dev, nothing known", "", "", none, Info{Version: "dev"}},
	} {
		if got := resolve(tc.version, tc.commit, tc.read); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
