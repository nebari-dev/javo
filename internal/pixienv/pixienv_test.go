package pixienv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
)

var files = model.EnvironmentFiles{Manifest: "[workspace]\nname = \"sales\"\nplatforms = [\"linux-64\"]\n", Lock: "version: 6\nenvironments: {}\n"}

func appPod() *corev1.PodSpec {
	s := podspec.Base("alice", "example.com/app:1", corev1.ResourceRequirements{})
	s.Containers[0].VolumeMounts = []corev1.VolumeMount{
		{Name: podspec.HomeVolume, MountPath: podspec.HomeDir, SubPath: "alice"},
		{Name: "src", MountPath: "/srv/src"},
	}
	return &s
}

func installStep(t *testing.T, spec *corev1.PodSpec) *corev1.Container {
	t.Helper()
	for i := range spec.InitContainers {
		if spec.InitContainers[i].Name == InstallContainer {
			return &spec.InitContainers[i]
		}
	}
	t.Fatalf("no install step: %+v", spec.InitContainers)
	return nil
}

// machine is the install step run on this machine: the pod's paths are
// folders of a temporary directory, and pixi and flock are stand-ins. pixi
// records each install (its folder) in calls, makes the prefix and prints a
// shell hook; flock runs its command.
type machine struct {
	t                         *testing.T
	root, home, envDir, files string
	repo                      string
	calls                     string
}

func newMachine(t *testing.T) *machine {
	m := &machine{t: t, root: t.TempDir()}
	m.home, m.envDir, m.files, m.repo = filepath.Join(m.root, "home"), filepath.Join(m.root, "env"), filepath.Join(m.root, "files"), filepath.Join(m.root, "repo")
	m.calls = filepath.Join(m.root, "calls")
	bin := filepath.Join(m.root, "bin")
	for _, d := range []string{m.home, m.envDir, m.repo, bin} {
		m.must(os.MkdirAll(d, 0o755))
	}
	m.write(filepath.Join(bin, "pixi"), `#!/bin/sh
case "$1" in
install) [ "$2" = --frozen ] || exit 9
  echo "$PWD" >> "`+m.calls+`"
  echo "${RUST_LOG:-}" >> "`+m.calls+`.log"
  [ ! -e fail ] || exit 3
  mkdir -p .pixi/envs/default/bin
  for b in $(sed -n 's/^# bin //p' pixi.toml); do printf '#!/bin/sh\n' > ".pixi/envs/default/bin/$b"; chmod +x ".pixi/envs/default/bin/$b"; done ;;
shell-hook) echo "export PATH=\"$PWD/.pixi/envs/default/bin:/usr/bin\""; echo "export CONDA_PREFIX=$PWD/.pixi/envs/default" ;;
*) exit 8 ;;
esac
`, 0o755)
	m.write(filepath.Join(bin, "flock"), "#!/bin/sh\nshift\nexec \"$@\"\n", 0o755)
	// The platform's binary, which the tools step copies next to the
	// activation: it records what it was asked to clear and whether pixi
	// had installed by then.
	m.write(filepath.Join(m.envDir, model.ProductName), "#!/bin/sh\necho \"$* $(test -d \"$2/bin\" && echo installed)\" >> \""+filepath.Join(m.root, "tool-calls")+"\"\n", 0o755)
	m.bundle("streamlit", "streamlit-lock")
	return m
}

func (m *machine) must(err error) {
	m.t.Helper()
	if err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) write(p, body string, mode os.FileMode) {
	m.t.Helper()
	m.must(os.MkdirAll(filepath.Dir(p), 0o755))
	m.must(os.WriteFile(p, []byte(body), mode))
}

// bundle writes a bundled environment of the pod files.
func (m *machine) bundle(name, lock string) {
	m.write(filepath.Join(m.files, "envs", name, "pixi.toml"), "[workspace]\n", 0o644)
	m.write(filepath.Join(m.files, "envs", name, "pixi.lock"), lock, 0o644)
}

