package helm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testImage names the image for every other render: the chart refuses to
// render without one (TestChartNeedsAnImage).
var testImage = []string{"--set", "image.tag=test"}

// Without a digest or an explicit tag the chart does not render, so an
// install that forgets the image cannot pull whatever a registry or a node
// holds under a default name.
func TestChartNeedsAnImage(t *testing.T) {
	t.Parallel()
	out, err := render(t, "--set", "image.tag=")
	if err == nil || !strings.Contains(out, "image.digest") {
		t.Fatalf("err %v, want a failure naming image.digest\n%s", err, out)
	}
}

// The release writes the released image's digest into the chart it
// packages, so the packaged chart, installed with no image values at all,
// pulls that image by digest everywhere, the backup job template included.
func TestReleasedChartPullsTheImageByDigest(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("needs helm; skipped with -short")
	}
	dir := filepath.Join(t.TempDir(), "javo")
	if err := os.CopyFS(dir, os.DirFS(chart)); err != nil {
		t.Fatal(err)
	}
	const repo = "registry.example/javo"
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	script := filepath.Join("..", "..", "scripts", "release", "set-chart-release.sh")
	if out, err := exec.Command(script, dir, "1.2.3", repo, digest).CombinedOutput(); err != nil {
		t.Fatalf("set-chart-release: %v\n%s", err, out)
	}
	args := append([]string{"template", "javo", dir, "--namespace", "javo-system"}, vapAPI...)
	args = append(args, bucketArgs("sqlite")...)
	b, err := exec.Command("helm", args...).CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, repo+"@"+digest) {
		t.Errorf("no image %s@%s in the render", repo, digest)
	}
	if strings.Contains(out, repo+":") {
		t.Errorf("the render names %s by tag", repo)
	}
	if !strings.Contains(renderedTemplates(t, out)["backup"].Object["spec"].(map[string]any)["image"].(string), "@"+digest) {
		t.Error("the backup job template does not name the image by digest")
	}
}
