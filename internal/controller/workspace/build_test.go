package workspace

import (
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/nebari-dev/javo/internal/homepath"
	"github.com/nebari-dev/javo/internal/imageref"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

func fixtures() (*model.App, *model.Template) {
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "sales-app", Owner: "alice", Template: "streamlit", File: "sales/app.py"}
	t := &model.Template{
		Name: "streamlit",
		Spec: model.TemplateSpec{
			Kind:          "streamlit",
			Image:         "example.com/streamlit:1",
			Port:          8501,
			Command:       []string{"streamlit"},
			Args:          []string{"run", "{{.File}}", "--server.baseUrlPath", "{{.BaseURL}}", "--server.port", "{{.Port}}"},
			ReadinessPath: "{{.BaseURL}}_stcore/health",
		},
	}
	return ws, t
}

func TestBuildPodRendersTemplates(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/alice-1a2b3c4d/sales-app/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	want := []string{"run", "/home/user/sales/app.py", "--server.baseUrlPath", "/u/alice-1a2b3c4d/sales-app/", "--server.port", "8501"}
	if strings.Join(c.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %v", c.Args)
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Fatal("no readiness probe")
	}
	g := c.ReadinessProbe.HTTPGet
	if g.Path != "/u/alice-1a2b3c4d/sales-app/_stcore/health" || g.Port != intstr.FromInt32(8501) {
		t.Fatalf("probe = %+v", g)
	}
}

func TestBuildPodUnknownFieldNamesTemplateAndField(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	tpl.Spec.Args = []string{"{{.Nope}}"}
	pod, err := BuildPod(ws, tpl, "/u/x/y/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err == nil || pod != nil {
		t.Fatalf("want error and nil pod, got %v %v", pod, err)
	}
	if !strings.Contains(err.Error(), "streamlit") || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("error does not name template and field: %v", err)
	}
}

func TestBuildPodHardening(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	slug := "alice"
	if pod.Name != ws.ObjectName() || pod.Namespace != "javo-apps" {
		t.Fatalf("meta = %s/%s", pod.Namespace, pod.Name)
	}
	if pod.Labels[model.LabelApp] != "app-1" || pod.Labels[model.LabelOwner] != slug {
		t.Fatalf("labels = %v", pod.Labels)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("service account token must not be mounted")
	}
	psc := pod.Spec.SecurityContext
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || *psc.RunAsUser != 1000 || *psc.RunAsGroup != 100 ||
		psc.FSGroup != nil || psc.FSGroupChangePolicy != nil ||
		psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("pod security context = %+v", psc)
	}
	sc := pod.Spec.Containers[0].SecurityContext
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" ||
		*sc.AllowPrivilegeEscalation || *sc.ReadOnlyRootFilesystem {
		t.Fatalf("container security context = %+v", sc)
	}
	mounts := map[string]string{}
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		mounts[m.MountPath] = m.Name
	}
	vols := map[string]corev1.Volume{}
	for _, v := range pod.Spec.Volumes {
		vols[v.Name] = v
	}
	home := vols[mounts["/home/user"]]
	if home.PersistentVolumeClaim == nil || home.PersistentVolumeClaim.ClaimName != naming.HomeClaimName(slug) {
		t.Fatalf("home volume = %+v", home)
	}
	if vols[mounts["/tmp"]].EmptyDir == nil {
		t.Fatal("/tmp must be an emptyDir")
	}
}

func TestBuildServiceSelectsTheApp(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	svc := BuildService(ws, tpl)
	if svc.Name != ws.ObjectName() || svc.Namespace != "javo-apps" {
		t.Fatalf("meta = %s/%s", svc.Namespace, svc.Name)
	}
	if svc.Spec.Selector[model.LabelApp] != "app-1" {
		t.Fatalf("selector = %v", svc.Spec.Selector)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 8501 || svc.Spec.Ports[0].TargetPort != intstr.FromInt32(8501) {
		t.Fatalf("ports = %+v", svc.Spec.Ports)
	}
	if svc.Labels[model.LabelOwner] != "alice" {
		t.Fatalf("labels = %v", svc.Labels)
	}
}

func TestBuildPodFileCannotEscapeHome(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"../../etc/passwd": "/home/user/etc/passwd",
		"sales/../app.py":  "/home/user/app.py",
		"sales/app.py":     "/home/user/sales/app.py",
	}
	for in, want := range cases {
		ws, tpl := fixtures()
		ws.File = in
		tpl.Spec.Args = []string{"{{.File}}"}
		pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := pod.Spec.Containers[0].Args[0]; got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestBuildPodRejectsControlCharsInFile(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"a\nb.py", "a\x00b.py", "a\rb.py", "a\u0085b.py", "a\xffb.py", "a\u2028b.py"} {
		ws, tpl := fixtures()
		ws.File = f
		pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
		if err == nil || pod != nil {
			t.Fatalf("%q: want error", f)
		}
		if !strings.Contains(err.Error(), "spec.file") || !errors.Is(err, homepath.ErrUnsafe) {
			t.Fatalf("%q: error = %v", f, err)
		}
	}
}

func sampleTemplate() *model.Template {
	_, tpl := fixtures()
	tpl.Spec.Sample = &model.TemplateSample{Path: "/opt/javo/samples/streamlit", Open: "app.py", Title: "Sales explorer"}
	return tpl
}

func TestBuildPodSampleCopiesIntoHomeBeforeStart(t *testing.T) {
	t.Parallel()
	ws, _ := fixtures()
	tpl := sampleTemplate()
	ws.Sample = true
	ws.File = SampleFileOf(tpl)
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	var cp *corev1.Container
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == "sample-copy" {
			cp = &pod.Spec.InitContainers[i]
		}
	}
	if cp == nil {
		t.Fatalf("no sample-copy init container: %+v", pod.Spec.InitContainers)
	}
	if cp.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
		t.Fatalf("termination message policy = %q", cp.TerminationMessagePolicy)
	}
	if cp.Image != tpl.Spec.Image {
		t.Fatalf("image = %s", cp.Image)
	}
	// -T copies the folder as the target, which BusyBox cp (nginx images)
	// does too; its -n skips a target "/." that exists, so the sample-dir
	// step makes only the parent.
	if got := strings.Join(cp.Command, "|"); got != "cp|-rnT|/opt/javo/samples/streamlit|/home/user/samples/streamlit" {
		t.Fatalf("argv = %q", got)
	}
	for _, c := range pod.Spec.InitContainers {
		if c.Name == "sample-dir" && strings.Join(c.Command, "|") != "mkdir|-p|/home/user/samples" {
			t.Fatalf("sample-dir argv = %q", c.Command)
		}
	}
	if len(cp.VolumeMounts) != 1 || cp.VolumeMounts[0].MountPath != "/home/user" {
		t.Fatalf("mounts = %+v", cp.VolumeMounts)
	}
	sc := cp.SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("security context = %+v", sc)
	}
	if psc := pod.Spec.SecurityContext; psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Fatal("pod must run as non-root")
	}
	args := strings.Join(pod.Spec.Containers[0].Args, "|")
	if !strings.Contains(args, "/home/user/samples/streamlit/app.py") {
		t.Fatalf("app args = %s", args)
	}
}