// run applies src to a pod, runs its install step here and returns the
// installed folder the activation file points to.
func (m *machine) run(src Source) string {
	m.t.Helper()
	out, err := m.step(src)
	if err != nil {
		m.t.Fatalf("install step: %v\n%s", err, out)
	}
	act, err := os.ReadFile(filepath.Join(m.envDir, "activate.sh"))
	if err != nil {
		m.t.Fatalf("no activation file: %v\n%s", err, out)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(act)), "\n")
	if !strings.HasPrefix(line, ". ") || !strings.HasSuffix(line, "/activate.sh") {
		m.t.Fatalf("activation file: %q", act)
	}
	return filepath.Dir(strings.TrimPrefix(line, ". "))
}

// step applies src to a pod and runs its install step here; the step's
// termination message goes to m.root/termination-log.
func (m *machine) step(src Source) ([]byte, error) {
	m.t.Helper()
	spec := appPod()
	m.must(Apply(spec, "example.com/pixi:1", podspec.Image{Ref: "example.com/javo:1"}, src))
	step := installStep(m.t, spec)
	paths := strings.NewReplacer(podspec.HomeDir, m.home, Dir, m.envDir, podfiles.Dir, m.files, "/srv/src/current", m.repo)
	cmd := exec.Command(step.Command[0], step.Command[1:]...)
	cmd.Env = []string{"PATH=" + filepath.Join(m.root, "bin") + ":/usr/bin:/bin:/sbin", "TMPDIR=" + m.root,
		"JAVO_TERMINATION_LOG=" + filepath.Join(m.root, "termination-log")}
	for _, e := range step.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+paths.Replace(e.Value))
	}
	return cmd.CombinedOutput()
}

func (m *machine) installs() []string {
	b, err := os.ReadFile(m.calls)
	if os.IsNotExist(err) {
		return nil
	}
	m.must(err)
	return strings.Fields(string(b))
}

func (m *machine) read(p string) string {
	b, err := os.ReadFile(p)
	m.must(err)
	return string(b)
}

