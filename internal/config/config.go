// Package config is the platform config file shared by `javo controller` and
// `javo gateway`. Helm renders it into the javo-config ConfigMap.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/json"
	"sigs.k8s.io/yaml"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/model"
)

// DefaultPath is where the chart mounts the config file.
const DefaultPath = "/etc/" + model.ProductName + "/config.yaml"

// Platform is the whole config file.
type Platform struct {
	Auth       Auth        `json:"auth"`
	Roles      Roles       `json:"roles"`
	URLs       URLs        `json:"urls"`
	Namespaces Namespaces  `json:"namespaces"`
	Identity   IdentityCfg `json:"identity"`
	Sessions   SessionCfg  `json:"sessions"`
	Sharing    SharingCfg  `json:"sharing"`
	// ContainerImages governs apps that run an image their creator names
	// (templates with userImage).
	ContainerImages ContainerImagesCfg `json:"containerImages"`
	// API configures the public API: personal tokens and the docs page.
	API APICfg `json:"api"`
	// IdleStop governs stopping apps nobody has used for a while.
	IdleStop IdleStopCfg `json:"idleStop"`
	// Storage is the admin's choice for homes, group folders and scratch.
	Storage StorageCfg `json:"storage"`
	// Backup is the bucket the database backups are copied to and the
	// volumes (home folders) backed up to; the database is backed up by the
	// server or its CronJob (database.backup).
	Backup BackupCfg `json:"backup"`
	// Source configures apps that run from a Git repository.
	Source SourceCfg `json:"source"`
	// AI configures the optional model features; off by default.
	AI AICfg `json:"ai"`
	// MCP configures the agent endpoint at /mcp.
	MCP MCPCfg `json:"mcp"`
	// Plans configures proposed changes waiting for approval.
	Plans PlansCfg `json:"plans"`
	// Thumbnails lets owners give their apps' cards a picture; off by default.
	Thumbnails ThumbnailsCfg `json:"thumbnails"`
	// Nebi lets apps and jobs run in a person's Nebi environment; off by default.
	Nebi NebiCfg `json:"nebi"`
	// Environments configures how apps and jobs get their pixi environments.
	Environments EnvironmentsCfg `json:"environments"`
	// Branding is the white-label name, logo, favicon and accent of the UI.
	Branding BrandingCfg `json:"branding"`
	// Limits caps what one person may have and run at a time.
	Limits LimitsCfg `json:"limits"`
}

// LimitsCfg is the admin's caps per person, so one person cannot fill the
// cluster or the database. A count left out (or 0) gets its default; every
// cap applies to platform admins too.
type LimitsCfg struct {
	// RunningApps is how many of a person's apps (previews not counted) may
	// run at once: creating, starting or importing one more is refused.
	RunningApps int32 `json:"runningApps"`
	// Apps is how many apps a person may have, running or stopped.
	Apps int32 `json:"apps"`
	// Previews is how many previews of pull requests a person's apps may
	// have in total; previews past it are not started.
	Previews int32 `json:"previews"`
	// Jobs is how many jobs a person may have.
	Jobs int32 `json:"jobs"`
	// ActiveRunsPerJob and ActiveRunsPerPerson cap the runs going at once,
	// of one job and of all of a person's jobs. Run now is refused at a cap,
	// and scheduled runs are skipped until a run ends.
	ActiveRunsPerJob    int32 `json:"activeRunsPerJob"`
	ActiveRunsPerPerson int32 `json:"activeRunsPerPerson"`
	// APITokens is how many personal API tokens a person may have.
	APITokens int32 `json:"apiTokens"`
	// PendingPlans is how many proposed changes may wait for a person's approval.
	PendingPlans int32 `json:"pendingPlans"`
	// JobMaxTimeout is the longest any run may take; a job or template that
	// sets a longer time limit, or none, gets this one.
	JobMaxTimeout metav1.Duration `json:"jobMaxTimeout"`
	// Namespace is what each per-user or per-team namespace gets; empty is
	// nothing. Not allowed in shared mode.
	Namespace NamespaceLimitsCfg `json:"namespace"`
}

// NamespaceLimitsCfg is a ResourceQuota and a LimitRange for each namespace
// of one person or one team.
type NamespaceLimitsCfg struct {
	// Quota is the ResourceQuota's hard limits (requests.cpu, limits.memory,
	// pods, requests.ephemeral-storage and so on). A quota on a resource
	// makes Kubernetes refuse pods that do not ask for it, so give
	// ContainerDefaultRequests (or ContainerDefaults) for each.
	Quota corev1.ResourceList `json:"quota,omitempty"`
	// ContainerDefaults and ContainerDefaultRequests are the limits and
	// requests the LimitRange gives a container that names none.
	ContainerDefaults        corev1.ResourceList `json:"containerDefaults,omitempty"`
	ContainerDefaultRequests corev1.ResourceList `json:"containerDefaultRequests,omitempty"`
}

