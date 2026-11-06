package model

import (
	"regexp"
	"slices"
	"strings"
	"text/template"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// The values of TemplateSpec.UserCommand.
const (
	UserCommandNone     = "None"
	UserCommandOptional = "Optional"
	UserCommandRequired = "Required"
)

// The values of TemplateSpec.FromRepo.
const (
	FromRepoNone     = "None"
	FromRepoOptional = "Optional"
	FromRepoRequired = "Required"
)

// CategoryOther is the category of a template that names none.
const CategoryOther = "other"

// CategoryAgents is the category of the coding agent templates: their apps
// are never shared, and their pods reach the owner's SSH keys only through
// an ssh-agent sidecar.
const CategoryAgents = "agents"

// TemplateCategory is a group of templates on New app and the Templates page.
type TemplateCategory struct {
	ID    string
	Title string
}

// TemplateCategories are the categories a template may name, in the order
// they are shown: the most used first, Other last.
var TemplateCategories = []TemplateCategory{
	{"notebooks", "Notebooks and IDEs"},
	{"dashboards", "Dashboards and visualization"},
	{"web", "Web apps and APIs"},
	{CategoryAgents, "Coding agents"},
	{"jobs", "Jobs"},
	{CategoryOther, "Other"},
}

// DefaultBuildOutput is the folder a build writes to when neither the
// template nor the app names one.
const DefaultBuildOutput = "dist"

// PodFilesDir is where a pod of a template with PodFiles finds the
// platform's files.
const PodFilesDir = "/opt/javo"

// environmentName is what TemplateSpec.Environment may be: a folder name.
var environmentName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// RunImage is the image the template's pods run: its own, else, for a
// template with an environment, pixiImage (the platform's).
func (s *TemplateSpec) RunImage(pixiImage string) string {
	if s.Image == "" && s.Environment != "" {
		return pixiImage
	}
	return s.Image
}

// UsesPodFiles reports whether pods of the template get the platform's
// files (PodFiles, or an Environment, which is one of them).
func (s *TemplateSpec) UsesPodFiles() bool { return s.PodFiles || s.Environment != "" }

// buildFileName is what TemplateBuild.IfFile may be: a plain file name.
var buildFileName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,254}$`)

// TemplateSpec describes how to run one kind of app. It is stored as one JSON
// column.
// +kubebuilder:object:generate=true
type TemplateSpec struct {
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	// Category is the ID of one of TemplateCategories; empty means Other.
	Category string `json:"category,omitempty"`

	Kind string `json:"kind"`

	// Image is required unless UserImage is set (it must be empty then) or
	// the template has an Environment: empty, that runs in the platform's
	// pixi image (RunImage).
	Image string `json:"image,omitempty"`

	// Environment names the template's default pixi environment: a folder
	// of the environments the platform carries in its pod files
	// (PodFilesDir/envs/<name>, with a pixi.toml and its pixi.lock). Each
	// start installs it into the owner's home, once per lockfile
	// (internal/pixienv); an environment picked from Nebi, or a pixi.lock at
	// the root of the app's repository, is installed instead. It implies
	// PodFiles. Empty runs the image as it is.
	Environment string `json:"environment,omitempty"`

	// ServesKernels says the template's program serves Jupyter kernels
	// (JupyterLab): an environment picked for an app is then installed
	// beside Environment, which still runs the program, and offered as a
	// kernel and by name in terminals, instead of replacing it. Needs
	// Environment.
	ServesKernels bool `json:"servesKernels,omitempty"`

	// PodFiles gives the pod the platform's files at PodFilesDir
	// (pod-user.sh, the samples, the terminal page, the environments),
	// copied from the platform's own image, and a passwd entry for the uid
	// it runs as. Templates whose command starts with pod-user.sh need it.
	PodFiles bool `json:"podFiles,omitempty"`
	// Port is the port the app serves on; with UserImage, the port an app
	// gets when its creator names none.
	Port int32 `json:"port"`

	// Command and Args are Go text/template strings with fields
	// .BaseURL, .Port, .File, .Home, .Sample (the sample's folder, empty
	// unless the workspace started from it) and .Host (the public host
	// browsers reach the gateway at, empty unless the deployment sets
	// publicHost), and the functions dir, base and trimSuffix (suffix
	// first). Use argv form, not a shell:
	// values are not shell-quoted.
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`

	// UserCommand says whether apps from this template take a command line
	// from their creator (App.Command): None, Optional or
	// Required. The command, split into words and rendered like Args,
	// replaces Args (the image's default command), as in
	// `docker run IMAGE COMMAND`; Command, if set, still runs first.
	UserCommand string `json:"userCommand,omitempty"`

	// UserImage means each app names its own image and port (App.Image and
	// App.Port). Such apps run as the user the image sets,
	// never root, and are ready once their port accepts a connection.
	UserImage bool `json:"userImage,omitempty"`

	// FromRepo says whether apps from this template run from a Git repository
	// (App.Source): None, Optional or Required.
	FromRepo string `json:"fromRepo,omitempty"`

	// Build, when set, makes an app with a repository build it in Image and
	// serve the result from ServeDir (kind static).
	Build *TemplateBuild `json:"build,omitempty"`

	// FileExtensions lists the file types this template opens.
	// Empty means the kind takes a folder.
	FileExtensions []string `json:"fileExtensions,omitempty"`

	// ReadinessPath is a Go text/template string.
	ReadinessPath string `json:"readinessPath,omitempty"`

	// Activity says what counts as use for idle stop. Traffic (the
	// default): every request and every open connection through the
	// gateway. Jupyter: requests, and while a connection is open what the
	// Jupyter server reports (/api/status, or now while a kernel runs
	// code), since a JupyterLab tab holds websockets open whether used or
	// not.
	Activity string `json:"activity,omitempty"`

	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// DefaultResources is the CPU and memory an app or a job from this
	// template runs at when its creator picks no size: what the template's
	// sample needs, with headroom. The gateway gives it as a custom amount
	// where the person may ask for one, else the smallest size that holds
	// it. Nil means the catalog's default size.
	DefaultResources *TemplateAmount `json:"defaultResources,omitempty"`

	// DefaultIdleStopMinutes is the "Stop when idle" timeout an app from
	// this template gets when its creator picks none, in place of the
	// admin's idleStop.defaultMinutes; 0 is Off. Kept inside the admin's
	// range when used (config.IdleStopCfg.DefaultFor). Nil means the
	// admin's default.
	DefaultIdleStopMinutes *int32 `json:"defaultIdleStopMinutes,omitempty"`

	// AllowedGroups limits who may use the template. Empty means everyone.
	AllowedGroups []string `json:"allowedGroups,omitempty"`

	// AllowedProviderRoles limits who may use the template to holders of
	// these identity provider roles (config rolesClaim), as an alternative
	// to AllowedGroups: either one is enough.
	AllowedProviderRoles []string `json:"allowedProviderRoles,omitempty"`

	// Sample is a ready-made example baked into the image. Nil means the
	// template has no sample.
	Sample *TemplateSample `json:"sample,omitempty"`

	// BasePath says how the app meets the URL prefix: Prefix means it serves
	// under {{.BaseURL}}; Strip means the gateway removes the prefix and sends
	// X-Forwarded-Prefix.
	BasePath string `json:"basePath,omitempty"`

	// AppLogin says whether the app has its own login. Disabled apps are
	// usable only where the cluster enforces NetworkPolicy.
	AppLogin string `json:"appLogin,omitempty"`

	// ProxyHeaders are extra request headers the gateway sets; values are Go
	// templates with .BaseURL.
	ProxyHeaders map[string]string `json:"proxyHeaders,omitempty"`

	// Jobs says what jobs may run on this template; nil means none.
	Jobs *TemplateJobs `json:"jobs,omitempty"`

	// AllowedUsers are Identity names. Empty AllowedUsers, AllowedGroups and
	// AllowedProviderRoles mean every member may use the template.
	AllowedUsers []string `json:"allowedUsers,omitempty"`

	// MaxSharing is the most open access level an app from this template may use.
	MaxSharing string `json:"maxSharing,omitempty"`

	// APIToken makes ReadWrite the default API access of apps from this
	// template (App.APIAccess); without it the default is None. An app with
	// access gets an API token of its owner, made for that app and revoked
	// with it: the app container gets JAVO_URL (the UI's public URL) and
	// JAVO_TOKEN (from a Secret), and a short guide to the API at
	// /opt/javo/AGENTS.md. For coding agents that create and manage apps.
	APIToken bool `json:"apiToken,omitempty"`

	// NebiToken gives apps from this template their owner's Nebi token
	// while the platform's Nebi integration is on (NebiTokenFor): a file the
	// nebi CLI and the Nebi kernels read, kept fresh by the gateway while
	// the owner uses the app. Like an API token, anyone the app is shared
	// with can then act as the owner in Nebi.
	NebiToken bool `json:"nebiToken,omitempty"`

	// MaxAPIAccess is the most API access an app from this template may
	// have: None, Read or ReadWrite; empty means ReadWrite. It also caps the
	// default.
	MaxAPIAccess string `json:"maxApiAccess,omitempty"`

	// APIGuidePaths are more absolute paths where the same guide is mounted
	// read-only, for agents that read instructions from a fixed place (for
	// example /etc/claude-code/CLAUDE.md for Claude Code).
	APIGuidePaths []string `json:"apiGuidePaths,omitempty"`

	// AgentDeploy lets the own API token of an app from this template, a
	// coding agent working for its owner, do more than the read-only
	// workload allow-list: create, change, start, restart and delete the
	// owner's apps and jobs that hold no secret, API token or Nebi token
	// (authn.AgentDeployWrites). Anything else stays refused and is left to
	// the person in the browser. Needs apiToken and apps that are never
	// shared (maxSharing Private).
	AgentDeploy bool `json:"agentDeploy,omitempty"`

	// AgentModel gives apps from this template a model to work with: the
	// gateway's model endpoint, called with the app's own token, while the
	// admin's agent model is on; otherwise the owner's own agent key, if
	// they stored one. The app gets JAVO_AGENT_MODE (platform, own or
	// none), JAVO_AGENT_MODEL and JAVO_AGENT_BASE_URL, and with own,
	// JAVO_AGENT_KEY. Needs apiToken and apps that are never shared
	// (maxSharing Private).
	AgentModel bool `json:"agentModel,omitempty"`

	// JobOnly marks a template that only runs jobs. It is not offered as an
	// app and creating an app from it is refused. kind, port and command are
	// still required by the schema; put placeholders there.
	JobOnly bool `json:"jobOnly,omitempty"`
}