// An environment installs into the home under the hash of its files, with
// its activation, and the marker written last; the next start (of this app or
// any other with the same files) reuses it without installing again.
func TestAnEnvironmentInstallsOnceIntoTheHomeAndIsReused(t *testing.T) {
	m := newMachine(t)
	dir := m.run(Source{Default: "streamlit"})
	if filepath.Dir(dir) != filepath.Join(m.home, ".javo", "envs") || len(filepath.Base(dir)) != 64 {
		t.Fatalf("installed at %s, want ~/.javo/envs/<sha256>", dir)
	}
	if got := m.installs(); len(got) != 1 || got[0] != dir {
		t.Fatalf("pixi install ran in %v, want once in %s", got, dir)
	}
	if m.read(filepath.Join(dir, "pixi.lock")) != "streamlit-lock" {
		t.Fatal("the bundled lockfile was not what was installed")
	}
	for _, f := range []string{".ready", "activate.sh"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	if !strings.Contains(m.read(filepath.Join(dir, "activate.sh")), dir+"/.pixi/envs/default/bin:$PATH") {
		t.Fatalf("the activation does not put the environment first on the pod's own PATH:\n%s", m.read(filepath.Join(dir, "activate.sh")))
	}
	tool, err := os.ReadFile(filepath.Join(m.root, "tool-calls"))
	if err != nil || strings.TrimSpace(string(tool)) != "clear-execstack "+dir+"/.pixi/envs/default installed" {
		t.Fatalf("the installed libraries were not cleared of executable-stack requests: %q, %v", tool, err)
	}
	if again := m.run(Source{Default: "streamlit"}); again != dir || len(m.installs()) != 1 {
		t.Fatalf("second start: %s, installs %v; want the same folder, no new install", again, m.installs())
	}
	m.bundle("panel", "panel-lock")
	if other := m.run(Source{Default: "panel"}); other == dir || len(m.installs()) != 2 {
		t.Fatalf("another lockfile went to %s (installs %v)", other, m.installs())
	}
}

// A folder left by an install that crashed has no marker: it is installed
// again from scratch, never used.
func TestAHalfInstalledEnvironmentIsInstalledAgain(t *testing.T) {
	m := newMachine(t)
	dir := m.run(Source{Default: "streamlit"})
	m.must(os.Remove(filepath.Join(dir, ".ready")))
	m.write(filepath.Join(dir, "leftover"), "x", 0o644)
	if again := m.run(Source{Default: "streamlit"}); again != dir {
		t.Fatalf("reinstalled at %s, want %s", again, dir)
	}
	if len(m.installs()) != 2 {
		t.Fatalf("installs %v, want a second one", m.installs())
	}
	if _, err := os.Stat(filepath.Join(dir, "leftover")); !os.IsNotExist(err) {
		t.Fatal("the half-installed folder was reused, not cleared")
	}
}

// A failed install leaves no marker and fails the step.
func TestAFailedInstallIsNotMarkedReady(t *testing.T) {
	m := newMachine(t)
	m.write(filepath.Join(m.files, "envs", "streamlit", "fail"), "", 0o644)
	spec := appPod()
	m.must(Apply(spec, "example.com/pixi:1", podspec.Image{Ref: "example.com/javo:1"}, Source{Default: "streamlit"}))
	step := installStep(t, spec)
	paths := strings.NewReplacer(podspec.HomeDir, m.home, Dir, m.envDir, podfiles.Dir, m.files)
	cmd := exec.Command(step.Command[0], step.Command[1:]...)
	cmd.Env = []string{"PATH=" + filepath.Join(m.root, "bin") + ":/usr/bin:/bin:/sbin", "TMPDIR=" + m.root}
	for _, e := range step.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+paths.Replace(e.Value))
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("a failed pixi install did not fail the step")
	}
	ready, _ := filepath.Glob(filepath.Join(m.home, ".javo", "envs", "*", ".ready"))
	if len(ready) != 0 {
		t.Fatalf("marked ready: %v", ready)
	}
}

// Precedence: a picked environment, else the repository's pixi.toml and
// pixi.lock, else the template's.
func TestAPickBeatsTheRepositoryWhichBeatsTheTemplate(t *testing.T) {
	m := newMachine(t)
	repo := Source{Default: "streamlit", RepoDir: "/srv/src/current"}
	if got := m.read(filepath.Join(m.run(repo), "pixi.lock")); got != "streamlit-lock" {
		t.Fatalf("a repository without a lockfile installed %q, want the template's", got)
	}
	m.write(filepath.Join(m.repo, "pixi.toml"), "[workspace]\n", 0o644)
	m.write(filepath.Join(m.repo, "pixi.lock"), "repo-lock", 0o644)
	if got := m.read(filepath.Join(m.run(repo), "pixi.lock")); got != "repo-lock" {
		t.Fatalf("installed %q, want the repository's lockfile", got)
	}
	picked := Source{Files: &files, Default: "streamlit", RepoDir: "/srv/src/current"}
	dir := m.run(picked)
	if m.read(filepath.Join(dir, "pixi.lock")) != files.Lock || m.read(filepath.Join(dir, "pixi.toml")) != files.Manifest {
		t.Fatal("the picked environment was not what was installed")
	}
}

// The step sees the home exactly as the app does (a preview's scratch home
// stays scratch), and the app sees the activation file.
func TestTheStepMountsTheAppsHome(t *testing.T) {
	spec := appPod()
	spec.Containers[0].VolumeMounts[0].Name = podspec.HomeScratchVolume
	if err := Apply(spec, "example.com/pixi:1", podspec.Image{Ref: "example.com/javo:1"}, Source{Default: "streamlit"}); err != nil {
		t.Fatal(err)
	}
	home := false
	for _, mt := range installStep(t, spec).VolumeMounts {
		home = home || (mt.MountPath == podspec.HomeDir && mt.Name == podspec.HomeScratchVolume && mt.SubPath == "alice")
	}
	sees := false
	for _, mt := range spec.Containers[0].VolumeMounts {
		sees = sees || mt.MountPath == Dir
	}
	if !home || !sees {
		t.Fatalf("step mounts the app's home: %v, app sees %s: %v", home, Dir, sees)
	}
}

