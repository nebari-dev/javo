package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

// pulls is a fake daemon that holds the images in has and records each
// pull as "<image> <user of the login>".
func pulls(t *testing.T, has ...string) (*Client, *[]string) {
	var got []string
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			for _, h := range has {
				if strings.HasSuffix(r.URL.Path, "/images/"+h+"/json") {
					reply(w, 200, map[string]string{"Id": "sha256:1"})
					return
				}
			}
			fail(w, 404, "No such image")
			return
		}
		user := ""
		if h := r.Header.Get("X-Registry-Auth"); h != "" {
			raw, _ := base64.URLEncoding.DecodeString(h)
			var a map[string]string
			json.Unmarshal(raw, &a)
			user = a["username"] + ":" + a["password"] + "@" + a["serveraddress"]
		}
		got = append(got, strings.TrimSpace(r.URL.Query().Get("fromImage")+" "+user))
		io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Done"}`+"\n")
	})
	return c, &got
}

func TestAnImageIsPulledWhenMissingOrAlways(t *testing.T) {
	c, got := pulls(t, "here:1")
	ctx := context.Background()
	if err := EnsureImage(ctx, c, "here:1", false, nil); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 0 {
		t.Fatalf("an image the daemon holds was pulled: %v", *got)
	}
	if err := EnsureImage(ctx, c, "here:1", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := EnsureImage(ctx, c, "missing:2", false, &RegistryAuth{Username: "bot", Password: "pw", ServerAddress: "docker.io"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"here:1", "missing:2 bot:pw@docker.io"}
	if strings.Join(*got, "|") != strings.Join(want, "|") {
		t.Fatalf("pulls = %v", *got)
	}
}

// Without a tag the daemon would fetch every tag of an image.
func TestAPullAlwaysNamesOneImage(t *testing.T) {
	c, got := pulls(t)
	for _, ref := range []string{"busybox", "registry.example.com:5000/a/b", "busybox:1", "busybox@sha256:abc"} {
		if err := c.ImagePull(context.Background(), ref, nil); err != nil {
			t.Fatal(err)
		}
	}
	want := "busybox:latest|registry.example.com:5000/a/b:latest|busybox:1|busybox@sha256:abc"
	if strings.Join(*got, "|") != want {
		t.Fatalf("pulls = %v", *got)
	}
}

// The daemon answers 200 and reports a failed pull inside the progress.
func TestAFailedPullIsAnError(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"pull access denied for private"}`+"\n")
	})
	err := c.ImagePull(context.Background(), "private:1", nil)
	if err == nil || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestALoginGoesOnlyToThePlatformsImagesOnItsOwnRegistry(t *testing.T) {
	logins := func(name string) []RegistryAuth {
		if name != "platform-registry" {
			return nil
		}
		return []RegistryAuth{
			{Username: "bot", ServerAddress: "https://registry.example.com/v2/"},
			{Username: "hub", ServerAddress: "https://index.docker.io/v1/"},
		}
	}
	platform := workload.Spec{PullSecrets: []string{"platform-registry"}}
	for image, want := range map[string]string{
		"registry.example.com/javo/lab:1":         "bot",
		"REGISTRY.example.com/javo/lab:1":         "bot",
		"registry.example.com:5000/javo/x":        "",
		"evil.example.org/javo/lab:1":             "",
		"alice/app:1":                             "hub",
		"busybox":                                 "hub",
		"docker.io/library/busybox":               "hub",
		"registry.example.com.evil.example.org/x": "",
	} {
		got := ""
		if a := PullAuth(platform, image, logins); a != nil {
			got = a.Username
		}
		if got != want {
			t.Errorf("%s: login %q, want %q", image, got, want)
		}
	}
	// A workload that names no pull credentials (every one that runs a
	// person's image) never sends any, even to the platform's registry.
	if a := PullAuth(workload.Spec{}, "registry.example.com/alice/private:1", logins); a != nil {
		t.Errorf("a login was sent for a workload without pull credentials: %+v", a)
	}
}
