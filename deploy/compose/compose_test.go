package compose_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type composeFile struct {
	Services map[string]struct {
		Image     string         `json:"image"`
		Profiles  []string       `json:"profiles"`
		Build     *struct{}      `json:"build"`
		DependsOn map[string]any `json:"depends_on"`
	} `json:"services"`
}

type templateFile struct {
	Spec struct {
		Image  string `json:"image"`
		Sample struct {
			Image string `json:"image"`
		} `json:"sample"`
		Build struct {
			Image string `json:"image"`
		} `json:"build"`
	} `json:"spec"`
}

// `docker compose --profile build up` builds every image the chart's built-in
// templates name (the installer copies them into k3s and stops on the first
// one this Docker host lacks), and the installer waits for each build. An
// upstream image pinned by digest is the exception: the installer leaves it
// as it is and k3s pulls it.
func TestComposeBuildsEveryTemplateImage(t *testing.T) {
	b, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c composeFile
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	built := map[string]string{} // image name without tag -> service
	for name, s := range c.Services {
		if s.Build == nil || len(s.Profiles) == 0 || s.Profiles[0] != "build" {
			continue
		}
		built[strings.SplitN(s.Image, ":", 2)[0]] = name
	}
	installer := c.Services["installer"]

	files, err := filepath.Glob("../helm/javo/files/templates/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no chart templates: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var tpl templateFile
		if err := yaml.Unmarshal(b, &tpl); err != nil {
			t.Fatal(err)
		}
		for _, img := range []string{tpl.Spec.Image, tpl.Spec.Sample.Image, tpl.Spec.Build.Image} {
			if img == "" || strings.Contains(img, "@sha256:") {
				continue
			}
			base := strings.SplitN(img, ":", 2)[0]
			svc, ok := built[base]
			if !ok {
				t.Errorf("%s names %s, which no build service in compose.yaml builds", filepath.Base(f), img)
				continue
			}
			if _, ok := installer.DependsOn[svc]; !ok {
				t.Errorf("the installer does not wait for build service %s (%s)", svc, img)
			}
		}
	}
}

// dockerFile is the part of docker.yaml the checks below read.
type dockerFile struct {
	Services map[string]struct {
		Command       []string `json:"command"`
		ContainerName string   `json:"container_name"`
		Privileged    bool     `json:"privileged"`
		NetworkMode   string   `json:"network_mode"`
		Pid           string   `json:"pid"`
		Ipc           string   `json:"ipc"`
		CapAdd        []string `json:"cap_add"`
		Ports         []string `json:"ports"`
		Volumes       []string `json:"volumes"`
	} `json:"services"`
	Configs map[string]struct {
		Content string `json:"content"`
	} `json:"configs"`
}

// docker.yaml is one service, Javo with the Docker executor: it holds the
// Docker socket and nothing else of the host's but the chart's template
// files, is not privileged, shares none of the host's namespaces,
// publishes the gateway's port only, and its container has the name its
// config gives docker.gateway, without which Javo does not start.
func TestDockerComposeFileRunsOneUnprivilegedJavo(t *testing.T) {
	b, err := os.ReadFile("docker.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var c dockerFile
	if err := yaml.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	javo, ok := c.Services["javo"]
	if !ok || len(c.Services) != 1 {
		t.Fatalf("services = %v, want javo alone", c.Services)
	}
	if javo.Privileged || javo.NetworkMode != "" || javo.Pid != "" || javo.Ipc != "" || len(javo.CapAdd) != 0 {
		t.Errorf("javo is privileged or shares a namespace of the host: %+v", javo)
	}
	if len(javo.Ports) != 1 || !strings.HasSuffix(javo.Ports[0], ":8080") || !slices.Contains(javo.Command, "--listen=:8080") {
		t.Errorf("ports %v with command %v, want the gateway's port alone", javo.Ports, javo.Command)
	}
	const socket = "/var/run/docker.sock"
	sockets := 0
	for _, v := range javo.Volumes {
		source, rest, _ := strings.Cut(v, ":")
		switch {
		case source == socket:
			sockets++
		case source == "data":
		case strings.HasPrefix(source, "../helm/javo/files/templates/") && strings.HasSuffix(rest, ":ro"):
			if _, err := os.Stat(source); err != nil {
				t.Errorf("mounts a template that does not exist: %v", err)
			}
			if filepath.Base(source) == "backup.yaml" {
				t.Error("offers the backup template, which needs Kubernetes")
			}
		default:
			t.Errorf("mounts %s: only the socket, the data volume and the templates (read-only) are", v)
		}
	}
	if sockets != 1 {
		t.Errorf("the Docker socket is mounted %d times, want once", sockets)
	}

	var cfg struct {
		Executor string `json:"executor"`
		Docker   struct {
			Gateway string `json:"gateway"`
		} `json:"docker"`
	}
	if err := yaml.Unmarshal([]byte(c.Configs["config"].Content), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Executor != "docker" || cfg.Docker.Gateway == "" || cfg.Docker.Gateway != javo.ContainerName {
		t.Errorf("executor %q with docker.gateway %q in a container named %q", cfg.Executor, cfg.Docker.Gateway, javo.ContainerName)
	}
}
