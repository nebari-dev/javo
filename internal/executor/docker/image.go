package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nebari-dev/javo/internal/workload"
)

// Image is what the daemon says about an image it holds.
type Image struct {
	ID          string `json:"Id"`
	RepoDigests []string
}

// ImageInspect reads an image the daemon holds; ErrNotFound when it does
// not.
func (c *Client) ImageInspect(ctx context.Context, ref string) (Image, error) {
	var out Image
	err := c.get(ctx, "/images/"+url.PathEscape(ref)+"/json", nil, &out)
	return out, err
}

// RegistryAuth is a login of one registry. It is secret.
type RegistryAuth struct {
	Username, Password string
	// ServerAddress is the registry's host, as in an image's name
	// ("registry.example.com", "docker.io").
	ServerAddress string
}

// ImagePull fetches an image from its registry, with auth when it is not
// nil. A name without a tag or a digest means the tag latest.
func (c *Client) ImagePull(ctx context.Context, ref string, auth *RegistryAuth) error {
	if !strings.Contains(ref, "@") && !strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") {
		// Without a tag the daemon would fetch every tag of the image.
		ref += ":latest"
	}
	r := request{method: http.MethodPost, path: "/images/create", query: url.Values{"fromImage": {ref}}}
	if auth != nil {
		raw, err := json.Marshal(map[string]string{"username": auth.Username, "password": auth.Password, "serveraddress": auth.ServerAddress})
		if err != nil {
			return err
		}
		r.header = http.Header{"X-Registry-Auth": {base64.URLEncoding.EncodeToString(raw)}}
	}
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The answer is the pull's progress, and is a success even when the
	// pull fails: the failure is a line of the progress.
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if line.Error != "" {
			return fmt.Errorf("docker: pull of %s: %s", ref, line.Error)
		}
	}
}

// EnsureImage makes the daemon hold image: it pulls when always is set (a
// registry then checks the credentials each time) or the daemon has no
// such image.
func EnsureImage(ctx context.Context, c *Client, image string, always bool, auth *RegistryAuth) error {
	if !always {
		if _, err := c.ImageInspect(ctx, image); err == nil || !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return c.ImagePull(ctx, image, auth)
}

// PullAuth is the login image is pulled with, or nil for none. Only a
// workload that runs the platform's own images names pull credentials
// (Spec.PullSecrets): a workload that runs an image a person named is
// refused before it gets here if it would need them, since Docker, like
// the kubelet, has no way to tell the images of one workload apart. On
// top of that a login is only ever sent to the registry it is for.
// logins gives the logins kept under one of the Spec's names.
func PullAuth(s workload.Spec, image string, logins func(name string) []RegistryAuth) *RegistryAuth {
	for _, name := range s.PullSecrets {
		for _, a := range logins(name) {
			if registryHost(a.ServerAddress) == registryOf(image) {
				return &a
			}
		}
	}
	return nil
}

// registryOf is the registry an image's name points at: its first part
// when that is a host (it has a dot or a port, or is localhost), else
// Docker Hub.
func registryOf(image string) string {
	first, _, found := strings.Cut(image, "/")
	if !found || !(strings.ContainsAny(first, ".:") || first == "localhost") {
		return "docker.io"
	}
	return registryHost(first)
}

// registryHost is a registry's host as registryOf writes it, from the
// forms a login names it in.
func registryHost(address string) string {
	address = strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://")
	host, _, _ := strings.Cut(address, "/")
	host = strings.ToLower(host)
	switch host {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return "docker.io"
	}
	return host
}
