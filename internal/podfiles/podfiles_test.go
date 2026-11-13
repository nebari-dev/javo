package podfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

func appPod() *workload.Spec {
	s := workload.Spec{Main: workload.Unit{Name: workload.MainUnit, Image: "example.com/app:1", Env: []workload.Env{
		{Name: "HOME", Value: podspec.HomeDir}, {Name: "JAVO_USER", Value: "alice"},
	}}}
	return &s
}

func initNamed(spec *workload.Spec, name string) *workload.Unit {
	for i := range spec.Steps {
		if spec.Steps[i].Name == name {
			return &spec.Steps[i]
		}
	}
	return nil
}

// The copy keeps the tree and the modes, so the scripts stay executable.
func TestCopyKeepsTheTreeAndTheModes(t *testing.T) {
	from, to := t.TempDir(), filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(filepath.Join(from, "samples", "streamlit"), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(from, "pod-user.sh"), []byte("#!/bin/sh\n"), 0o755))
	must(os.WriteFile(filepath.Join(from, "samples", "streamlit", "app.py"), []byte("import streamlit\n"), 0o644))
	must(os.Symlink("pod-user.sh", filepath.Join(from, "link")))
	must(Copy(from, to))

	fi, err := os.Stat(filepath.Join(to, "pod-user.sh"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("pod-user.sh: %v, %v", fi, err)
	}
	got, err := os.ReadFile(filepath.Join(to, "samples", "streamlit", "app.py"))
	if err != nil || string(got) != "import streamlit\n" {
		t.Fatalf("app.py = %q, %v", got, err)
	}
	if l, err := os.Readlink(filepath.Join(to, "link")); err != nil || l != "pod-user.sh" {
		t.Fatalf("link = %q, %v", l, err)
	}
}

// The app container sees the files, and the passwd, group and bash.bashrc
// the identity step writes in place of the image's; the files are copied by
// the platform's image and the identity is written by the app's own image,
// whose /etc it starts from.
func TestThePodGetsTheFilesAndAnIdentity(t *testing.T) {
	spec := appPod()
	Add(spec, podspec.Image{Ref: "example.com/javo:1"})

	cp, id := initNamed(spec, CopyContainer), initNamed(spec, IdentityContainer)
	if cp == nil || cp.Image != "example.com/javo:1" || id == nil || id.Image != "example.com/app:1" {
		t.Fatalf("init containers: %+v", spec.Steps)
	}
	mounts := map[string]workload.Mount{}
	for _, m := range spec.Main.Mounts {
		mounts[m.Path] = m
	}
	if _, ok := mounts[Dir]; !ok {
		t.Fatalf("the app container does not see %s: %+v", Dir, mounts)
	}
	for _, f := range []string{"passwd", "group", "bash.bashrc"} {
		m, ok := mounts["/etc/"+f]
		if !ok || m.SubPath != f || !m.ReadOnly {
			t.Fatalf("/etc/%s is not the written file, read-only: %+v", f, m)
		}
	}
	if got := ReadOnly(); got.Path != Dir || !got.ReadOnly {
		t.Fatalf("ReadOnly() = %+v", got)
	}
}

// identityStep runs the identity step's script on this machine against a
// fake /etc and returns what it wrote.
func identityStep(t *testing.T, passwd, group, bashrc string, env ...string) map[string]string {
	t.Helper()
	etc, out := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"passwd": passwd, "group": group, "bash.bashrc": bashrc} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(etc, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	step := initNamed(func() *workload.Spec { s := appPod(); Add(s, podspec.Image{Ref: "javo"}); return s }(), IdentityContainer)
	argv := append(append([]string{}, step.Command...), step.Args...)
	for i, a := range argv {
		argv[i] = strings.NewReplacer("/etc/", etc+"/", etcDir, out).Replace(a)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=/home/user"}, env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("identity step: %v\n%s", err, b)
	}
	got := map[string]string{}
	for _, name := range []string{"passwd", "group", "bash.bashrc"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		got[name] = string(b)
	}
	return got
}

func TestTheIdentityNamesThePodsUIDAfterItsOwner(t *testing.T) {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	got := identityStep(t, "root:x:0:0:root:/root:/bin/bash\n", "root:x:0:\n", "# the image's\n", "JAVO_USER=alice")
	if !strings.Contains(got["passwd"], "root:x:0:0:root:/root:/bin/bash\n") ||
		!strings.HasSuffix(got["passwd"], "alice:x:"+uid+":"+gid+":alice:/home/user:/bin/bash\n") {
		t.Fatalf("passwd:\n%s", got["passwd"])
	}
	if !strings.Contains(got["group"], "alice:x:"+gid+":") {
		t.Fatalf("group:\n%s", got["group"])
	}
	if !strings.HasPrefix(got["bash.bashrc"], "# the image's\n") || !strings.Contains(got["bash.bashrc"], Dir+"/javo.bashrc") {
		t.Fatalf("bash.bashrc:\n%s", got["bash.bashrc"])
	}
}

// An image that already lists the uid (rocker's rstudio is uid 1000) gets
// the entry replaced, so the session's home is the mounted one; a name
// another account has falls back to "user".
func TestTheIdentityReplacesTheImagesEntryForTheUID(t *testing.T) {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	passwd := "root:x:0:0:root:/root:/bin/bash\nrstudio:x:" + uid + ":" + gid + "::/home/rstudio:/bin/bash\n"
	got := identityStep(t, passwd, "users:x:"+gid+":\n", "", "JAVO_USER=root")
	want := "root:x:0:0:root:/root:/bin/bash\nuser:x:" + uid + ":" + gid + ":user:/home/user:/bin/bash\n"
	if got["passwd"] != want {
		t.Fatalf("passwd:\n%s\nwant:\n%s", got["passwd"], want)
	}
	if got["group"] != "users:x:"+gid+":\n" {
		t.Fatalf("group:\n%s", got["group"])
	}
}

// A private platform image is pulled with the Secrets it names; one already
// on the pod is not listed twice, and a public image adds none.
func TestApplyPullsAPrivateImageWithItsSecrets(t *testing.T) {
	spec := appPod()
	spec.PullSecrets = []string{"app-registry", "ghcr"}
	Add(spec, podspec.Image{Ref: "ghcr.io/example/javo:1", PullSecrets: []string{"ghcr", "mirror"}})
	got := spec.PullSecrets
	want := []string{"app-registry", "ghcr", "mirror"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("imagePullSecrets = %v, want %v", got, want)
	}
	if c := initNamed(spec, CopyContainer); c == nil || c.Image != "ghcr.io/example/javo:1" {
		t.Fatalf("copy container = %+v", c)
	}

	public := appPod()
	Add(public, podspec.Image{Ref: "javo:1"})
	if public.PullSecrets != nil {
		t.Fatalf("public image: imagePullSecrets = %v, want none", public.PullSecrets)
	}
}
