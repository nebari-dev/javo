package config

import (
	"strings"
	"testing"
)

// Apps run on Kubernetes unless the file names another executor, and a
// file written before there was a choice loads as it did.
func TestTheExecutorIsKubernetesUnlessNamed(t *testing.T) {
	p, err := load(t, localAuth)
	if err != nil || !p.OnKubernetes() {
		t.Fatalf("executor %q, %v", p.Executor, err)
	}
	if _, err := load(t, localAuth+"executor: podman\n"); err == nil || !strings.Contains(err.Error(), "executor") {
		t.Fatalf("an unknown executor: %v", err)
	}
}

// A section that only a cluster gives meaning to is refused under another
// executor, by its key and the executor's name, rather than read and
// never applied; so is a section of another executor.
func TestSectionsOfAnotherExecutorAreRefused(t *testing.T) {
	docker := localAuth + "executor: docker\nkeysDir: /var/lib/javo/keys\ndocker:\n  gateway: javo\n"
	if p, err := load(t, docker); err != nil || p.OnKubernetes() || p.Docker.Socket != "/var/run/docker.sock" {
		t.Fatalf("a docker config: %+v, %v", p.Docker, err)
	}
	for key, section := range map[string]string{
		"namespaces":       "namespaces:\n  mode: per-user\n",
		"workloads":        "workloads:\n  runtimeClassName: gvisor\n",
		"storage":          "storage:\n  home:\n    size: 5Gi\n",
		"limits.namespace": "limits:\n  namespace:\n    quota:\n      pods: \"10\"\n",
		"process":          "process:\n  stateDir: /var/lib/javo/state\n",
	} {
		_, err := load(t, docker+section)
		if err == nil || !strings.Contains(err.Error(), key+": ") || !strings.Contains(err.Error(), "executor docker") {
			t.Errorf("%s under docker: %v", key, err)
		}
	}
	for key, section := range map[string]string{
		"docker":  "docker:\n  gateway: javo\n",
		"process": "process:\n  stateDir: /var/lib/javo/state\n",
		"keysDir": "keysDir: /var/lib/javo/keys\n",
	} {
		_, err := load(t, localAuth+section)
		if err == nil || !strings.Contains(err.Error(), key+": ") || !strings.Contains(err.Error(), "executor kubernetes") {
			t.Errorf("%s under kubernetes: %v", key, err)
		}
	}
}

// Each executor that is not Kubernetes needs to know where it is: the
// Docker one its own container, the process one its folders, both where
// Javo's keys are kept.
func TestAnExecutorNeedsItsSettings(t *testing.T) {
	process := localAuth + "executor: process\nkeysDir: /h/keys\nprocess:\n  stateDir: /h/state\n  homesDir: /h/homes\n"
	if _, err := load(t, process); err != nil {
		t.Fatal(err)
	}
	for want, yaml := range map[string]string{
		"docker.gateway":   localAuth + "executor: docker\nkeysDir: /k\n",
		"keysDir":          localAuth + "executor: docker\ndocker:\n  gateway: javo\n",
		"docker.pool":      localAuth + "executor: docker\nkeysDir: /k\ndocker:\n  gateway: javo\n  pool: 8.8.0.0/16\n",
		"docker.socket":    localAuth + "executor: docker\nkeysDir: /k\ndocker:\n  gateway: javo\n  socket: docker.sock\n",
		"process.stateDir": localAuth + "executor: process\nkeysDir: /k\nprocess:\n  homesDir: /h/homes\n",
		"process.homesDir": localAuth + "executor: process\nkeysDir: /k\nprocess:\n  stateDir: state\n  homesDir: homes\n",
		"process.pixi":     process + "  pixi: pixi\n",
		// One person on their own machine: there is no identity provider
		// to ask who a second one is.
		"auth.oidc": process + "urls:\n  uiHost: javo.example.com\nauth:\n  oidc:\n    issuer: https://idp.example.com\n    clientID: javo\n",
		// Home folders are backed up by Jobs of a cluster.
		"backup.s3": process + "backup:\n  s3:\n    endpoint: https://s3.example.com\n    bucket: backups\n",
		// Previews are followed by a controller that only runs on a cluster.
		"source.previews.enabled": process + "source:\n  previews:\n    enabled: true\n",
	} {
		if _, err := load(t, yaml); err == nil || !strings.Contains(err.Error(), want+": ") {
			t.Errorf("want %s refused, got %v", want, err)
		}
	}
}