// A template with pod files gets them (copied by the platform's image) and
// the identity before anything else runs; the sample is then copied from
// them.
func TestBuildPodGivesThePodFilesBeforeTheSample(t *testing.T) {
	t.Parallel()
	ws, _ := fixtures()
	tpl := sampleTemplate()
	tpl.Spec.PodFiles = true
	ws.Sample = true
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != podfiles.CopyContainer+","+podfiles.IdentityContainer+",sample-dir,sample-copy" {
		t.Fatalf("init containers = %s", got)
	}
	if img := pod.Spec.InitContainers[0].Image; img != testImages.Runner.Ref {
		t.Fatalf("the files are copied by %q, want the platform's image", img)
	}
	cp := pod.Spec.InitContainers[3]
	if !slices.ContainsFunc(cp.VolumeMounts, func(m corev1.VolumeMount) bool { return m.MountPath == podfiles.Dir && m.ReadOnly }) {
		t.Fatalf("the sample copy does not see the pod files: %+v", cp.VolumeMounts)
	}
}

func TestBuildPodSampleFalseIsUnchanged(t *testing.T) {
	t.Parallel()
	ws, _ := fixtures()
	pod, err := BuildPod(ws, sampleTemplate(), "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.InitContainers) != 0 {
		t.Fatalf("init containers = %+v", pod.Spec.InitContainers)
	}
}

func TestBuildPodSampleWithoutTemplateSampleIsInvalid(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	ws.Sample = true
	if pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages); err == nil || pod != nil {
		t.Fatalf("want error, got %v %v", pod, err)
	}
}

func TestSampleFile(t *testing.T) {
	t.Parallel()
	tpl := sampleTemplate()
	if got := SampleFileOf(tpl); got != "samples/streamlit/app.py" {
		t.Fatalf("file kind = %q", got)
	}
	tpl.Spec.Sample.Open = ""
	if got := SampleFileOf(tpl); got != "samples/streamlit" {
		t.Fatalf("folder kind = %q", got)
	}
}

func TestBuildPodDropsArgsThatRenderEmpty(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	ws.File = ""
	tpl.Spec.Args = []string{"run", "--x={{.Port}}", "{{if .File}}--f={{.File}}{{end}}"}
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pod.Spec.Containers[0].Args, "|"); got != "run|--x=8501" {
		t.Fatalf("args = %q", got)
	}
}

// Apps that check the websocket Origin (Bokeh, Panel) are told the public
// host the browser uses; without one configured the argument is dropped.
func TestBuildPodRendersPublicHost(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	tpl.Spec.Args = []string{"{{if .Host}}--allow-websocket-origin={{.Host}}{{end}}"}
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "javo.example.com", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pod.Spec.Containers[0].Args, "|"); got != "--allow-websocket-origin=javo.example.com" {
		t.Fatalf("args = %q", got)
	}
	pod, err = BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := pod.Spec.Containers[0].Args; len(got) != 0 {
		t.Fatalf("args = %q, want none", got)
	}
}

// Apps that take a module and folder (uvicorn) or a prefix without the
// trailing slash (Starlette's root_path) get them from the template functions
// dir, base and trimSuffix.
func TestBuildPodTemplateFunctions(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	ws.File = "api/main.py"
	tpl.Spec.Args = []string{"--app-dir", "{{dir .File}}", `{{base .File | trimSuffix ".py"}}:app`, `--root-path={{trimSuffix "/" .BaseURL}}`}
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	want := "--app-dir|/home/user/api|main:app|--root-path=/u/a/b"
	if got := strings.Join(pod.Spec.Containers[0].Args, "|"); got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

// A size sets the app container's CPU and memory as both requests and
// limits; other template resources stay, and the template is not changed.
func TestBuildPodSizeOverridesCPUAndMemory(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	tpl.Spec.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("1Gi")},
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
	}
	medium := sizes.Size{Name: "medium", DisplayName: "Medium", CPU: resource.MustParse("4"), Memory: resource.MustParse("8Gi")}
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), medium, testImages)
	if err != nil {
		t.Fatal(err)
	}
	r := pod.Spec.Containers[0].Resources
	for name, list := range map[string]corev1.ResourceList{"requests": r.Requests, "limits": r.Limits} {
		if !list.Cpu().Equal(medium.CPU) || !list.Memory().Equal(medium.Memory) {
			t.Errorf("%s = cpu %s memory %s, want 4 and 8Gi", name, list.Cpu(), list.Memory())
		}
	}
	if got := r.Requests[corev1.ResourceEphemeralStorage]; !got.Equal(resource.MustParse("1Gi")) {
		t.Errorf("ephemeral-storage request = %s, want the template's 1Gi", got.String())
	}
	if pod.Annotations[model.AnnotationSize] != "medium" {
		t.Errorf("size annotation = %q", pod.Annotations[model.AnnotationSize])
	}
	if got := tpl.Spec.Resources.Requests[corev1.ResourceCPU]; !got.Equal(resource.MustParse("250m")) {
		t.Errorf("template changed: cpu request %s", got.String())
	}
}

