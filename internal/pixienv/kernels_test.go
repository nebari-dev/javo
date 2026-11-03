package pixienv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeKernel(t *testing.T, prefix, name string, spec map[string]any) {
	t.Helper()
	dir := filepath.Join(prefix, "share", "jupyter", "kernels", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(dir, "kernel.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logo-64x64.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readKernel(t *testing.T, to, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(to, "kernels", name, "kernel.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// Each kernel an environment installs is offered under the environment's
// name, started in that environment's activation, with its other files.
func TestAnEnvironmentsKernelsAreOfferedUnderItsName(t *testing.T) {
	prefix, to, notice := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "notice")
	writeKernel(t, prefix, "python3", map[string]any{
		"argv": []string{"python", "-m", "ipykernel_launcher", "-f", "{connection_file}"}, "display_name": "Python 3 (ipykernel)", "language": "python",
	})
	if err := RegisterKernels(prefix, "/home/user/.javo/envs/abc/activate.sh", "smoke pushed", to, notice); err != nil {
		t.Fatal(err)
	}
	spec := readKernel(t, to, "javo-smoke-pushed-python3")
	if spec["display_name"] != "smoke pushed" || spec["language"] != "python" {
		t.Fatalf("spec: %v", spec)
	}
	want := []any{"/bin/sh", "-c", `. "$0" && exec "$@"`, "/home/user/.javo/envs/abc/activate.sh", "python", "-m", "ipykernel_launcher", "-f", "{connection_file}"}
	if !reflect.DeepEqual(spec["argv"], want) {
		t.Fatalf("argv %v, want %v", spec["argv"], want)
	}
	if _, err := os.Stat(filepath.Join(to, "kernels", "javo-smoke-pushed-python3", "logo-64x64.png")); err != nil {
		t.Fatal("the kernel's logo was not copied")
	}
	if _, err := os.Stat(notice); !os.IsNotExist(err) {
		t.Fatal("a notice was written for an environment with a kernel")
	}
}

// With several kernels each name says which it is.
func TestSeveralKernelsSayWhichTheyAre(t *testing.T) {
	prefix, to := t.TempDir(), t.TempDir()
	writeKernel(t, prefix, "python3", map[string]any{"argv": []string{"python"}, "display_name": "Python 3"})
	writeKernel(t, prefix, "ir", map[string]any{"argv": []string{"R"}, "display_name": "R"})
	if err := RegisterKernels(prefix, "/a.sh", "mixed", to, ""); err != nil {
		t.Fatal(err)
	}
	if got := readKernel(t, to, "javo-mixed-ir")["display_name"]; got != "mixed (R)" {
		t.Fatalf("display name %v", got)
	}
}

// An environment without a kernel is still installed; the person is told in
// plain words why JupyterLab does not offer it.
func TestAnEnvironmentWithoutAKernelLeavesANotice(t *testing.T) {
	prefix, to, notice := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "notice")
	if err := RegisterKernels(prefix, "/a.sh", "smoke-pushed", to, notice); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(notice)
	want := "The environment smoke-pushed has no Jupyter kernel, so JupyterLab does not offer it. Add ipykernel (or another kernel) to it, then restart the app."
	if err != nil || string(b) != want {
		t.Fatalf("notice %q, %v; want %q", b, err, want)
	}
}

// The program an app starts is the first word of its container's command
// after pod-user.sh; a shell script names no single program.
func TestProgramOf(t *testing.T) {
	cases := []struct {
		cmd, args []string
		want      string
	}{
		{[]string{"/opt/javo/pod-user.sh", "streamlit"}, []string{"run", "app.py"}, "streamlit"},
		{[]string{"/opt/javo/pod-user.sh"}, []string{"python", "sample.py"}, "python"},
		{[]string{"/opt/javo/pod-user.sh", "sh", "-c", "exec jupyter-lab"}, nil, ""},
		{[]string{"/opt/javo/pod-user.sh", "bash", "-lc", "x"}, nil, ""},
		{nil, nil, ""},
		{[]string{"panel"}, []string{"serve"}, "panel"},
	}
	for _, c := range cases {
		if got := ProgramOf(c.cmd, c.args); got != c.want {
			t.Errorf("ProgramOf(%v, %v) = %q, want %q", c.cmd, c.args, got, c.want)
		}
	}
}