// IsSet reports whether any part is given.
func (c NamespaceLimitsCfg) IsSet() bool {
	return len(c.Quota) > 0 || len(c.ContainerDefaults) > 0 || len(c.ContainerDefaultRequests) > 0
}

// The defaults of LimitsCfg.
const (
	DefaultRunningApps         = 10
	DefaultApps                = 50
	DefaultPreviews            = 20
	DefaultJobs                = 50
	DefaultActiveRunsPerJob    = 3
	DefaultActiveRunsPerPerson = 10
	DefaultAPITokens           = 50
	DefaultPendingPlans        = 20
	DefaultJobMaxTimeout       = 24 * time.Hour
)

func (c *LimitsCfg) applyDefaults() {
	for _, f := range []struct {
		n   *int32
		def int32
	}{
		{&c.RunningApps, DefaultRunningApps}, {&c.Apps, DefaultApps}, {&c.Previews, DefaultPreviews},
		{&c.Jobs, DefaultJobs}, {&c.ActiveRunsPerJob, DefaultActiveRunsPerJob},
		{&c.ActiveRunsPerPerson, DefaultActiveRunsPerPerson}, {&c.APITokens, DefaultAPITokens},
		{&c.PendingPlans, DefaultPendingPlans},
	} {
		if *f.n == 0 {
			*f.n = f.def
		}
	}
	setDur(&c.JobMaxTimeout, DefaultJobMaxTimeout)
}

func (c LimitsCfg) validate(bad func(key, format string, a ...any)) {
	for _, f := range []struct {
		key string
		n   int32
	}{
		{"runningApps", c.RunningApps}, {"apps", c.Apps}, {"previews", c.Previews}, {"jobs", c.Jobs},
		{"activeRunsPerJob", c.ActiveRunsPerJob}, {"activeRunsPerPerson", c.ActiveRunsPerPerson},
		{"apiTokens", c.APITokens}, {"pendingPlans", c.PendingPlans},
	} {
		if f.n < 1 {
			bad("limits."+f.key, "%d must be at least 1", f.n)
		}
	}
	if c.JobMaxTimeout.Duration <= 0 {
		bad("limits.jobMaxTimeout", "%s must be positive", c.JobMaxTimeout.Duration)
	}
}

// EnvironmentsCfg configures the pixi environments apps and jobs run in: a
// template's own, a picked Nebi one, or a repository's pixi.lock.
type EnvironmentsCfg struct {
	// PixiImage is an image with pixi on PATH (and sh, flock, sha256sum,
	// base64, gunzip); an init container of each app or job pod that runs in
	// an environment installs it with this image. Templates with an
	// environment run in it too.
	PixiImage string `json:"pixiImage,omitempty"`
}

// ThumbnailsCfg configures pictures on app cards. While Enabled is false the
// picture routes are not served, app views carry no thumbnail and the cards
// show the app kind's logo as before.
type ThumbnailsCfg struct {
	Enabled bool `json:"enabled"`
}

// NebiCfg is the optional Nebi environments integration. While Enabled is
// false nothing talks to Nebi and no form offers environments. The gateway
// asks Nebi as the signed-in person, with their OIDC ID token, so it needs
// auth.oidc and a Nebi that trusts the same issuer.
type NebiCfg struct {
	Enabled bool `json:"enabled"`
	// URL is Nebi's address as the gateway and the apps reach it, such as
	// https://nebi.example.com: apps whose template sets nebiToken call it
	// as their owner. The picked environments install with
	// environments.pixiImage.
	URL string `json:"url,omitempty"`
}