// A GPU size's app pod asks for the GPU and lands on the size's GPU nodes,
// tolerating their taint.
func TestBuildPodGPUSizePlacesThePod(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{Name: "gpu", DisplayName: "GPU", CPU: resource.MustParse("4"), Memory: resource.MustParse("16Gi"),
		GPU: 1, GPUResource: sizes.DefaultGPUResource, NodeSelector: map[string]string{"pool": "gpu"},
		Tolerations: []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if g := pod.Spec.Containers[0].Resources.Limits[sizes.DefaultGPUResource]; g.Cmp(resource.MustParse("1")) != 0 {
		t.Errorf("gpu limit %s", g.String())
	}
	if pod.Spec.NodeSelector["pool"] != "gpu" || len(pod.Spec.Tolerations) != 1 || pod.Spec.Tolerations[0].Key != "nvidia.com/gpu" {
		t.Errorf("placement: selector %v, tolerations %+v", pod.Spec.NodeSelector, pod.Spec.Tolerations)
	}
}

// The zero Size (no sizes configured) keeps the template's resources.
func TestBuildPodZeroSizeKeepsTemplateResources(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	tpl.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")}}
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	r := pod.Spec.Containers[0].Resources
	if !r.Limits.Memory().Equal(resource.MustParse("2Gi")) || len(r.Requests) != 0 {
		t.Fatalf("resources = %+v", r)
	}
	if _, ok := pod.Annotations[model.AnnotationSize]; ok {
		t.Fatal("size annotation set without a size")
	}
}

func TestBuildPodRunsAsTheGivenUID(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(100500), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := *pod.Spec.SecurityContext.RunAsUser; got != 100500 {
		t.Fatalf("runAsUser = %d", got)
	}
}

func envOf(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// customFixtures is an app on a template that requires a typed command.
func customFixtures(command string) (*model.App, *model.Template) {
	ws := &model.App{ID: "app-2", Namespace: "javo-apps", Name: "clock", Owner: "alice", Template: "custom", Command: command}
	t := &model.Template{
		Name: "custom",
		Spec: model.TemplateSpec{
			Kind: "custom", Image: "example.com/python:1", Port: 8080,
			UserCommand: model.UserCommandRequired, ReadinessPath: "{{.BaseURL}}",
		},
	}
	return ws, t
}

// The typed command is split like a shell would and rendered like template
// args, then replaces the args; no shell runs it.
func TestBuildPodRunsTheTypedCommand(t *testing.T) {
	t.Parallel()
	ws, tpl := customFixtures(`python "my app.py" --port={{.Port}} --base-url={{.BaseURL}} --data '{{.Home}}/data'`)
	pod, err := BuildPod(ws, tpl, "/u/alice/clock/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	want := []string{"python", "my app.py", "--port=8080", "--base-url=/u/alice/clock/", "--data", "/home/user/data"}
	if c.Command != nil || strings.Join(c.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("command = %q, args = %q, want args %q", c.Command, c.Args, want)
	}
	if v, ok := envOf(c, "PORT"); !ok || v != "8080" {
		t.Fatalf("PORT = %q, %v", v, ok)
	}
	// Without a chosen folder the command runs in the home folder.
	if c.WorkingDir != "/home/user" {
		t.Fatalf("workingDir = %q", c.WorkingDir)
	}
}

// A template's own command (an entrypoint) still runs; the typed command
// takes the place of its args, as with docker run IMAGE COMMAND.
func TestBuildPodTypedCommandKeepsTemplateEntrypoint(t *testing.T) {
	t.Parallel()
	ws, tpl := customFixtures("serve --port={{.Port}}")
	tpl.Spec.Command = []string{"tini", "--"}
	tpl.Spec.Args = []string{"default"}
	pod, err := BuildPod(ws, tpl, "/u/alice/clock/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if strings.Join(c.Command, "|") != "tini|--" || strings.Join(c.Args, "|") != "serve|--port=8080" {
		t.Fatalf("command = %q, args = %q", c.Command, c.Args)
	}
}

// A typed command runs in the folder of the chosen file, whatever the
// template's file types.
func TestBuildPodTypedCommandRunsInTheChosenFilesFolder(t *testing.T) {
	t.Parallel()
	for _, exts := range [][]string{nil, {".py"}} {
		ws, tpl := customFixtures("python app.py")
		tpl.Spec.FileExtensions = exts
		ws.File = "projects/clock/app.py"
		pod, err := BuildPod(ws, tpl, "/u/alice/clock/", "", testLayout(FixedUID), sizes.Size{}, testImages)
		if err != nil {
			t.Fatal(err)
		}
		if got := pod.Spec.Containers[0].WorkingDir; got != "/home/user/projects/clock" {
			t.Fatalf("extensions %v: workingDir = %q", exts, got)
		}
	}
}

// Apps without a typed command are built as before: no PORT, no working dir.
func TestBuildPodWithoutTypedCommandIsUnchanged(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if _, ok := envOf(c, "PORT"); ok || c.WorkingDir != "" {
		t.Fatalf("env = %v, workingDir = %q", c.Env, c.WorkingDir)
	}
}

func TestBuildPodRefusesAnUnreadableCommand(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"echo $HOME", `python "app.py`, "python app.py --x={{.Nope}}"} {
		ws, tpl := customFixtures(cmd)
		if _, err := BuildPod(ws, tpl, "/u/alice/clock/", "", testLayout(FixedUID), sizes.Size{}, testImages); err == nil {
			t.Fatalf("command %q built", cmd)
		}
	}
}

func TestCheckCommand(t *testing.T) {
	t.Parallel()
	if err := CheckCommand("python app.py --port={{.Port}} --base-url={{.BaseURL}} --root={{.Home}} {{.File}}"); err != nil {
		t.Fatalf("placeholders refused: %v", err)
	}
	var bad *BadPlaceholder
	if err := CheckCommand("python {{.Nope}}"); !errors.As(err, &bad) {
		t.Fatalf("unknown placeholder: err = %v, want BadPlaceholder", err)
	}
	if err := CheckCommand("python {{.Port"); !errors.As(err, &bad) {
		t.Fatalf("broken placeholder: err = %v, want BadPlaceholder", err)
	}
}

// containerFixtures is an app that names its own image and port.
func containerFixtures() (*model.App, *model.Template) {
	ws := &model.App{ID: "app-3", Namespace: "javo-apps", Name: "web", Owner: "alice", Template: "container", Image: "registry.example.com/team/web:1", Port: 9000}
	t := &model.Template{
		Name: "container",
		Spec: model.TemplateSpec{
			Kind: "container", Port: 8080, UserImage: true, UserCommand: model.UserCommandOptional,
			BasePath: "Strip", ReadinessPath: "{{.BaseURL}}",
		},
	}
	return ws, t
}

// An image sample has no folder to copy: a Workspace that asks for the
// sample copy anyway is refused instead of copying the image's root.
func TestBuildPodRefusesASampleCopyFromAnImageSample(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	tpl.Spec.Sample = &model.TemplateSample{Title: "Toolbox", Image: "registry.example.com/toolbox:1"}
	ws.Sample = true
	if pod, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages); err == nil {
		t.Fatalf("built a pod with init containers %+v", pod.Spec.InitContainers)
	}
}

func TestBuildPodRunsTheChosenImage(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	pod, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if c.Image != "registry.example.com/team/web:1" {
		t.Fatalf("image = %q", c.Image)
	}
	// The image's own command and folder run unless a command is typed.
	if c.Command != nil || c.Args != nil || c.WorkingDir != "" {
		t.Fatalf("command = %q, args = %q, workingDir = %q", c.Command, c.Args, c.WorkingDir)
	}
	if len(c.Ports) != 1 || c.Ports[0].ContainerPort != 9000 {
		t.Fatalf("ports = %+v", c.Ports)
	}
	if v, _ := envOf(c, "PORT"); v != "9000" {
		t.Fatalf("PORT = %q", v)
	}
	// Ready once the port takes connections: an image may answer / with 404.
	p := c.ReadinessProbe
	if p == nil || p.HTTPGet != nil || p.TCPSocket == nil || p.TCPSocket.Port != intstr.FromInt32(9000) {
		t.Fatalf("probe = %+v", p)
	}
	// It runs as the owner, never as the user the image names: an image's
	// USER could be another person's uid, or gid 0.
	psc := pod.Spec.SecurityContext
	if psc.RunAsUser == nil || *psc.RunAsUser != 100500 || psc.RunAsGroup == nil || *psc.RunAsGroup != 100 ||
		psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Fatalf("pod security context = %+v", psc)
	}
	// A cached image is never reused without the registry checking the
	// pull: another person's private image stays theirs.
	if c.ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("pull policy = %q, want Always", c.ImagePullPolicy)
	}
	sc := c.SecurityContext
	if *sc.AllowPrivilegeEscalation || len(sc.Capabilities.Drop) != 1 || psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("container security context = %+v", sc)
	}
	svc := BuildService(ws, tpl)
	if svc.Spec.Ports[0].Port != 9000 || svc.Spec.Ports[0].TargetPort != intstr.FromInt32(9000) {
		t.Fatalf("service ports = %+v", svc.Spec.Ports)
	}
	if AppPortOf(ws, tpl) != 9000 {
		t.Fatalf("AppPort = %d", AppPortOf(ws, tpl))
	}
}

