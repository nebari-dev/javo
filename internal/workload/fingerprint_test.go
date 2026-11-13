package workload

import "testing"

func TestOutdated(t *testing.T) {
	built := Fingerprint{Size: "medium", Run: "r1", Restart: "t1", APIAccess: "Read", GitToken: "3", Secrets: "s1", Layout: "l1"}
	change := func(f func(*Fingerprint)) Fingerprint { w := built; f(&w); return w }
	for _, tc := range []struct {
		name string
		want Fingerprint
		have Fingerprint
		out  bool
	}{
		{"the same", built, built, false},
		{"another size", change(func(w *Fingerprint) { w.Size = "large" }), built, true},
		{"another command, image or port", change(func(w *Fingerprint) { w.Run = "r2" }), built, true},
		{"a restart request", change(func(w *Fingerprint) { w.Restart = "t2" }), built, true},
		{"another API access", change(func(w *Fingerprint) { w.APIAccess = "None" }), built, true},
		{"another repository token", change(func(w *Fingerprint) { w.GitToken = "4" }), built, true},
		{"other secrets", change(func(w *Fingerprint) { w.Secrets = "s2" }), built, true},
		{"another layout", change(func(w *Fingerprint) { w.Layout = "l2" }), built, true},
		// The layout cannot always be known (the owner has no uid yet): then
		// it is not compared.
		{"a layout that cannot be known now", change(func(w *Fingerprint) { w.Layout = "" }), built, false},
		// Built before sizes were recorded: an app that never picked a size
		// keeps running, so an upgrade does not restart every app.
		{"no recorded size, none picked", Fingerprint{Size: "small", SizeUnpicked: true}, Fingerprint{}, false},
		{"no recorded size, one picked", Fingerprint{Size: "small"}, Fingerprint{}, true},
		{"a recorded size, none picked", Fingerprint{Size: "small", SizeUnpicked: true}, Fingerprint{Size: "medium"}, true},
	} {
		if got := tc.want.Outdated(tc.have); got != tc.out {
			t.Errorf("%s: Outdated = %v, want %v", tc.name, got, tc.out)
		}
	}
}