// AICfg configures the model features. With Enabled false no provider is
// built and nothing connects to a model.
type AICfg struct {
	Enabled bool `json:"enabled"`
	// Provider is a registered adapter: anthropic, or openai (any
	// OpenAI-compatible server: vLLM, Ollama, llama.cpp, Azure OpenAI).
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"baseURL"`
	// Timeout bounds one model call; default 60s.
	Timeout  metav1.Duration `json:"timeout"`
	Features AIFeatures      `json:"features"`
	// DailyTokensPerUser is each person's model token allowance per UTC day.
	DailyTokensPerUser int64 `json:"dailyTokensPerUser"`
	// MaxInputRunes caps the evidence sent in one call; older log lines go first.
	MaxInputRunes int `json:"maxInputRunes"`
	Admin         struct {
		// IncludeOwners sends app owners' names to the model in admin questions.
		IncludeOwners bool `json:"includeOwners"`
	} `json:"admin"`
}

// AIFeatures turns single AI features off while AI is on.
type AIFeatures struct {
	Explain bool `json:"explain"`
	Draft   bool `json:"draft"`
	Palette bool `json:"palette"`
	Admin   bool `json:"admin"`
}

// MCPCfg configures the Model Context Protocol endpoint for agents.
type MCPCfg struct {
	Enabled bool `json:"enabled"`
	// PublicURL is the address agents reach the gateway at, for approve links
	// and the protected resource metadata. Default: urls.scheme://urls.uiHost
	// when uiHost is set.
	PublicURL string `json:"publicURL"`
}

// PlansCfg configures proposed changes.
type PlansCfg struct {
	// TTL is how long a proposed change waits for approval; default 10m.
	TTL metav1.Duration `json:"ttl"`
}

// BackupCfg is the backup bucket.
type BackupCfg struct {
	// S3 is the bucket every database backup is copied to and the home
	// folders are backed up to with restic; nil means none.
	S3 *S3Cfg `json:"s3,omitempty"`
}

// S3Cfg is an S3-compatible backup bucket. The chart renders it from the
// backupTarget values and mounts the files it names.
type S3Cfg struct {
	// Endpoint is the address with its scheme, such as
	// https://s3.eu-west-1.amazonaws.com or http://minio.minio.svc:9000.
	Endpoint string `json:"endpoint"`
	Bucket   string `json:"bucket"`
	Region   string `json:"region,omitempty"`
	// Prefix is the folder in the bucket the backups go under.
	Prefix string `json:"prefix,omitempty"`
	// BucketLookup is auto, dns or path.
	BucketLookup       string `json:"bucketLookup,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty"`
	// CACertFile trusts the certificates in that file for the endpoint.
	CACertFile string `json:"caCertFile,omitempty"`
	// CredentialsDir holds the files access-key-id and secret-access-key;
	// PasswordFile the password every backup is encrypted with.
	CredentialsDir string `json:"credentialsDir,omitempty"`
	PasswordFile   string `json:"passwordFile,omitempty"`
	// Homes is which backups of each home folder and shared folder are
	// kept. When backups run is the backup job's schedule (Jobs page).
	Homes HomeBackupCfg `json:"homes"`
}

// HomeBackupCfg is what is kept of the folders' backups.
type HomeBackupCfg struct {
	KeepDaily   int `json:"keepDaily,omitempty"`
	KeepWeekly  int `json:"keepWeekly,omitempty"`
	KeepMonthly int `json:"keepMonthly,omitempty"`
}

// Where the chart mounts the bucket's access key and the backup password.
const (
	DefaultS3CredentialsDir = "/etc/" + model.ProductName + "-backup/s3"
	DefaultS3PasswordFile   = "/etc/" + model.ProductName + "-backup/password/password"
)

func (c *S3Cfg) applyDefaults() {
	setStr(&c.Prefix, model.ProductName)
	setStr(&c.BucketLookup, "auto")
	setStr(&c.CredentialsDir, DefaultS3CredentialsDir)
	setStr(&c.PasswordFile, DefaultS3PasswordFile)
	h := &c.Homes
	if h.KeepDaily == 0 && h.KeepWeekly == 0 && h.KeepMonthly == 0 {
		h.KeepDaily, h.KeepWeekly, h.KeepMonthly = 7, 4, 6
	}
}

func (c *S3Cfg) validate(bad func(key, format string, a ...any)) {
	// The message never repeats the address: it may hold a password.
	if u, err := url.Parse(c.Endpoint); err != nil || u.Hostname() == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		bad("backup.s3.endpoint", "must be an http or https address with a host and no user name or password")
	}
	if c.Bucket == "" {
		bad("backup.s3.bucket", "required")
	}
	switch c.BucketLookup {
	case "auto", "dns", "path":
	default:
		bad("backup.s3.bucketLookup", "%q is not auto, dns or path", c.BucketLookup)
	}
	if h := c.Homes; h.KeepDaily < 0 || h.KeepWeekly < 0 || h.KeepMonthly < 0 {
		bad("backup.s3.homes", "keepDaily, keepWeekly and keepMonthly must not be negative")
	}
}

// Target reads the access key and the password from their files.
func (c S3Cfg) Target() (restic.Target, error) {
	read := func(file string) (string, error) {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("backup.s3: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	t := restic.Target{
		Endpoint: c.Endpoint, Bucket: c.Bucket, Region: c.Region, Prefix: c.Prefix,
		BucketLookup: c.BucketLookup, InsecureTLS: c.InsecureSkipVerify, CACertFile: c.CACertFile,
	}
	var err error
	if t.AccessKeyID, err = read(filepath.Join(c.CredentialsDir, "access-key-id")); err != nil {
		return t, err
	}
	if t.SecretAccessKey, err = read(filepath.Join(c.CredentialsDir, "secret-access-key")); err != nil {
		return t, err
	}
	if t.Password, err = read(c.PasswordFile); err != nil {
		return t, err
	}
	return t, nil
}

// Keep is the homes' retention as restic's.
func (h HomeBackupCfg) Keep() restic.Keep {
	return restic.Keep{Daily: h.KeepDaily, Weekly: h.KeepWeekly, Monthly: h.KeepMonthly}
}

// SourceCfg configures apps that run from a Git repository.
type SourceCfg struct {
	// CloneImage is the git-sync image whose init container clones the
	// repository in the app's pod. Empty means apps from Git cannot start.
	CloneImage string `json:"cloneImage,omitempty"`
	// GitConfig is passed to every clone as git-sync's --git-config, in its
	// "section.key:value,section.key:value" form. Empty adds nothing. The
	// end-to-end tests use it to trust their in-cluster Git server's
	// self-signed certificate; do not turn off certificate checks in production.
	GitConfig string `json:"gitConfig,omitempty"`
	// Previews configures one preview app per open pull request.
	Previews PreviewsCfg `json:"previews"`
}

// Defaults and limits of source.previews.
const (
	DefaultPreviewPoll = 2 * time.Minute
	MinPreviewPoll     = 30 * time.Second
	DefaultGitHubAPI   = "https://api.github.com"
)

// PreviewsCfg configures previews of pull requests (source.previews). While
// Enabled is false the controller does not look at pull requests and apps are
// not offered the setting.
type PreviewsCfg struct {
	Enabled bool `json:"enabled"`
	// PollInterval is how often each app's open pull requests are listed,
	// at least MinPreviewPoll.
	PollInterval metav1.Duration `json:"pollInterval"`
	// MaxPerApp caps the previews one app may ask for, 1 to model.MaxPreviewMax.
	MaxPerApp int32     `json:"maxPerApp"`
	GitHub    GitHubCfg `json:"github"`
}

// GitHubCfg is how the controller reaches GitHub.
type GitHubCfg struct {
	// APIURL is the REST API address.
	APIURL string `json:"apiURL,omitempty"`
	// TokenFile is a file holding a token the controller uses for public
	// repositories of apps whose owner pasted none; empty means anonymous
	// requests (a low hourly limit). Give it public-repository read access
	// only; private repositories are refused without the owner's own token.
	TokenFile string `json:"tokenFile,omitempty"`
}

// StorageCfg is the admin's storage settings. A probe and the move of homes
// are asked for at run time (javo storage, the admin API) and kept in the
// database; only their timeout and how long old claims are kept are set here.
type StorageCfg struct {
	Home          model.HomeStorageSpec   `json:"home"`
	SharedFolders model.SharedFoldersSpec `json:"sharedFolders"`
	Scratch       model.ScratchSpec       `json:"scratch"`
	// TeamStorage is the default allowance of each team namespace.
	TeamStorage    model.TeamStorageSpec `json:"teamStorage"`
	ClassOverrides []model.ClassOverride `json:"classOverrides,omitempty"`
	Probe          struct {
		Timeout *metav1.Duration `json:"timeout,omitempty"`
	} `json:"probe"`
	Migration struct {
		KeepOldFor *metav1.Duration `json:"keepOldFor,omitempty"`
	} `json:"migration"`
}

// Spec is the settings as a ClusterStorageSpec, without run-time requests.
func (c StorageCfg) Spec() model.ClusterStorageSpec {
	return model.ClusterStorageSpec{
		Home: c.Home, SharedFolders: c.SharedFolders, Scratch: c.Scratch, TeamStorage: c.TeamStorage,
		ClassOverrides: c.ClassOverrides,
		Probe:          model.ProbeSpec{Timeout: c.Probe.Timeout},
		Migration:      model.MigrationSpec{KeepOldFor: c.Migration.KeepOldFor},
	}
}

func (c *StorageCfg) applyDefaults() {
	s := c.Spec()
	s.ApplyDefaults()
	c.Home, c.SharedFolders, c.Scratch = s.Home, s.SharedFolders, s.Scratch
	c.Probe.Timeout, c.Migration.KeepOldFor = s.Probe.Timeout, s.Migration.KeepOldFor
}

// IdleStopCfg is the admin's policy for "Stop when idle": an app with a
// timeout is stopped after that many minutes without traffic through the
// gateway. authz.Decide applies it to every create and change.
type IdleStopCfg struct {
	// DefaultMinutes is what a new app gets when its creator picks nothing;
	// 0 is Off.
	DefaultMinutes int32 `json:"defaultMinutes"`
	// MinMinutes and MaxMinutes bound the timeout a person may pick.
	MinMinutes int32 `json:"minMinutes"`
	MaxMinutes int32 `json:"maxMinutes"`
	// AllowOff lets people turn the stop off; omitted means true. Platform
	// admins always may.
	AllowOff *bool `json:"allowOff,omitempty"`
}

// MayTurnOff reports whether people may set an app's idle stop to Off.
func (c IdleStopCfg) MayTurnOff() bool { return c.AllowOff == nil || *c.AllowOff }

// APICfg configures personal API tokens, the optional API docs page and
// the limits on what requests may hold.
type APICfg struct {
	Tokens TokensCfg `json:"tokens"`
	Limits LimitsCfg `json:"limits"`
	Docs   struct {
		// Enabled serves the Swagger UI page at /api/docs.
		Enabled bool `json:"enabled"`
	} `json:"docs"`
}

// LimitsCfg bounds what one person, or everyone at once, may hold in the
// gateway. Zero values take the defaults (WithDefaults).
type LimitsCfg struct {
	// StreamsPerPerson is how many followed logs (app, startup step and job
	// run logs) one person may have open at once. Default 8.
	StreamsPerPerson int `json:"streamsPerPerson"`
	// ThumbnailDecodes is how many uploaded pictures the gateway decodes at
	// once, for everyone; each may take up to 64 MB. Default 4.
	ThumbnailDecodes int `json:"thumbnailDecodes"`
	// BodyReadTimeout is how long a request to the API or sign-in may take
	// to send its body. Default 60s.
	BodyReadTimeout metav1.Duration `json:"bodyReadTimeout"`
}

// WithDefaults is l with every zero value set to its default.
func (l LimitsCfg) WithDefaults() LimitsCfg {
	if l.StreamsPerPerson == 0 {
		l.StreamsPerPerson = 8
	}
	if l.ThumbnailDecodes == 0 {
		l.ThumbnailDecodes = 4
	}
	setDur(&l.BodyReadTimeout, 60*time.Second)
	return l
}

// TokensCfg limits personal API tokens.
type TokensCfg struct {
	// GroupsMaxAge is how long after the owner's last sign-in a token keeps
	// working. A token acts with the groups seen at that sign-in, so a
	// group the provider removed cannot live on through a token.
	GroupsMaxAge metav1.Duration `json:"groupsMaxAge"`
	// MaxLifetime is the longest expiry a new token may have; zero allows
	// tokens that never expire.
	MaxLifetime metav1.Duration `json:"maxLifetime"`
}

// ContainerImagesCfg says whether people may run an image of their choice,
// and from which registries.
type ContainerImagesCfg struct {
	// Enabled; omitted means true.
	Enabled *bool `json:"enabled,omitempty"`
	// AllowedRegistries are registry hosts (docker.io,
	// registry.example.com:5000) or host and path prefixes (ghcr.io/example);
	// empty allows any registry.
	AllowedRegistries []string `json:"allowedRegistries,omitempty"`
}

// IsEnabled reports whether people may run an image of their choice.
func (c ContainerImagesCfg) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Auth selects how people sign in.
type Auth struct {
	Local struct {
		Enabled bool `json:"enabled"`
	} `json:"local"`
	OIDC *OIDC `json:"oidc,omitempty"`
}

// OIDC configures one OpenID Connect provider.
type OIDC struct {
	Issuer           string   `json:"issuer"`
	ClientID         string   `json:"clientID"`
	ClientSecretFile string   `json:"clientSecretFile,omitempty"`
	DisplayName      string   `json:"displayName,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	// UsernameClaim names the display-name claim (default preferred_username).
	UsernameClaim string `json:"usernameClaim,omitempty"`
	// GroupsClaim is a dotted path to the groups claim (default groups).
	GroupsClaim string `json:"groupsClaim,omitempty"`
	// RolesClaim is a dotted path to a claim listing the provider's roles
	// (resource_access.<client>.roles); empty reads none.
	RolesClaim         string          `json:"rolesClaim,omitempty"`
	RevalidateInterval metav1.Duration `json:"revalidateInterval,omitempty"`
	OutageGrace        metav1.Duration `json:"outageGrace,omitempty"`
	// Dial maps host:port to host:port, for dev setups only.
	Dial map[string]string `json:"dial,omitempty"`
}