func TestBuildPodChosenImageWithACommand(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	ws.Command = "--listen=:{{.Port}} --prefix {{.BaseURL}}"
	pod, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	c := pod.Spec.Containers[0]
	if c.Command != nil || strings.Join(c.Args, "|") != "--listen=:9000|--prefix|/u/alice/web/" || c.WorkingDir != "" {
		t.Fatalf("command = %q, args = %q, workingDir = %q", c.Command, c.Args, c.WorkingDir)
	}
}

func TestBuildPodChosenImageNeedsAnImage(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	ws.Image = ""
	if _, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages); err == nil {
		t.Fatal("built without an image")
	}
}

// The port falls back to the template's when the app names none.
func TestAppPortDefaultsToTheTemplates(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	ws.Port = 0
	if got := AppPortOf(ws, tpl); got != 8080 {
		t.Fatalf("AppPort = %d", got)
	}
	// A template with its own image ignores a port on the app.
	ws2, tpl2 := fixtures()
	ws2.Port = 9999
	if got := AppPortOf(ws2, tpl2); got != 8501 {
		t.Fatalf("AppPort = %d", got)
	}
}

// What the app runs is recorded on the pod, so a change can restart it; an
// app that types nothing gets no record (pods from before keep running).
func TestRunFingerprint(t *testing.T) {
	t.Parallel()
	ws, _ := fixtures()
	if RunFingerprint(ws) != "" {
		t.Fatal("fingerprint for an app without command, image or port")
	}
	a, _ := containerFixtures()
	b := *a
	b.Port = 9001
	c := *a
	c.Command = "--x"
	if RunFingerprint(a) == "" || RunFingerprint(a) == RunFingerprint(&b) || RunFingerprint(a) == RunFingerprint(&c) {
		t.Fatal("fingerprint does not follow image, port and command")
	}
	_, tpl := containerFixtures()
	pod, err := BuildPod(a, tpl, "/u/alice/web/", "", testLayout(1), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Annotations[model.AnnotationRun] != RunFingerprint(a) {
		t.Fatalf("annotations = %v", pod.Annotations)
	}
}

func TestAPITokenReachesTheAppFromASecretAndTheGuideIsMounted(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	tpl.Spec.APIToken = true
	tpl.Spec.APIGuidePaths = []string{"/etc/claude-code/CLAUDE.md", "/home/AGENTS.md"}
	pod, err := BuildPod(ws, tpl, "/u/alice/sales-app/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithAPIToken(pod, tpl, model.APIAccessOf(ws, tpl), "sales-app-javo-api", "https://javo.example.com")
	c := pod.Spec.Containers[0]
	env := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		env[e.Name] = e
	}
	if env["JAVO_URL"].Value != "https://javo.example.com" {
		t.Fatalf("JAVO_URL %+v", env["JAVO_URL"])
	}
	tok := env["JAVO_TOKEN"]
	if tok.Value != "" || tok.ValueFrom == nil || tok.ValueFrom.SecretKeyRef == nil ||
		tok.ValueFrom.SecretKeyRef.Name != "sales-app-javo-api" || tok.ValueFrom.SecretKeyRef.Key != APITokenKey {
		t.Fatalf("JAVO_TOKEN %+v", tok)
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.MountPath] = m
	}
	for _, p := range []string{APIGuidePath, "/etc/claude-code/CLAUDE.md", "/home/AGENTS.md"} {
		m, ok := mounts[p]
		if !ok || !m.ReadOnly || m.SubPath != APIGuideKey {
			t.Fatalf("guide at %s: %+v (mounts %v)", p, m, c.VolumeMounts)
		}
	}
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == mounts[APIGuidePath].Name {
			vol = &pod.Spec.Volumes[i]
		}
	}
	// Only the guide is projected into the volume: the token is never a file.
	if vol == nil || vol.Secret == nil || vol.Secret.SecretName != "sales-app-javo-api" ||
		len(vol.Secret.Items) != 1 || vol.Secret.Items[0].Key != APIGuideKey {
		t.Fatalf("guide volume %+v", vol)
	}
}

func TestNoAPITokenWithoutTheTemplateField(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/alice/sales-app/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithAPIToken(pod, tpl, model.APIAccessOf(ws, tpl), "sales-app-javo-api", "https://javo.example.com")
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == envURL || e.Name == envToken {
			t.Fatalf("env %s on a template without apiToken", e.Name)
		}
	}
	if len(pod.Spec.Volumes) != 3 { // home, tmp and scratch
		t.Fatalf("volumes %v", pod.Spec.Volumes)
	}
}

var testImages = Images{Clone: "example.com/git-sync:1", Runner: podspec.Image{Ref: "example.com/javo:1"}, Pixi: "example.com/pixi:1"}

const exampleSHA = "0123456789abcdef0123456789abcdef01234567"

// sourceFixtures is a streamlit app that runs sales/app.py from folder web of a repository.
func sourceFixtures() (*model.App, *model.Template) {
	ws, tpl := fixtures()
	ws.File = "app.py"
	ws.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main", Path: "web"}
	return ws, tpl
}

// staticTemplate builds with node and serves with nginx, which the pod gives a
// server config of its own.
func staticTemplate(command string) *model.Template {
	t := plainStaticTemplate(command)
	t.Spec.Build.ServerConfigDir = "/etc/nginx/conf.d"
	t.Spec.Build.ServerConfig = "server {\n  listen {{.Port}};\n  root {{.ServeDir}};\n  absolute_redirect off;\n}\n"
	return t
}

// plainStaticTemplate has no server config: its image serves the folder with
// its own.
func plainStaticTemplate(command string) *model.Template {
	return &model.Template{Name: "static-site", Spec: model.TemplateSpec{
		Kind: "static", Image: "example.com/nginx:1", Port: 8080, ReadinessPath: "/",
		Build: &model.TemplateBuild{Image: "example.com/node:1", Command: command, Output: "dist", ServeDir: "/usr/share/nginx/html"},
	}}
}