// TemplateBuild is the build step of a template: the repository is built in
// Image, and the files it writes are copied to ServeDir of the app image.
// +kubebuilder:object:generate=true
type TemplateBuild struct {
	// Image runs Command in the repository folder.
	Image string `json:"image"`
	// Command is the default build command; an app's Source.Build may replace
	// it. Empty means there is nothing to build, the repository folder is served.
	Command string `json:"command,omitempty"`
	// IfFile, when set, runs Command only when a file of that name is in the
	// repository folder (package.json for npm); without one the folder is
	// served as it is. An app's own build command always runs.
	IfFile string `json:"ifFile,omitempty"`
	// Output is the folder Command writes to, relative to the repository
	// folder; empty means DefaultBuildOutput.
	Output string `json:"output,omitempty"`
	// ServeDir is the absolute folder of the app image the files are copied to.
	ServeDir string `json:"serveDir"`
	// ServerConfig is the config the pod writes for the server of the app
	// image, a Go text/template string with fields .Port (the template's
	// port) and .ServeDir. It is written to site.conf in ServerConfigDir, an
	// empty folder mounted over that folder of the app image, so whatever the
	// image keeps there is hidden. Empty means the image serves ServeDir with
	// its own config and no step is added.
	ServerConfig string `json:"serverConfig,omitempty"`
	// ServerConfigDir is the absolute folder of the app image the server
	// reads its config from; required with ServerConfig, and only with it.
	ServerConfigDir string `json:"serverConfigDir,omitempty"`
}