// Roles binds groups to roles. Empty Member.Groups means every signed-in
// person is a member.
type Roles struct {
	PlatformAdmin RoleBinding `json:"platformAdmin"`
	TemplateAdmin RoleBinding `json:"templateAdmin"`
	Member        RoleBinding `json:"member"`
}

// RoleBinding lists the groups that hold a role, verbatim as the IdP emits them.
type RoleBinding struct {
	Groups []string `json:"groups,omitempty"`
}

// URLs says where apps are served.
type URLs struct {
	Mode       string `json:"mode"` // path or subdomain
	UIHost     string `json:"uiHost,omitempty"`
	AppsHost   string `json:"appsHost,omitempty"`
	AppsDomain string `json:"appsDomain,omitempty"`
	Scheme     string `json:"scheme,omitempty"`
}

// Namespaces says where workspaces live.
type Namespaces struct {
	Mode   string `json:"mode"` // shared, per-user or per-team
	Shared string `json:"shared,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Teams  []Team `json:"teams,omitempty"`
}

// Team maps an IdP group to a namespace slug in per-team mode.
type Team struct {
	Group string `json:"group"`
	Slug  string `json:"slug"`
	// StorageLimit is the total storage the team's claims may request. Unset
	// means the default, storage.teamStorage.limit.
	StorageLimit *resource.Quantity `json:"storageLimit,omitempty"`
}

// The values of identity.uidMode.
const (
	UIDModeFixed     = "fixed"
	UIDModeAllocated = "allocated"
)

// IdentityCfg configures POSIX uid assignment.
type IdentityCfg struct {
	UIDMode  string  `json:"uidMode"` // fixed or allocated
	UIDRange IDRange `json:"uidRange"`
	// GIDRange is where groups get their gid, the first time a shared folder
	// of the group needs one. It must not overlap UIDRange.
	GIDRange IDRange `json:"gidRange"`
	// StaleAfter is how long since last login before a person shown in a
	// sharing list is marked no longer active (never auto-removed).
	StaleAfter metav1.Duration `json:"staleAfter"`
}

// IDRange is an inclusive range of POSIX ids.
type IDRange struct {
	First int64 `json:"first"`
	Last  int64 `json:"last"`
}

// SessionCfg configures sessions.
type SessionCfg struct {
	TTL metav1.Duration `json:"ttl"`
	// SweepEvery is how often each gateway deletes expired sessions.
	SweepEvery metav1.Duration `json:"sweepEvery"`
}

// SharingCfg configures sharing.
type SharingCfg struct {
	AllowOneHost bool `json:"allowOneHost"`
}

// Load reads, strictly decodes, defaults and validates the file at path.
func Load(path string) (Platform, error) {
	var p Platform
	data, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("read config: %w", err)
	}
	js, err := yaml.YAMLToJSON(data)
	if err != nil {
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	// sigs.k8s.io/json reports unknown fields with their full dotted path.
	strict, err := json.UnmarshalStrict(js, &p)
	if err != nil {
		return p, fmt.Errorf("decode %s: %w", path, err)
	}
	if len(strict) > 0 {
		return p, fmt.Errorf("decode %s: %w", path, errors.Join(strict...))
	}
	p.applyDefaults()
	if err := p.Validate(); err != nil {
		return p, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func (p *Platform) applyDefaults() {
	setStr(&p.URLs.Mode, "path")
	setStr(&p.Namespaces.Mode, "shared")
	setStr(&p.Namespaces.Shared, model.DefaultWorkspaceNamespace)
	setStr(&p.Namespaces.Prefix, model.ProductName)
	setStr(&p.Identity.UIDMode, UIDModeAllocated)
	if p.Identity.UIDRange.First == 0 && p.Identity.UIDRange.Last == 0 {
		p.Identity.UIDRange.First, p.Identity.UIDRange.Last = 100000, 199999
	}
	if p.Identity.GIDRange.First == 0 && p.Identity.GIDRange.Last == 0 {
		p.Identity.GIDRange.First, p.Identity.GIDRange.Last = 200000, 299999
	}
	p.Storage.applyDefaults()
	p.Branding.applyDefaults()
	p.Limits.applyDefaults()
	if p.Backup.S3 != nil {
		p.Backup.S3.applyDefaults()
	}
	setDur(&p.Identity.StaleAfter, 90*24*time.Hour)
	setDur(&p.Sessions.TTL, 12*time.Hour)
	setDur(&p.Sessions.SweepEvery, 5*time.Minute)
	setDur(&p.API.Tokens.GroupsMaxAge, 7*24*time.Hour)
	p.API.Limits = p.API.Limits.WithDefaults()
	setDur(&p.AI.Timeout, 60*time.Second)
	setDur(&p.Plans.TTL, 10*time.Minute)
	if p.AI.DailyTokensPerUser == 0 {
		p.AI.DailyTokensPerUser = 200000
	}
	if p.AI.MaxInputRunes == 0 {
		p.AI.MaxInputRunes = 24000
	}
	if p.IdleStop.MinMinutes == 0 {
		p.IdleStop.MinMinutes = 10
	}
	if p.IdleStop.MaxMinutes == 0 {
		p.IdleStop.MaxMinutes = 7 * 24 * 60
	}
	setDur(&p.Source.Previews.PollInterval, DefaultPreviewPoll)
	if p.Source.Previews.MaxPerApp == 0 {
		p.Source.Previews.MaxPerApp = model.MaxPreviewMax
	}
	setStr(&p.Source.Previews.GitHub.APIURL, DefaultGitHubAPI)
	if p.URLs.UIHost != "" {
		setStr(&p.MCP.PublicURL, p.URLs.URLScheme()+"://"+p.URLs.UIHost)
	}
	if o := p.Auth.OIDC; o != nil {
		setStr(&o.UsernameClaim, "preferred_username")
		setStr(&o.GroupsClaim, "groups")
		setDur(&o.RevalidateInterval, 5*time.Minute)
		setDur(&o.OutageGrace, 15*time.Minute)
	}
}

func setStr(s *string, def string) {
	if *s == "" {
		*s = def
	}
}

func setDur(d *metav1.Duration, def time.Duration) {
	if d.Duration == 0 {
		d.Duration = def
	}
}

// Validate returns every problem joined, each naming its config key. The
// messages are for operators (logs, helm install), not end-user copy.
func (p Platform) Validate() error {
	var errs []error
	bad := func(key, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, a...)))
	}
	positive := func(key string, d metav1.Duration) {
		if d.Duration <= 0 {
			bad(key, "%s must be positive", d.Duration)
		}
	}
	positive("sessions.ttl", p.Sessions.TTL)
	positive("sessions.sweepEvery", p.Sessions.SweepEvery)
	positive("identity.staleAfter", p.Identity.StaleAfter)
	positive("api.tokens.groupsMaxAge", p.API.Tokens.GroupsMaxAge)
	if l := p.API.Limits; l.StreamsPerPerson < 1 {
		bad("api.limits.streamsPerPerson", "%d must be at least 1", l.StreamsPerPerson)
	}
	if l := p.API.Limits; l.ThumbnailDecodes < 1 {
		bad("api.limits.thumbnailDecodes", "%d must be at least 1", l.ThumbnailDecodes)
	}
	positive("api.limits.bodyReadTimeout", p.API.Limits.BodyReadTimeout)
	if p.API.Tokens.MaxLifetime.Duration < 0 {
		bad("api.tokens.maxLifetime", "%s must not be negative (zero allows tokens that never expire)", p.API.Tokens.MaxLifetime.Duration)
	}
	if is := p.IdleStop; is.MinMinutes < 1 {
		bad("idleStop.minMinutes", "%d must be at least 1", is.MinMinutes)
	} else if is.MaxMinutes < is.MinMinutes {
		bad("idleStop.maxMinutes", "%d is below minMinutes %d", is.MaxMinutes, is.MinMinutes)
	} else if is.DefaultMinutes == 0 && !is.MayTurnOff() {
		bad("idleStop.defaultMinutes", "0 (Off) needs allowOff: set a default between %d and %d", is.MinMinutes, is.MaxMinutes)
	} else if is.DefaultMinutes != 0 && (is.DefaultMinutes < is.MinMinutes || is.DefaultMinutes > is.MaxMinutes) {
		bad("idleStop.defaultMinutes", "%d is outside %d to %d (0 is Off)", is.DefaultMinutes, is.MinMinutes, is.MaxMinutes)
	}
	if p.AI.Enabled && (p.AI.Provider == "" || p.AI.Model == "") {
		bad("ai", "enabled needs ai.provider and ai.model")
	}
	if n := p.Nebi; n.Enabled {
		if u, err := url.Parse(n.URL); n.URL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			bad("nebi.url", "%q: enabled needs Nebi's http or https address", n.URL)
		}
		if p.Environments.PixiImage == "" {
			bad("environments.pixiImage", "required with nebi.enabled: an image with pixi on PATH installs the environments")
		}
		if p.Auth.OIDC == nil {
			bad("nebi", "enabled needs auth.oidc: Nebi is asked with the person's OIDC ID token")
		}
	}
	if !p.Auth.Local.Enabled && p.Auth.OIDC == nil {
		bad("auth", "no way to sign in: enable auth.local or configure auth.oidc")
	}
	if o := p.Auth.OIDC; o != nil {
		if o.Issuer == "" {
			bad("auth.oidc.issuer", "required")
		}
		if o.ClientID == "" {
			bad("auth.oidc.clientID", "required")
		}
		if p.URLs.UIHost == "" {
			bad("urls.uiHost", "required with auth.oidc: the sign-in redirect address is built from it, never from a request's Host header")
		}
		positive("auth.oidc.revalidateInterval", o.RevalidateInterval)
		positive("auth.oidc.outageGrace", o.OutageGrace)
	}
	switch p.URLs.Mode {
	case "path":
		if p.URLs.AppsDomain != "" {
			bad("urls.appsDomain", "only applies when urls.mode is subdomain")
		}
	case "subdomain":
		if p.URLs.AppsDomain == "" {
			bad("urls.appsDomain", "required when urls.mode is subdomain")
		}
	default:
		bad("urls.mode", "%q is not path or subdomain", p.URLs.Mode)
	}
	if p.URLs.AppsOnOtherHost() && p.URLs.UIHost == "" {
		bad("urls.uiHost", "required when apps are served on another host: it is where people get a ticket to open an app")
	}
	switch p.Namespaces.Mode {
	case "shared", "per-user":
	case "per-team":
		if len(p.Namespaces.Teams) == 0 {
			bad("namespaces.teams", "at least one team is required when namespaces.mode is per-team")
		}
	default:
		bad("namespaces.mode", "%q is not shared, per-user or per-team", p.Namespaces.Mode)
	}
	for i, t := range p.Namespaces.Teams {
		if msgs := validation.IsDNS1123Label(t.Slug); len(msgs) > 0 {
			bad(fmt.Sprintf("namespaces.teams[%d].slug", i), "%q: %s", t.Slug, strings.Join(msgs, "; "))
		}
	}
	switch p.Identity.UIDMode {
	case UIDModeFixed, UIDModeAllocated:
	default:
		bad("identity.uidMode", "%q is not fixed or allocated", p.Identity.UIDMode)
	}
	if r := p.Identity.UIDRange; r.First <= 0 || r.Last < r.First {
		bad("identity.uidRange", "first %d, last %d is not a positive ascending range", r.First, r.Last)
	}
	if r, u := p.Identity.GIDRange, p.Identity.UIDRange; r.First <= 0 || r.Last < r.First {
		bad("identity.gidRange", "first %d, last %d is not a positive ascending range", r.First, r.Last)
	} else if r.First <= u.Last && u.First <= r.Last {
		bad("identity.gidRange", "%d to %d overlaps identity.uidRange %d to %d", r.First, r.Last, u.First, u.Last)
	}
	spec := p.Storage.Spec()
	if err := spec.Validate(); err != nil {
		errs = append(errs, err)
	}
	if d := p.Source.Previews.PollInterval.Duration; d < MinPreviewPoll {
		bad("source.previews.pollInterval", "%s is below the minimum %s", d, MinPreviewPoll)
	}
	if n := p.Source.Previews.MaxPerApp; n < 1 || n > model.MaxPreviewMax {
		bad("source.previews.maxPerApp", "%d is not between 1 and %d", n, model.MaxPreviewMax)
	}
	// The message never repeats the address: it may hold a password.
	if u, err := url.Parse(p.Source.Previews.GitHub.APIURL); err != nil || u.Hostname() == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		bad("source.previews.github.apiURL", "must be an http or https address with a host and no user name or password")
	}
	if p.Backup.S3 != nil {
		p.Backup.S3.validate(bad)
	}
	p.Branding.validate(bad)
	p.Limits.validate(bad)
	if p.Limits.Namespace.IsSet() && p.Namespaces.Mode == "shared" {
		bad("limits.namespace", "only applies when namespaces.mode is per-user or per-team: in the shared namespace one quota would cap everyone together")
	}
	for i, r := range p.ContainerImages.AllowedRegistries {
		if r == "" || strings.Contains(r, "://") || strings.ContainsAny(r, " \t\n") {
			bad(fmt.Sprintf("containerImages.allowedRegistries[%d]", i), "%q is not a registry host such as ghcr.io or a host and path such as ghcr.io/example", r)
		}
	}
	return errors.Join(errs...)
}

// AppsOnOtherHost is true when apps are not served from the UI's host. An
// empty AppsHost with no AppsDomain means apps share the UI host.
func (u URLs) AppsOnOtherHost() bool {
	return u.AppsDomain != "" || u.AppsHost != "" && u.AppsHost != u.UIHost
}

// SecureCookies is true when the gateway's cookies are Secure: always with
// https, and with http only when forced (--secure-cookies).
func (u URLs) SecureCookies(force bool) bool {
	return force || u.URLScheme() == "https"
}

// URLScheme is the scheme of absolute URLs: urls.scheme, https when unset.
func (u URLs) URLScheme() string {
	if u.Scheme == "" {
		return "https"
	}
	return u.Scheme
}