func initNames(pod *corev1.Pod) []string {
	var out []string
	for _, c := range pod.Spec.InitContainers {
		out = append(out, c.Name)
	}
	return out
}

func initNamed(t *testing.T, pod *corev1.Pod, name string) corev1.Container {
	t.Helper()
	for _, c := range pod.Spec.InitContainers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no init container %s in %v", name, initNames(pod))
	return corev1.Container{}
}

func mountOf(c corev1.Container, volume string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == volume {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}

func hasHomeClaim(pod *corev1.Pod) bool {
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			return true
		}
	}
	return false
}

func TestBuildPodClonesChecksAndRunsFromTheClone(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	pod, err := BuildPod(ws, tpl, "/u/alice/sales-app/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "clone,source-check" {
		t.Fatalf("init containers = %s", got)
	}
	clone := initNamed(t, pod, "clone")
	wantArgs := []string{"--repo=https://github.com/example-org/site", "--ref=main", "--root=/srv/src", "--link=current", "--one-time", "--depth=1", "--submodules=off"}
	if clone.Image != testImages.Clone || strings.Join(clone.Args, "|") != strings.Join(wantArgs, "|") {
		t.Fatalf("clone = %s %q", clone.Image, clone.Args)
	}
	check := initNamed(t, pod, "source-check")
	wantCheck := []string{"/javo", "source-check", "--link=/srv/src/current", "--path=web", "--file=app.py"}
	if check.Image != testImages.Runner.Ref || strings.Join(check.Command, "|") != strings.Join(wantCheck, "|") {
		t.Fatalf("source-check = %s %q", check.Image, check.Command)
	}
	for _, c := range pod.Spec.InitContainers {
		if c.TerminationMessagePolicy != corev1.TerminationMessageFallbackToLogsOnError {
			t.Errorf("%s: termination message policy %q", c.Name, c.TerminationMessagePolicy)
		}
		if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
			t.Errorf("%s: not restricted", c.Name)
		}
		if mountOf(c, "src") == nil {
			t.Errorf("%s: no src mount", c.Name)
		}
	}
	app := pod.Spec.Containers[0]
	if got := strings.Join(app.Args, " "); !strings.Contains(got, "/srv/src/current/web/app.py") || strings.Contains(got, "/home/user") {
		t.Fatalf("app args = %v", app.Args)
	}
	if app.WorkingDir != "/srv/src/current/web" {
		t.Fatalf("working dir = %q", app.WorkingDir)
	}
	if mountOf(app, "src") == nil {
		t.Fatal("the app does not mount the clone")
	}
	env := map[string]string{}
	for _, e := range app.Env {
		env[e.Name] = e.Value
	}
	if env["PORT"] != "8501" || env["BASE_URL"] != "/u/alice/sales-app/" {
		t.Fatalf("env = %v", env)
	}
}

func TestBuildPodWithoutRefClonesTheDefaultBranch(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	ws.Source.Ref, ws.Source.Path, ws.File = "", "", ""
	tpl.Spec.Args = []string{"{{.Source}}|{{.File}}"}
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(initNamed(t, pod, "clone").Args, "--ref=HEAD") {
		t.Fatalf("clone args = %v", initNamed(t, pod, "clone").Args)
	}
	if got := initNamed(t, pod, "source-check").Command; slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--file") }) {
		t.Fatalf("no file named, but source-check checks one: %v", got)
	}
	if pod.Spec.Containers[0].Args[0] != "/srv/src/current|" {
		t.Fatalf("args = %v", pod.Spec.Containers[0].Args)
	}
}

func TestCloneTakesTheGitConfigOfThePlatform(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	img := testImages
	img.GitConfig = "http.sslVerify:false"
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, img)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(initNamed(t, pod, "clone").Args, "--git-config=http.sslVerify:false") {
		t.Fatalf("clone args = %v", initNamed(t, pod, "clone").Args)
	}
	plain, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(initNamed(t, plain, "clone").Args, func(a string) bool { return strings.HasPrefix(a, "--git-config") }) {
		t.Fatalf("no git config set, but clone args = %v", initNamed(t, plain, "clone").Args)
	}
}

func TestWithGitTokenMountsTheSecretOnlyIntoClone(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithGitToken(pod, ws, "alice-sales-javo-git", "3")

	clone := initNamed(t, pod, "clone")
	for _, want := range []string{"--username=x-access-token", "--password-file=/etc/javo/git/token"} {
		if !slices.Contains(clone.Args, want) {
			t.Errorf("clone args %q lack %s", clone.Args, want)
		}
	}
	if m := mountOf(clone, "git-token"); m == nil || !m.ReadOnly || m.MountPath != "/etc/javo/git" {
		t.Fatalf("clone token mount = %+v", m)
	}
	secrets := 0
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil {
			secrets++
			if v.Secret.SecretName != "alice-sales-javo-git" {
				t.Errorf("secret %q", v.Secret.SecretName)
			}
		}
	}
	if secrets != 1 {
		t.Fatalf("%d secret volumes", secrets)
	}
	for _, c := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		if c.Name != "clone" && mountOf(c, "git-token") != nil {
			t.Errorf("%s mounts the token", c.Name)
		}
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "alice-sales-javo-git" {
				t.Errorf("%s reads the token through env", c.Name)
			}
		}
	}
	if pod.Annotations[model.AnnotationGitToken] != "3" {
		t.Fatalf("annotations = %v", pod.Annotations)
	}
}

func TestWithGitTokenNeverReachesAForkPreview(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	ws.ParentID, ws.PullNumber, ws.Source.Fork = "parent-1", 7, true
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithGitToken(pod, ws, "alice-sales-javo-git", "3")
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil {
			t.Fatalf("fork preview has secret volume %s", v.Name)
		}
	}
	if slices.ContainsFunc(initNamed(t, pod, "clone").Args, func(a string) bool { return strings.HasPrefix(a, "--password-file") }) {
		t.Fatal("fork preview clone is given the token")
	}
	if _, ok := pod.Annotations[model.AnnotationGitToken]; ok {
		t.Fatal("a pod without the token must not claim a generation")
	}
}

func TestPreviewPodHasNoHomeClaim(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	ws.ParentID, ws.PullNumber = "parent-1", 7
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if hasHomeClaim(pod) {
		t.Fatal("a preview mounts the owner's home")
	}
	var home *corev1.VolumeMount
	for i, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.MountPath == "/home/user" {
			home = &pod.Spec.Containers[0].VolumeMounts[i]
		}
	}
	if home == nil {
		t.Fatal("no scratch folder at the home path")
	}
}

