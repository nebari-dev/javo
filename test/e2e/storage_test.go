//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
)

type storageView struct {
	Mode, ModeLabel string
	SizeEnforced    bool
	ScratchPaths    []string
	Notices         []struct{ Code, Tone, Title, Text string }
	Folders         []struct {
		Group, Dir string
		Ready      bool
	}
}

// TestStorage checks storage on whatever cluster the suite runs against: the
// platform has looked at the cluster's storage and says in plain words what
// homes are on; an app's pod mounts its owner's home and node-local scratch
// and sets no fsGroup; on several nodes with a disk per person, two apps of
// one owner land on the same node (they share the disk).
func TestStorage(t *testing.T) {
	s := newSession(t)
	s.login(localLogin, "alice")
	var me struct {
		ID    string
		Roles []string
	}
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)

	var v storageView
	deadline := time.Now().Add(2 * time.Minute)
	for {
		s.json(http.MethodGet, "/api/storage", nil, http.StatusOK, &v)
		if v.ModeLabel != "" && len(v.Notices) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("storage view without a mode label or notices: %+v", v)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("homes are on %s (%s); notices %d", v.Mode, v.ModeLabel, len(v.Notices))
	if !slices.Contains(v.ScratchPaths, "/scratch") {
		t.Errorf("scratch paths = %v", v.ScratchPaths)
	}

	if slices.Contains(me.Roles, "platform-admin") {
		var report struct {
			Rows []struct{ Fact, Provenance string }
		}
		s.json(http.MethodGet, "/api/admin/storage", nil, http.StatusOK, &report)
		if len(report.Rows) == 0 {
			t.Error("the admin storage report has no rows")
		}
	}

	seedHome(t, s, c, me.ID)
	first := s.create("Storage one", "streamlit", seedFile)
	s.waitFor(first.Name, 5*time.Minute, func(a app) bool { return a.Phase == "Running" })
	pods := ownerAppPods(t, c, me.ID)
	if len(pods) == 0 {
		t.Fatal("no app pod")
	}
	pod := pods[0]
	if pod.Spec.SecurityContext.FSGroup != nil {
		t.Errorf("app pod sets fsGroup %d", *pod.Spec.SecurityContext.FSGroup)
	}
	if got := pod.Annotations["cluster-autoscaler.kubernetes.io/safe-to-evict-local-volumes"]; got != "scratch,tmp" {
		t.Errorf("safe-to-evict annotation = %q", got)
	}
	if !slices.ContainsFunc(pod.Spec.Containers[0].VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == "/scratch" }) {
		t.Errorf("no /scratch mount: %+v", pod.Spec.Containers[0].VolumeMounts)
	}

	var nodes corev1.NodeList
	if err := c.List(context.Background(), &nodes); err != nil {
		t.Fatal(err)
	}
	if v.Mode != "perUser" || len(nodes.Items) < 2 {
		t.Logf("co-location not checked: mode %s on %d nodes", v.Mode, len(nodes.Items))
		return
	}
	second := s.create("Storage two", "streamlit", seedFile)
	s.waitFor(second.Name, 5*time.Minute, func(a app) bool { return a.Phase == "Running" })
	pods = ownerAppPods(t, c, me.ID)
	for _, p := range pods[1:] {
		if p.Spec.NodeName != pods[0].Spec.NodeName {
			t.Errorf("apps of one owner on a per-user disk run on %s and %s", pods[0].Spec.NodeName, p.Spec.NodeName)
		}
	}
}

// ownerAppPods lists ownerID's running app pods (not file pods or Jobs).
func ownerAppPods(t *testing.T, c client.Client, ownerID string) []corev1.Pod {
	t.Helper()
	var l corev1.PodList
	if err := c.List(context.Background(), &l, client.InNamespace(appsNS), client.MatchingLabels{model.LabelOwner: ownerID}, client.HasLabels{model.LabelApp}); err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(l.Items, func(p corev1.Pod) bool { return p.Status.Phase != corev1.PodRunning })
}