// The platform's binary reaches the install step through the activation
// folder, copied by a step that runs the platform's image first.
func TestTheToolsStepCopiesThePlatformsBinaryFirst(t *testing.T) {
	spec := appPod()
	if err := Apply(spec, "example.com/pixi:1", podspec.Image{Ref: "example.com/javo:1"}, Source{Default: "streamlit"}); err != nil {
		t.Fatal(err)
	}
	if n := len(spec.InitContainers); n != 2 || spec.InitContainers[0].Name != ToolsContainer || spec.InitContainers[1].Name != InstallContainer {
		t.Fatalf("init containers: %+v", spec.InitContainers)
	}
	tools := spec.InitContainers[0]
	if tools.Image != "example.com/javo:1" || !strings.HasSuffix(strings.Join(tools.Args, " "), "--to "+Dir+"/"+model.ProductName) {
		t.Fatalf("tools step: %s %v %v", tools.Image, tools.Command, tools.Args)
	}
}

// A private platform image brings its pull secrets to the pod.
func TestTheToolsStepPullsAPrivateImage(t *testing.T) {
	spec := appPod()
	if err := Apply(spec, "example.com/pixi:1", podspec.Image{Ref: "example.com/javo:1", PullSecrets: []string{"ghcr"}}, Source{Default: "streamlit"}); err != nil {
		t.Fatal(err)
	}
	if len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != "ghcr" {
		t.Fatalf("pull secrets: %v", spec.ImagePullSecrets)
	}
}

func TestNothingToInstallAddsNothing(t *testing.T) {
	spec := appPod()
	if err := Apply(spec, "", podspec.Image{Ref: "example.com/javo:1"}, Source{RepoDir: "/srv/src/current"}); err != nil || len(spec.InitContainers) != 0 {
		t.Fatalf("a repository alone, on a template with no environment: %v, %+v", err, spec.InitContainers)
	}
}

func TestAnEnvironmentTooLargeForThePodIsRefused(t *testing.T) {
	big := model.EnvironmentFiles{Manifest: "x", Lock: randomText(MaxEncoded * 2)}
	if err := Apply(appPod(), "example.com/pixi:1", podspec.Image{Ref: "example.com/javo:1"}, Source{Files: &big}); err == nil {
		t.Fatal("a lockfile larger than a pod may carry was accepted")
	}
	if err := Apply(appPod(), "", podspec.Image{Ref: "example.com/javo:1"}, Source{Files: &files}); err == nil {
		t.Fatal("an environment was accepted with no image to install it")
	}
}

// randomText does not compress, so its encoded size is about its length.
func randomText(n int) string {
	b := make([]byte, n)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = 'a' + byte(x%26)
	}
	return string(b)
}

// withBins is files whose environment provides the named programs (the
// stand-in pixi makes them).
func withBins(bins ...string) *model.EnvironmentFiles {
	f := files
	for _, b := range bins {
		f.Manifest += "# bin " + b + "\n"
	}
	return &f
}