func TestStaticTemplateBuildsInTheClonesThenCopiesTheSite(t *testing.T) {
	t.Parallel()
	ws, _ := sourceFixtures()
	ws.File, ws.Source.Path = "", "site"
	pod, err := BuildPod(ws, staticTemplate("npm ci && npm run build"), "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "clone,source-check,build,site-copy,site-config" {
		t.Fatalf("init containers = %s", got)
	}
	build := initNamed(t, pod, "build")
	if build.Image != "example.com/node:1" || strings.Join(build.Command, "|") != "sh|-c|npm ci && npm run build" || build.WorkingDir != "/srv/src/current/site" {
		t.Fatalf("build = %s %q in %s", build.Image, build.Command, build.WorkingDir)
	}
	env := map[string]string{}
	for _, e := range build.Env {
		env[e.Name] = e.Value
	}
	if env["CI"] != "true" || env["npm_config_cache"] != "/tmp/npm" || env["JAVO_BASE_URL"] != "/u/alice/site/" || env["HOME"] != "/tmp" {
		t.Fatalf("build env = %v", env)
	}
	cp := initNamed(t, pod, "site-copy")
	if strings.Join(cp.Command, "|") != "cp|-r|/srv/src/current/site/dist/.|/usr/share/nginx/html" {
		t.Fatalf("site-copy = %q", cp.Command)
	}
	if m := mountOf(cp, "site"); m == nil || m.MountPath != "/usr/share/nginx/html" {
		t.Fatalf("site-copy site mount = %+v", m)
	}
	if m := mountOf(pod.Spec.Containers[0], "site"); m == nil || m.MountPath != "/usr/share/nginx/html" {
		t.Fatalf("app site mount = %+v", m)
	}
	if hasHomeClaim(pod) {
		t.Fatal("nginx never needs the home")
	}
}

// nginx's own config answers a folder without its trailing slash with an
// absolute redirect (http, the pod's port, no URL prefix), which sends the
// visitor off the app. The pod serves with a config that redirects relatively.
func TestStaticSiteServesWithRelativeRedirects(t *testing.T) {
	t.Parallel()
	repo, _ := sourceFixtures()
	repo.File = ""
	home := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "page", Owner: "alice", Template: "static-site", File: "pages/page.html"}
	for name, ws := range map[string]*model.App{"repo": repo, "home file": home} {
		pod, err := BuildPod(ws, staticTemplate("npm run build"), "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		init := initNamed(t, pod, "site-config")
		if init.Image != "example.com/node:1" || len(init.Command) != 6 || init.Command[0] != "sh" {
			t.Fatalf("%s: site-config = %s %q", name, init.Image, init.Command)
		}
		conf := init.Command[4]
		for _, want := range []string{"listen 8080;", "root /usr/share/nginx/html;", "absolute_redirect off;"} {
			if !strings.Contains(conf, want) {
				t.Fatalf("%s: config lacks %q:\n%s", name, want, conf)
			}
		}
		for _, c := range []corev1.Container{init, pod.Spec.Containers[0]} {
			if m := mountOf(c, "site-config"); m == nil || m.MountPath != "/etc/nginx/conf.d" {
				t.Fatalf("%s: %s site-config mount = %+v", name, c.Name, m)
			}
		}
		if init.Command[2] != siteConfigScript || init.Command[5] != "/etc/nginx/conf.d" {
			t.Fatalf("%s: site-config writes %q", name, init.Command[2])
		}
	}
}

// The server config belongs to the template: one without it keeps its image's
// own config, so no step is added and nothing is mounted over the image.
func TestTemplateWithoutServerConfigAddsNoConfigStep(t *testing.T) {
	t.Parallel()
	repo, _ := sourceFixtures()
	repo.File = ""
	home := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "page", Owner: "alice", Template: "static-site", File: "pages/page.html"}
	for name, ws := range map[string]*model.App{"repo": repo, "home file": home} {
		pod, err := BuildPod(ws, plainStaticTemplate("npm run build"), "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if slices.Contains(initNames(pod), "site-config") {
			t.Fatalf("%s: init containers = %v", name, initNames(pod))
		}
		if mountOf(pod.Spec.Containers[0], "site-config") != nil {
			t.Fatalf("%s: the app has a config mount", name)
		}
		for _, v := range pod.Spec.Volumes {
			if v.Name == "site-config" {
				t.Fatalf("%s: pod has a config volume", name)
			}
		}
	}
}

// The folder the config goes to is the template's, not nginx's.
func TestServerConfigGoesToTheTemplatesFolder(t *testing.T) {
	t.Parallel()
	ws, _ := sourceFixtures()
	ws.File = ""
	tpl := plainStaticTemplate("")
	tpl.Spec.Build.ServerConfigDir = "/etc/caddy/conf"
	tpl.Spec.Build.ServerConfig = ":{{.Port}} { root * {{.ServeDir}} }"
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	init := initNamed(t, pod, "site-config")
	if init.Command[2] != siteConfigScript || init.Command[4] != ":8080 { root * /usr/share/nginx/html }" || init.Command[5] != "/etc/caddy/conf" {
		t.Fatalf("site-config = %q", init.Command)
	}
	for _, c := range []corev1.Container{init, pod.Spec.Containers[0]} {
		if m := mountOf(c, "site-config"); m == nil || m.MountPath != "/etc/caddy/conf" {
			t.Fatalf("%s site-config mount = %+v", c.Name, m)
		}
	}
}

// The script that writes the server config is fixed: the config and its folder
// reach it as arguments, so neither is ever read as shell.
const siteConfigScript = `printf '%s\n' "$1" > "$2/site.conf"`

func TestServerConfigFolderIsNeverShell(t *testing.T) {
	t.Parallel()
	ws, _ := sourceFixtures()
	ws.File = ""
	tpl := plainStaticTemplate("")
	dir := "/etc/my conf; touch /tmp/x $(id) >"
	tpl.Spec.Build.ServerConfigDir = dir
	tpl.Spec.Build.ServerConfig = "listen {{.Port}};"
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	init := initNamed(t, pod, "site-config")
	if !slices.Equal(init.Command, []string{"sh", "-c", siteConfigScript, "sh", "listen 8080;", dir}) {
		t.Fatalf("site-config = %q", init.Command)
	}
}

func TestStaticTemplateWithoutABuildCommandSkipsBuildAndServesTheFolder(t *testing.T) {
	t.Parallel()
	ws, _ := sourceFixtures()
	ws.File = ""
	pod, err := BuildPod(ws, staticTemplate(""), "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "clone,source-check,site-copy,site-config" {
		t.Fatalf("init containers = %s", got)
	}
	if got := initNamed(t, pod, "site-copy").Command; got[2] != "/srv/src/current/web/." {
		t.Fatalf("site-copy = %q", got)
	}
}