// RenderServerConfig is ServerConfig for an app image serving on port.
func (b *TemplateBuild) RenderServerConfig(port int32) (string, error) {
	t, err := template.New("serverConfig").Parse(b.ServerConfig)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := t.Execute(&out, struct {
		Port     int32
		ServeDir string
	}{port, b.ServeDir}); err != nil {
		return "", err
	}
	return out.String(), nil
}

// TemplateJobs limits the jobs a template's image may run.
// +kubebuilder:object:generate=true
type TemplateJobs struct {
	Engines    []string         `json:"engines,omitempty"`
	Kernels    []string         `json:"kernels,omitempty"`
	MaxTimeout *metav1.Duration `json:"maxTimeout,omitempty"`
	// Sample is a ready-made command job baked into the image, offered on
	// the Jobs page; nil means none. Its engine is command, or bash on a
	// template whose Engines list bash and not command.
	Sample *TemplateJobSample `json:"sample,omitempty"`
}

// TemplateJobSample is a command job a person can make with one click.
// +kubebuilder:object:generate=true
type TemplateJobSample struct {
	// Title names the sample on its card and becomes the job's name, e.g.
	// "Print the time".
	Title string `json:"title"`
	// Command is the command line the job runs, read like a command job's
	// (shell quoting, no shell, {{.Home}}), usually a script in the image;
	// for a bash job, the script bash runs.
	Command string `json:"command"`
	// Parameters the sample job declares, to show how a run gets them.
	Parameters []JobParameter `json:"parameters,omitempty"`
}