// On a template that serves kernels, a picked environment is installed
// beside the template's, which still runs the server; the platform's binary
// then offers the picked one's kernels to Jupyter, and terminals can enter it
// by name.
func TestAPickedEnvironmentBesideTheTemplatesIsOfferedAsKernels(t *testing.T) {
	m := newMachine(t)
	m.bundle("jupyterlab", "lab-lock")
	src := Source{Files: withBins("python"), Default: "jupyterlab", Name: "smoke-pushed", From: "Nebi", Beside: true}
	server := m.run(src)
	if m.read(filepath.Join(server, "pixi.lock")) != "lab-lock" {
		t.Fatal("the server does not run in the template's environment")
	}
	if len(m.installs()) != 2 {
		t.Fatalf("installs %v, want the template's and the picked one", m.installs())
	}
	picked := m.installs()[1]
	if m.read(filepath.Join(picked, "pixi.lock")) != files.Lock {
		t.Fatal("the picked environment was not installed")
	}
	want := "env-kernels --prefix " + picked + "/.pixi/envs/default --activate " + picked + "/activate.sh --name smoke-pushed --to " + m.envDir + "/jupyter"
	if !strings.Contains(m.read(filepath.Join(m.root, "tool-calls")), want) {
		t.Fatalf("kernels not offered; tool calls:\n%s\nwant %q", m.read(filepath.Join(m.root, "tool-calls")), want)
	}
	if !strings.Contains(m.read(filepath.Join(m.envDir, ActivateFile)), `JUPYTER_PATH="`+m.envDir+"/jupyter") {
		t.Fatalf("the server does not look for the kernels:\n%s", m.read(filepath.Join(m.envDir, ActivateFile)))
	}
	if got := strings.TrimSpace(m.read(filepath.Join(m.envDir, "envs", "smoke-pushed"))); got != ". "+picked+"/activate.sh" {
		t.Fatalf("named environment: %q", got)
	}
}

// A picked environment that replaces the template's must bring the program
// the template starts: without it the step fails with a plain reason in its
// termination message, instead of the app crashing on a shell error.
func TestAPickedEnvironmentWithoutTheProgramFailsThePlainWay(t *testing.T) {
	m := newMachine(t)
	out, err := m.step(Source{Files: withBins("python"), Default: "streamlit", Name: "smoke-pushed", From: "Nebi", Program: "streamlit"})
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != ExitProgramMissing {
		t.Fatalf("step: %v, want exit %d\n%s", err, ExitProgramMissing, out)
	}
	want := "The environment smoke-pushed has no streamlit. Add streamlit to it in Nebi, or pick another environment."
	if got := m.read(filepath.Join(m.root, "termination-log")); got != want {
		t.Fatalf("termination message %q, want %q", got, want)
	}
	if dir := m.run(Source{Files: withBins("python", "streamlit"), Default: "streamlit", Name: "smoke-pushed", From: "Nebi", Program: "streamlit"}); m.read(filepath.Join(dir, "pixi.lock")) != files.Lock {
		t.Fatal("an environment with the program did not start")
	}
}

// A repository's lockfile is checked the same way, with words for it.
func TestARepositoryLockfileWithoutTheProgramFailsThePlainWay(t *testing.T) {
	m := newMachine(t)
	m.write(filepath.Join(m.repo, "pixi.toml"), "[workspace]\n", 0o644)
	m.write(filepath.Join(m.repo, "pixi.lock"), "repo-lock", 0o644)
	if _, err := m.step(Source{Default: "streamlit", RepoDir: "/srv/src/current", Program: "streamlit"}); err == nil {
		t.Fatal("a repository lockfile without the program started")
	}
	want := "The repository's pixi.lock has no streamlit. Add streamlit to it, or remove pixi.toml and pixi.lock from the repository."
	if got := m.read(filepath.Join(m.root, "termination-log")); got != want {
		t.Fatalf("termination message %q, want %q", got, want)
	}
}

// Without a terminal pixi prints two lines for a whole install; the step
// asks it for a line per package (conda downloads and PyPI wheels), so the
// app's start-up log shows the install moving.
func TestTheInstallLogsEachPackage(t *testing.T) {
	m := newMachine(t)
	m.run(Source{Default: "streamlit"})
	got := strings.TrimSpace(m.read(m.calls + ".log"))
	for _, want := range []string{"info", "rattler_cache=debug", "uv_installer=debug", "uv_distribution=debug"} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Fatalf("pixi install ran with RUST_LOG %q, want it to include %s", got, want)
		}
	}
}