func TestAppBuildSettingsOverrideTheTemplate(t *testing.T) {
	t.Parallel()
	ws, _ := sourceFixtures()
	ws.File = ""
	ws.Source.Build = &model.SourceBuild{Command: "make site", Output: "public"}
	pod, err := BuildPod(ws, staticTemplate("npm run build"), "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := initNamed(t, pod, "build").Command; got[2] != "make site" {
		t.Fatalf("build = %q", got)
	}
	if got := initNamed(t, pod, "site-copy").Command; got[2] != "/srv/src/current/web/public/." {
		t.Fatalf("site-copy = %q", got)
	}
	ws.Source.Build = &model.SourceBuild{}
	pod, err = BuildPod(ws, staticTemplate("npm run build"), "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(initNames(pod), "build") {
		t.Fatal("an app that sets an empty build command still builds")
	}
}

func TestStaticTemplateFromAHomeFileCopiesOnlyThatFile(t *testing.T) {
	t.Parallel()
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "page", Owner: "alice", Template: "static-site", File: "pages/page.html"}
	pod, err := BuildPod(ws, staticTemplate("npm ci && npm run build"), "/u/alice/page/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(initNames(pod), ","); got != "site-copy,site-config" {
		t.Fatalf("init containers = %s", got)
	}
	cp := initNamed(t, pod, "site-copy")
	if strings.Join(cp.Command, "|") != "cp|/home/user/pages/page.html|/usr/share/nginx/html/index.html" {
		t.Fatalf("site-copy = %q", cp.Command)
	}
	if m := mountOf(cp, "home"); m == nil || !m.ReadOnly || m.MountPath != "/home/user" {
		t.Fatalf("site-copy home mount = %+v", m)
	}
	app := pod.Spec.Containers[0]
	if mountOf(app, "home") != nil {
		t.Fatal("the app container mounts the home claim")
	}
	if m := mountOf(app, "site"); m == nil {
		t.Fatal("app does not serve the copied site")
	}
}

func TestAppWithoutASourceBuildsTodaysPod(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.InitContainers) != 0 || !hasHomeClaim(pod) {
		t.Fatalf("a home app changed: inits %v", initNames(pod))
	}
	for _, v := range pod.Spec.Volumes {
		if v.Name == "src" || v.Name == "site" {
			t.Fatalf("volume %s on an app without a source", v.Name)
		}
	}
	// Only the storage layout's annotation: no run fingerprint or token mark.
	for k := range pod.Annotations {
		if k != podstorage.SafeToEvictAnnotation {
			t.Fatalf("annotations = %v", pod.Annotations)
		}
	}
}

func TestRunFingerprintOfAnAppWithoutASourceIsUnchanged(t *testing.T) {
	t.Parallel()
	// Recorded before sources existed: upgrading must not replace a running pod.
	if got := RunFingerprint(&model.App{}); got != "" {
		t.Fatalf("no command, image, port: %q", got)
	}
	if got := RunFingerprint(&model.App{Command: "python app.py", Port: 9000}); got != "169d490dc15d1839" {
		t.Fatalf("fingerprint = %q", got)
	}
}

func TestRunFingerprintChangesWithTheSource(t *testing.T) {
	t.Parallel()
	a := &model.App{Source: &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}}
	base := RunFingerprint(a)
	if base == "" {
		t.Fatal("a source app has a fingerprint")
	}
	for name, edit := range map[string]func(*model.Source){
		"ref":   func(s *model.Source) { s.Ref = exampleSHA },
		"url":   func(s *model.Source) { s.URL = "https://github.com/example-org/other" },
		"path":  func(s *model.Source) { s.Path = "web" },
		"build": func(s *model.Source) { s.Build = &model.SourceBuild{Command: "make"} },
	} {
		b := &model.App{Source: &model.Source{URL: a.Source.URL, Ref: a.Source.Ref}}
		edit(b.Source)
		if RunFingerprint(b) == base {
			t.Errorf("changing the %s keeps the fingerprint", name)
		}
	}
}

// A ref stored before refs were checked must not reach git-sync's arguments.
func TestCloneRefusesARefGitWouldNot(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	ws.Source.Ref = "--upload-pack=touch /tmp/x"
	if _, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages); err == nil || !strings.Contains(err.Error(), "spec.source.ref") {
		t.Fatalf("err = %v", err)
	}
}

// git-sync leaves its git config and credential cache socket in its /tmp. The
// build step runs pull request code, so it must not see the clone's /tmp; the
// token volume is the clone's alone.
func TestCloneTmpIsNotSharedWithTheBuild(t *testing.T) {
	t.Parallel()
	ws, _ := sourceFixtures()
	ws.File, ws.Source.Path = "", "site"
	pod, err := BuildPod(ws, staticTemplate("npm run build"), "/u/alice/site/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithGitToken(pod, ws, "alice-site.app-git", "3")
	clone := initNamed(t, pod, "clone")
	var cloneTmp string
	for _, m := range clone.VolumeMounts {
		if m.MountPath == "/tmp" {
			cloneTmp = m.Name
		}
	}
	if cloneTmp == "" {
		t.Fatal("the clone has no /tmp of its own")
	}
	for _, c := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		if c.Name == "clone" {
			continue
		}
		if mountOf(c, cloneTmp) != nil || mountOf(c, "git-token") != nil {
			t.Errorf("%s mounts the clone's /tmp (%s) or its token", c.Name, cloneTmp)
		}
	}
}

func TestSourceAppNeedsTheImages(t *testing.T) {
	t.Parallel()
	ws, tpl := sourceFixtures()
	if _, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, Images{}); err == nil || !strings.Contains(err.Error(), "source.cloneImage") {
		t.Fatalf("err = %v", err)
	}
}

// A private platform image is pulled with its Secrets by every pod that runs
// it: the pod files copy and the source check. Pods that do not run it get
// none.
func TestBuildPodPullsAPrivatePlatformImageWithItsSecrets(t *testing.T) {
	t.Parallel()
	private := testImages
	private.Runner.PullSecrets = []string{"ghcr"}
	want := []corev1.LocalObjectReference{{Name: "ghcr"}}

	ws, _ := fixtures()
	tpl := sampleTemplate()
	tpl.Spec.PodFiles = true
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, private)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pod.Spec.ImagePullSecrets, want) {
		t.Errorf("pod files: imagePullSecrets = %v, want %v", pod.Spec.ImagePullSecrets, want)
	}

	ws, tpl = sourceFixtures()
	pod, err = BuildPod(ws, tpl, "/u/alice/sales-app/", "", testLayout(FixedUID), sizes.Size{}, private)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pod.Spec.ImagePullSecrets, want) {
		t.Errorf("source check: imagePullSecrets = %v, want %v", pod.Spec.ImagePullSecrets, want)
	}

	ws, _ = fixtures()
	pod, err = BuildPod(ws, sampleTemplate(), "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, private)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.ImagePullSecrets != nil {
		t.Errorf("no platform image in the pod: imagePullSecrets = %v, want none", pod.Spec.ImagePullSecrets)
	}
}

