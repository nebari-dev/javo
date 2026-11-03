//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/naming"
)

type sizeItem struct {
	Name   string  `json:"name"`
	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
	Fits   bool    `json:"fits"`
	Reason string  `json:"reason"`
}

type sizeList struct {
	Items  []sizeItem `json:"items"`
	Custom struct {
		Allowed   bool    `json:"allowed"`
		MaxCPU    float64 `json:"maxCPU"`
		MaxMemory int64   `json:"maxMemory"`
	} `json:"custom"`
}

type problemBody struct {
	Title  string `json:"title"`
	Errors []struct {
		Location, Message string
	} `json:"errors"`
}

// expect sends body and wants status, returning the problem it answered.
func (s *session) expect(method, path string, body any, status int) problemBody {
	s.t.Helper()
	resp, data := s.do(method, path, body)
	if resp.StatusCode != status {
		s.t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, data, status)
	}
	var p problemBody
	_ = json.Unmarshal(data, &p)
	return p
}

func (p problemBody) field(loc string) string {
	for _, e := range p.Errors {
		if e.Location == loc {
			return e.Message
		}
	}
	return ""
}

// TestSizeBiggerThanTheNode: a size no node can hold is offered marked with
// its reason, and creating an app at it or changing one to it is refused.
// (An app already at such a size fails as SizeTooBig: the controller's
// envtest covers that, since only the API writes app sizes.)
func TestSizeBiggerThanTheNode(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var list sizeList
	s.json(http.MethodGet, "/api/sizes", nil, http.StatusOK, &list)
	var tooBig, fits string
	for _, it := range list.Items {
		switch {
		case !it.Fits && tooBig == "":
			tooBig = it.Name
			if it.Reason == "" {
				t.Fatalf("size %s does not fit and says no reason", it.Name)
			}
		case it.Fits && fits == "":
			fits = it.Name
		}
	}
	if tooBig == "" || fits == "" {
		t.Skipf("this deployment has no size bigger than its nodes (sizes %+v)", list.Items)
	}

	p := s.expect(http.MethodPost, "/api/workspaces", map[string]any{"displayName": "Too big", "template": "streamlit", "sample": true, "size": tooBig}, http.StatusUnprocessableEntity)
	if p.field("body.size") == "" {
		t.Fatalf("refusal names no size reason: %+v", p)
	}

	var a app
	s.json(http.MethodPost, "/api/samples/streamlit", nil, http.StatusCreated, &a)
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
	p = s.expect(http.MethodPut, "/api/workspaces/"+a.Name+"/size", map[string]string{"size": tooBig}, http.StatusUnprocessableEntity)
	if p.field("body.size") == "" {
		t.Fatalf("change refusal names no size reason: %+v", p)
	}
}

// TestCustomAmounts: a platform admin runs an app at a custom amount within
// the node; a member with a group allowance is held to it; a member without
// one is refused.
func TestCustomAmounts(t *testing.T) {
	requireKeycloak(t)
	admin := newSession(t)
	admin.login(keycloakLogin, "alice") // /javo-admins
	var list sizeList
	admin.json(http.MethodGet, "/api/sizes", nil, http.StatusOK, &list)
	if !list.Custom.Allowed || list.Custom.MaxCPU <= 0 {
		t.Fatalf("admin allowance %+v", list.Custom)
	}

	a := admin.createApp(map[string]any{"displayName": "Custom amount", "template": "streamlit", "sample": true,
		"resources": map[string]any{"cpu": 0.6, "memory": 768 << 20}})
	t.Cleanup(func() { admin.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
	c := kube(t)
	owner := admin.me().ID
	deadline := time.Now().Add(time.Minute)
	for {
		var p corev1.Pod
		err := c.Get(context.Background(), client.ObjectKey{Namespace: appsNS, Name: naming.ObjectName(owner, a.Name)}, &p)
		if err == nil {
			r := p.Spec.Containers[0].Resources
			if !r.Requests.Cpu().Equal(resource.MustParse("600m")) || !r.Limits.Memory().Equal(resource.MustParse("768Mi")) {
				t.Fatalf("pod resources %+v", r)
			}
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("no pod: %v", err)
		}
		time.Sleep(time.Second)
	}
	admin.waitFor(a.Name, 5*time.Minute, running(t))

	// bob is in /org/data-science, allowed up to 1 CPU and 1 GB (deploy/dev/values.yaml).
	bob := newSession(t)
	bob.login(keycloakLogin, "bob")
	p := bob.expect(http.MethodPost, "/api/workspaces", map[string]any{"displayName": "Over", "template": "streamlit", "sample": true,
		"resources": map[string]any{"cpu": 2, "memory": 512 << 20}}, http.StatusUnprocessableEntity)
	if p.field("body.resources") != "You can ask for up to 1 CPU and 1 GB." {
		t.Fatalf("over the limit: %+v", p)
	}

	member := newSession(t)
	member.login(localLogin, "alice") // no groups
	member.expect(http.MethodPost, "/api/workspaces", map[string]any{"displayName": "Nope", "template": "streamlit", "sample": true,
		"resources": map[string]any{"cpu": 0.5, "memory": 512 << 20}}, http.StatusForbidden)
}
