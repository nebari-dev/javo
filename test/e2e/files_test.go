//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

// pickerPoll asks for a folder the way the file picker does: again after
// 0.5 s, 1 s, then every 2 s while the home folder opens or the answer is
// a stale listing. It returns how long until the first listing was on
// screen and until a fresh one was.
func pickerPoll(t *testing.T, s *session, folder string, within time.Duration) (visible, fresh time.Duration) {
	t.Helper()
	start := time.Now()
	for {
		resp, data := s.do(http.MethodGet, "/api/files?path="+folder, nil)
		switch resp.StatusCode {
		case http.StatusOK:
			var l struct{ Stale bool }
			if err := json.Unmarshal(data, &l); err != nil {
				t.Fatal(err)
			}
			if visible == 0 {
				visible = time.Since(start)
			}
			if !l.Stale {
				return visible, time.Since(start)
			}
		case http.StatusServiceUnavailable:
		default:
			t.Fatalf("GET /api/files?path=%s: %d %s", folder, resp.StatusCode, data)
		}
		waited := time.Since(start)
		if waited > within {
			t.Fatalf("%s not listed within %s", folder, within)
		}
		switch {
		case waited < 500*time.Millisecond:
			time.Sleep(500 * time.Millisecond)
		case waited < 1500*time.Millisecond:
			time.Sleep(time.Second)
		default:
			time.Sleep(2 * time.Second)
		}
	}
}

// stopReader deletes ownerID's file pod and waits until it is gone, so the
// next open starts it cold.
func stopReader(t *testing.T, c client.Client, ownerID string) {
	t.Helper()
	ctx := context.Background()
	sel := client.MatchingLabels{model.LabelRole: "files", model.LabelOwner: ownerID}
	if err := c.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace(appsNS), sel, client.GracePeriodSeconds(0)); err != nil {
		t.Fatalf("delete file pod: %v", err)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(200 * time.Millisecond) {
		var pods corev1.PodList
		if err := c.List(ctx, &pods, client.InNamespace(appsNS), sel); err != nil {
			t.Fatal(err)
		}
		if len(pods.Items) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("file pod is still there after a minute")
		}
	}
}

// TestFilePickerTimings measures how long the file picker takes to show a
// folder: cold (no file pod), warmed by a page load, and warm. Folders made
// for this run are not in the gateway's cache, so cold opens wait for the
// pod, not for a cached answer.
func TestFilePickerTimings(t *testing.T) {
	s := newSession(t)
	s.login(localLogin, "alice")
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)
	run := time.Now().Format("150405")
	ensureHomeClaim(t, c, me.ID)
	runHomeJob(t, s, c, me.ID, "sh", "-c", `for d in cold page warm; do mkdir -p "/home/user/e2e-picker/$1/$d" && : > "/home/user/e2e-picker/$1/$d/app.py"; done`, "seed", run)
	dir := func(d string) string { return "e2e-picker/" + run + "/" + d }

	stopReader(t, c, me.ID)
	_, cold := pickerPoll(t, s, dir("cold"), 2*time.Minute)
	t.Logf("cold open, no warm-up: folder listed %s after the picker asked", cold.Round(time.Millisecond))

	// The gateway takes one warm-up per user every 10 seconds; the pod is
	// stopped behind its back here, so each case waits that out first.
	var lastWarm time.Time
	warm := func() {
		time.Sleep(time.Until(lastWarm.Add(10 * time.Second)))
		lastWarm = time.Now()
		if resp, data := s.do(http.MethodPost, "/api/files/warm", nil); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST /api/files/warm: %d %s", resp.StatusCode, data)
		}
	}

	// The New app page loads and warms the home folder up; the user opens
	// the picker straight away.
	stopReader(t, c, me.ID)
	start := time.Now()
	warm()
	pickerPoll(t, s, dir("page"), 2*time.Minute)
	t.Logf("cold, picker opened right after the page loaded: folder listed %s after page load", time.Since(start).Round(time.Millisecond))

	// The page loaded a moment before the user opened the picker.
	stopReader(t, c, me.ID)
	warm()
	time.Sleep(3 * time.Second)
	_, prewarmed := pickerPoll(t, s, dir("warm"), time.Minute)
	t.Logf("prewarmed first open (picker opened 3 s after page load): folder listed %s after the picker asked", prewarmed.Round(time.Millisecond))

	// Warm: the pod is up and the folders were listed before.
	var opens []time.Duration
	for range 5 {
		visible, _ := pickerPoll(t, s, dir("warm"), 10*time.Second)
		opens = append(opens, visible.Round(time.Millisecond))
	}
	t.Logf("warm opens of a folder listed before: %v", opens)
	slowest := opens[0]
	for _, w := range opens {
		slowest = max(slowest, w)
	}
	if slowest > 300*time.Millisecond {
		t.Errorf("a warm open took %s, want under 300ms", slowest)
	}
	if prewarmed > time.Second {
		t.Errorf("prewarmed first open took %s, want under 1s", prewarmed)
	}
}