// effectiveEnv is what a program in c sees for name: the kubelet takes the
// last definition. It fails when name is defined twice, which the API server
// warns about.
func effectiveEnv(t *testing.T, c corev1.Container, name string) string {
	t.Helper()
	var values []string
	for _, e := range c.Env {
		if e.Name == name {
			values = append(values, e.Value)
		}
	}
	if len(values) != 1 {
		t.Fatalf("%s defined %d times: %v", name, len(values), values)
	}
	return values[0]
}

// Programs in an app print colours as in a terminal, unless the app says otherwise.
func TestAppPodAsksForColourUnlessTheAppSetsItsOwn(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	pod, err := BuildPod(ws, tpl, "/u/alice/sales-app/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"FORCE_COLOR", "CLICOLOR_FORCE", "PY_COLORS"} {
		if v := effectiveEnv(t, pod.Spec.Containers[0], n); v != "1" {
			t.Errorf("%s = %q, want 1", n, v)
		}
	}
	WithEnv(pod, []model.EnvVar{{Name: "FORCE_COLOR", Value: "0"}})
	if v := effectiveEnv(t, pod.Spec.Containers[0], "FORCE_COLOR"); v != "0" {
		t.Fatalf("FORCE_COLOR = %q, want the app's own 0", v)
	}
}

// The platform's pull secrets are for its own images. A pod that runs an
// image the person named never carries them, or the person's image would be
// pulled with the platform's credentials.
func TestBuildPodNeverGivesAUserImageThePlatformsPullSecrets(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	ws.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}
	img := testImages
	img.Runner = podspec.Image{Ref: "registry.example.com/javo:1", PullSecrets: []string{"platform-pull"}}
	pod, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, img)
	if err == nil {
		t.Fatalf("built a pod for image %s with pull secrets %v", pod.Spec.Containers[0].Image, pod.Spec.ImagePullSecrets)
	}
	// Without platform pull secrets the same app builds.
	if _, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages); err != nil {
		t.Fatalf("public platform image: %v", err)
	}
}

// The gateway checks an image against the allowed registries when the app is
// saved; the controller checks again when it builds the pod, so tightening
// the list stops apps saved before.
func TestBuildPodRefusesAnImageFromARegistryNoLongerAllowed(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	img := testImages
	img.AllowedRegistries = []string{"ghcr.io/example"}
	_, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, img)
	var na *imageref.NotAllowedError
	if !errors.As(err, &na) || na.Registry != "registry.example.com" {
		t.Fatalf("err = %v, want images from registry.example.com refused", err)
	}
	img.AllowedRegistries = []string{"registry.example.com/team"}
	if _, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, img); err != nil {
		t.Fatalf("allowed registry: %v", err)
	}
}

// The Nebi CLI container pulls the platform's image with its pull secrets, so
// a template that runs people's images and asks for Nebi cannot run then.
func TestNebiNeverGivesAUserImageThePlatformsPullSecrets(t *testing.T) {
	t.Parallel()
	ws, tpl := containerFixtures()
	tpl.Spec.NebiToken = true
	pod, err := BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{NebiURL: "http://nebi.example.com", RunnerImage: podspec.Image{Ref: "registry.example.com/javo:1", PullSecrets: []string{"platform-pull"}}}
	if err := r.withNebi(pod, ws, tpl); err == nil {
		t.Fatalf("Nebi gave image %s pull secrets %v", pod.Spec.Containers[0].Image, pod.Spec.ImagePullSecrets)
	}
	r.RunnerImage.PullSecrets = nil
	pod, _ = BuildPod(ws, tpl, "/u/alice/web/", "", testLayout(100500), sizes.Size{}, testImages)
	if err := r.withNebi(pod, ws, tpl); err != nil {
		t.Fatalf("public platform image: %v", err)
	}
}

// The app container keeps the storage layout's disk limit at any size, so
// one app cannot fill the node's disk.
func TestBuildPodLimitsTheAppsDiskAtAnySize(t *testing.T) {
	t.Parallel()
	ws, tpl := fixtures()
	l := podstorage.Build("alice", podstorage.Input{UID: 1000, GID: 100, TmpMax: resource.MustParse("2Gi"), DiskMax: resource.MustParse("30Gi")})
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", l, sizes.Custom(resource.MustParse("1"), resource.MustParse("1Gi")), testImages)
	if err != nil {
		t.Fatal(err)
	}
	r := pod.Spec.Containers[0].Resources
	if lim := r.Limits[corev1.ResourceEphemeralStorage]; lim.Cmp(resource.MustParse("30Gi")) != 0 || r.Limits.Memory().Cmp(resource.MustParse("1Gi")) != 0 {
		t.Fatalf("resources = %+v", r)
	}
}

// Every init container that runs only the platform's own steps (checks,
// copies, the identity, the Nebi CLI) has a read-only root filesystem: it
// writes only to its volumes. The clone (git-sync) and a build (the
// repository's own commands) keep a writable one.
func TestPlatformInitContainersHaveAReadOnlyRoot(t *testing.T) {
	t.Parallel()
	writable := map[string]bool{source.CloneContainer: true, source.BuildContainer: true}
	var pods []*corev1.Pod
	build := func(ws *model.App, tpl *model.Template) {
		t.Helper()
		pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
		if err != nil {
			t.Fatal(err)
		}
		pods = append(pods, pod)
	}
	ws, _ := fixtures()
	tpl := sampleTemplate()
	tpl.Spec.PodFiles = true
	ws.Sample = true
	build(ws, tpl)
	ws, _ = sourceFixtures()
	build(ws, staticTemplate("npm ci && npm run build"))
	ws, _ = fixtures()
	ws.File = "index.html"
	build(ws, staticTemplate(""))
	ws, tpl = fixtures()
	pod, err := BuildPod(ws, tpl, "/u/a/b/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithNebi(pod, ws.ObjectName(), "http://nebi.example.com", testImages.Runner)
	pods = append(pods, pod)

	seen := map[string]bool{}
	for _, p := range pods {
		for _, c := range p.Spec.InitContainers {
			seen[c.Name] = true
			ro := c.SecurityContext != nil && c.SecurityContext.ReadOnlyRootFilesystem != nil && *c.SecurityContext.ReadOnlyRootFilesystem
			if ro == writable[c.Name] {
				t.Errorf("init container %s: read-only root %v, want %v", c.Name, ro, !writable[c.Name])
			}
		}
	}
	for _, name := range []string{source.CheckContainer, source.SiteCopyContainer, source.SiteConfigContainer, podfiles.CopyContainer, podfiles.IdentityContainer, NebiCLIContainer, model.SampleCopyContainer} {
		if !seen[name] {
			t.Errorf("no pod had init container %s: the check covers nothing for it", name)
		}
	}
}