// TemplateAmount is a CPU and memory amount, set as both requests and
// limits.
// +kubebuilder:object:generate=true
type TemplateAmount struct {
	CPU    resource.Quantity `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
}

// Resources is a as an app's custom amount.
func (a *TemplateAmount) Resources() *AppResources {
	return &AppResources{CPU: a.CPU, Memory: a.Memory}
}

// TemplateSample names the sample a template ships: a folder in its image,
// or, for a userImage template, an image to run.
// +kubebuilder:object:generate=true
type TemplateSample struct {
	// Path is the absolute in-image path of the sample folder. Required
	// unless the template has userImage, and empty then.
	Path string `json:"path,omitempty"`
	// Image is the image the sample app runs, for a userImage template;
	// New app prefills it.
	Image string `json:"image,omitempty"`
	// Port is the port Image serves on; 0 means the template's port.
	Port int32 `json:"port,omitempty"`
	// Open is the path inside Path that the app opens; empty for kinds that
	// take a folder.
	Open string `json:"open,omitempty"`
	// Title names the sample on its card, e.g. "Sales explorer".
	Title string `json:"title"`
	// Command is the command line the sample runs with, for templates that
	// take one (userCommand); New app prefills it.
	Command string `json:"command,omitempty"`
}

// Template is one row of the templates table: a recipe for a kind of app.
// Source is "builtin" for templates synced from the chart and "admin" for
// templates an administrator saved.
type Template struct {
	Name    string
	Source  string
	Spec    TemplateSpec
	Version int64
}

var templateKinds = []string{"jupyterlab", "streamlit", "vscode", "marimo", "pluto", "rstudio", "panel", "bokeh", "dash", "gradio", "voila", "custom", "fastapi", "container", "static", "claude-code", "pi", "herdr", "terminal", "whybook", "python", "julia", "r", "go", "node"}

// Activity values (TemplateSpec.Activity).
const (
	ActivityTraffic = "Traffic"
	ActivityJupyter = "Jupyter"
)

var jobEngines = []string{"papermill", "marimo", "quarto", "command", "bash"}

// forbiddenProxyHeaders may not be set through a template: they carry the
// caller's credentials or identity.
var forbiddenProxyHeaders = []string{"authorization", "cookie", "host", "x-forwarded-for"}

// ApplyDefaults fills what an unset field means.
func (s *TemplateSpec) ApplyDefaults() {
	if s.UserCommand == "" {
		s.UserCommand = UserCommandNone
	}
	if s.FromRepo == "" {
		s.FromRepo = FromRepoNone
	}
	if s.Build != nil && s.Build.Output == "" {
		s.Build.Output = DefaultBuildOutput
	}
	if s.ReadinessPath == "" {
		s.ReadinessPath = "{{.BaseURL}}"
	}
	if s.BasePath == "" {
		s.BasePath = "Prefix"
	}
	if s.AppLogin == "" {
		s.AppLogin = "Disabled"
	}
	if s.MaxSharing == "" {
		s.MaxSharing = AccessPrivate
	}
	if s.Category == "" {
		s.Category = CategoryOther
	}
}

// Validate checks the template's name and spec. The name was a Kubernetes
// object name and is still used as one, so it is a lowercase DNS-1123
// subdomain of at most 253 characters.
func (t *Template) Validate() error {
	var v validator
	for _, msg := range validation.IsDNS1123Subdomain(t.Name) {
		v.add("metadata.name", msg)
	}
	return v.merge(t.Spec.Validate())
}

// Validate checks the rules every saved template satisfies. Empty enum
// fields count as their default.
func (s *TemplateSpec) Validate() error {
	var v validator
	v.oneOf("spec.kind", s.Kind, false, templateKinds...)
	categories := make([]string, len(TemplateCategories))
	for i, c := range TemplateCategories {
		categories[i] = c.ID
	}
	v.oneOf("spec.category", s.Category, true, categories...)
	if s.Port < 1 || s.Port > 65535 {
		v.add("spec.port", "port must be between 1 and 65535")
	}
	switch {
	case s.UserImage && s.Image != "":
		v.add("spec.image", "set image, or userImage, not both")
	case !s.UserImage && s.Image == "" && s.Environment == "":
		v.add("spec.image", "image is required, unless userImage is set or an environment runs in the platform's pixi image")
	}
	v.oneOf("spec.userCommand", s.UserCommand, true, UserCommandNone, UserCommandOptional, UserCommandRequired)
	v.oneOf("spec.fromRepo", s.FromRepo, true, FromRepoNone, FromRepoOptional, FromRepoRequired)
	if b := s.Build; b != nil {
		b.validate(&v, "spec.build")
	}
	v.oneOf("spec.basePath", s.BasePath, true, "Prefix", "Strip")
	v.oneOf("spec.appLogin", s.AppLogin, true, "Enabled", "Disabled")
	v.oneOf("spec.activity", s.Activity, true, ActivityTraffic, ActivityJupyter)
	v.oneOf("spec.maxSharing", s.MaxSharing, true, AccessPrivate, AccessRestricted, AccessSignedIn, AccessPublic)
	v.oneOf("spec.maxApiAccess", s.MaxAPIAccess, true, apiAccessLevels...)
	for k := range s.ProxyHeaders {
		if slices.Contains(forbiddenProxyHeaders, strings.ToLower(k)) {
			v.add("spec.proxyHeaders", "proxyHeaders may not set Authorization, Cookie, Host or X-Forwarded-For")
			break
		}
	}
	if s.Environment != "" && !environmentName.MatchString(s.Environment) {
		v.add("spec.environment", "must be a folder name: at most 63 lowercase letters, digits and dashes, starting with a letter or digit")
	}
	if s.ServesKernels && s.Environment == "" {
		v.add("spec.servesKernels", "needs an environment of the template's own, which runs the program beside the picked one")
	}
	if s.UserImage && s.Environment != "" {
		v.add("spec.environment", "a userImage template runs the image as it is, without an environment")
	}
	if s.UserImage && s.PodFiles {
		v.add("spec.podFiles", "a userImage template runs the image as it is, without the pod files")
	}
	if s.UserImage && s.Jobs != nil {
		v.add("spec.jobs", "a userImage template cannot run jobs")
	}
	if sm := s.Sample; sm != nil {
		shaped := sm.Path != "" && sm.Image == "" && sm.Port == 0
		if s.UserImage {
			shaped = sm.Image != "" && sm.Path == ""
		}
		if !shaped {
			v.add("spec.sample", "the sample of a userImage template names an image and no path; any other sample names a path and no image or port")
		}
		s.Sample.validate(&v, "spec.sample")
		if s.UserCommand == UserCommandRequired && s.Sample.Command == "" {
			v.add("spec.sample", "the sample of a template that requires a command needs a command")
		}
	}
	if r := s.DefaultResources; r != nil {
		if r.CPU.Cmp(MinCustomCPU) < 0 {
			v.add("spec.defaultResources.cpu", "cpu must be at least 100m")
		}
		if r.Memory.Cmp(MinCustomMemory) < 0 {
			v.add("spec.defaultResources.memory", "memory must be at least 256Mi")
		}
	}
	if m := s.DefaultIdleStopMinutes; m != nil && *m < 0 {
		v.add("spec.defaultIdleStopMinutes", "defaultIdleStopMinutes must not be negative")
	}
	if s.JobOnly && s.Jobs == nil {
		v.add("spec.jobOnly", "jobOnly needs jobs")
	}
	if s.Jobs != nil {
		s.Jobs.validate(&v, "spec.jobs")
	}
	if len(s.APIGuidePaths) > 0 && !s.APIToken {
		v.add("spec.apiGuidePaths", "apiGuidePaths needs apiToken")
	}
	if s.AgentDeploy && !s.APIToken {
		v.add("spec.agentDeploy", "agentDeploy needs apiToken")
	}
	if s.AgentModel && !s.APIToken {
		v.add("spec.agentModel", "agentModel needs apiToken")
	}
	if s.AgentDeploy && s.MaxSharing != "" && s.MaxSharing != AccessPrivate {
		v.add("spec.agentDeploy", "agentDeploy needs maxSharing Private")
	}
	if s.AgentModel && s.MaxSharing != "" && s.MaxSharing != AccessPrivate {
		v.add("spec.agentModel", "agentModel needs maxSharing Private")
	}
	v.maxItems("spec.apiGuidePaths", len(s.APIGuidePaths), 8)
	for i, p := range s.APIGuidePaths {
		if !strings.HasPrefix(p, "/") || charLen(p) > 1024 {
			v.add(indexed("spec.apiGuidePaths", i), "must be an absolute path of at most 1024 characters")
		}
	}
	return v.err()
}

func (b *TemplateBuild) validate(v *validator, field string) {
	if b.Image == "" {
		v.add(field+".image", "image is required")
	}
	v.maxLen(field+".image", b.Image, 512)
	v.maxLen(field+".command", b.Command, MaxBuildLength)
	if b.IfFile != "" && !buildFileName.MatchString(b.IfFile) {
		v.add(field+".ifFile", "must be a file name of at most 255 letters, digits, dots, dashes and underscores")
	}
	validateRelative(v, field+".output", b.Output, MaxBuildLength)
	if !strings.HasPrefix(b.ServeDir, "/") || charLen(b.ServeDir) > 1024 {
		v.add(field+".serveDir", "serveDir is required: an absolute path of at most 1024 characters")
	}
	v.maxLen(field+".serverConfig", b.ServerConfig, 4096)
	switch {
	case b.ServerConfig != "" && b.ServerConfigDir == "":
		v.add(field+".serverConfigDir", "serverConfigDir is required with serverConfig")
	case b.ServerConfig == "" && b.ServerConfigDir != "":
		v.add(field+".serverConfig", "serverConfig is required with serverConfigDir")
	}
	if b.ServerConfigDir != "" && (!strings.HasPrefix(b.ServerConfigDir, "/") || charLen(b.ServerConfigDir) > 1024) {
		v.add(field+".serverConfigDir", "must be an absolute path of at most 1024 characters")
	}
	if b.ServerConfig != "" {
		if _, err := b.RenderServerConfig(1); err != nil {
			v.add(field+".serverConfig", "is not a valid template: "+err.Error())
		}
	}
}

func (s *TemplateSample) validate(v *validator, field string) {
	if s.Path != "" && (!strings.HasPrefix(s.Path, "/") || charLen(s.Path) > 1024) {
		v.add(field+".path", "must be an absolute path of at most 1024 characters")
	}
	v.maxLen(field+".image", s.Image, 512)
	if s.Port < 0 || s.Port > 65535 {
		v.add(field+".port", "port must be between 1 and 65535")
	}
	v.maxLen(field+".open", s.Open, 1024)
	if s.Title == "" || charLen(s.Title) > 63 {
		v.add(field+".title", "title is required and at most 63 characters")
	}
	v.maxLen(field+".command", s.Command, 2048)
}

func (j *TemplateJobs) validate(v *validator, field string) {
	for i, e := range j.Engines {
		v.oneOf(indexed(field+".engines", i), e, false, jobEngines...)
	}
	if j.Sample == nil {
		return
	}
	f := field + ".sample"
	if j.Sample.Title == "" || charLen(j.Sample.Title) > 63 {
		v.add(f+".title", "title is required and at most 63 characters")
	}
	if j.Sample.Command == "" || charLen(j.Sample.Command) > 2048 {
		v.add(f+".command", "command is required and at most 2048 characters")
	}
	validateParameters(v, f+".parameters", j.Sample.Parameters)
}

// privateOnlyKinds hand whoever opens the app a shell, a notebook kernel or
// a coding agent that runs as the owner, with the owner's files.
var privateOnlyKinds = []string{"terminal", "jupyterlab", "whybook", "claude-code", "pi", "herdr"}

// codeEditorKinds are IDEs and editable notebooks: whoever opens the app can
// run any code or command as its owner.
var codeEditorKinds = []string{"vscode", "rstudio", "marimo", "pluto"}

// RunsAnyCode reports whether whoever opens an app from t can run any code
// as its owner (a code editor kind).
func RunsAnyCode(t *Template) bool { return slices.Contains(codeEditorKinds, t.Spec.Kind) }

// MaxSharing is the most open level an app from t may be shared at, whatever
// spec.maxSharing says above it: Private for a missing template, a
// private-only kind or a coding agent (category agents); Restricted (chosen
// people and groups) for a code editor. Otherwise it is spec.maxSharing, and
// an unknown value is Private. authz.Decide (every open of an app), the
// Sharing API and the template list all ask it.
func MaxSharing(t *Template) string {
	switch {
	case t == nil || slices.Contains(privateOnlyKinds, t.Spec.Kind) || t.Spec.Category == CategoryAgents || AccessRank(t.Spec.MaxSharing) < 0:
		return AccessPrivate
	case RunsAnyCode(t) && AccessRank(t.Spec.MaxSharing) > AccessRank(AccessRestricted):
		return AccessRestricted
	}
	return t.Spec.MaxSharing
}
