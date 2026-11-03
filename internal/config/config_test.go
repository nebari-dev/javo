package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, yaml string) (Platform, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestLoadAppliesDefaults(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Namespaces.Mode != "shared" || p.Namespaces.Shared != "javo-apps" || p.Namespaces.Prefix != "javo" {
		t.Fatalf("namespaces = %+v", p.Namespaces)
	}
	if p.URLs.Mode != "path" {
		t.Fatalf("urls.mode = %q", p.URLs.Mode)
	}
	if p.Identity.UIDMode != "allocated" || p.Identity.UIDRange.First != 100000 || p.Identity.UIDRange.Last != 199999 {
		t.Fatalf("identity = %+v", p.Identity)
	}
	if p.Sessions.TTL.Duration.Hours() != 12 {
		t.Fatalf("sessions.ttl = %v", p.Sessions.TTL)
	}
	if p.Sessions.SweepEvery.Duration.Minutes() != 5 {
		t.Fatalf("sessions.sweepEvery = %v", p.Sessions.SweepEvery)
	}
}

func TestLoadReadsSessionSweepInterval(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\nsessions:\n  sweepEvery: 10s\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Sessions.SweepEvery.Duration.Seconds() != 10 {
		t.Fatalf("sessions.sweepEvery = %v", p.Sessions.SweepEvery)
	}
}

func TestLoadOIDCDefaults(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: false\n  oidc:\n    issuer: https://idp.example.com/auth/realms/javo\n    clientID: javo\n")
	if err != nil {
		t.Fatal(err)
	}
	o := p.Auth.OIDC
	if o.UsernameClaim != "preferred_username" || o.GroupsClaim != "groups" ||
		o.RevalidateInterval.Duration.Minutes() != 5 || o.OutageGrace.Duration.Minutes() != 15 {
		t.Fatalf("oidc defaults = %+v", o)
	}
}

func TestLoadRejectsBadConfig(t *testing.T) {
	cases := []struct{ name, yaml, key string }{
		{"unknown key", "auth:\n  local:\n    enabled: true\n  oidc:\n    issuerr: x\n", "auth.oidc.issuerr"},
		{"subdomain without domain", "auth:\n  local:\n    enabled: true\nurls:\n  mode: subdomain\n", "urls.appsDomain"},
		{"per-team without teams", "auth:\n  local:\n    enabled: true\nnamespaces:\n  mode: per-team\n", "namespaces.teams"},
		{"bad team slug", "auth:\n  local:\n    enabled: true\nnamespaces:\n  mode: per-team\n  teams:\n    - {group: /a, slug: Not_Valid}\n", "namespaces.teams[0].slug"},
		{"no way to sign in", "auth:\n  local:\n    enabled: false\n", "auth"},
		{"unknown namespace mode", "auth:\n  local:\n    enabled: true\nnamespaces:\n  mode: weird\n", "namespaces.mode"},
		{"negative session ttl", "auth:\n  local:\n    enabled: true\nsessions:\n  ttl: -1h\n", "sessions.ttl"},
		{"negative sweep interval", "auth:\n  local:\n    enabled: true\nsessions:\n  sweepEvery: -1m\n", "sessions.sweepEvery"},
		{"negative revalidate interval", "auth:\n  local:\n    enabled: false\n  oidc:\n    issuer: https://idp.example.com\n    clientID: javo\n    revalidateInterval: -5m\n", "auth.oidc.revalidateInterval"},
		{"negative outage grace", "auth:\n  local:\n    enabled: false\n  oidc:\n    issuer: https://idp.example.com\n    clientID: javo\n    outageGrace: -1m\n", "auth.oidc.outageGrace"},
		{"apps host without ui host", "auth:\n  local:\n    enabled: true\nurls:\n  appsHost: apps.example.com\n", "urls.uiHost"},
		{"subdomain apps without ui host", "auth:\n  local:\n    enabled: true\nurls:\n  mode: subdomain\n  appsDomain: apps.example.com\n", "urls.uiHost"},
		{"apps domain in path mode", "auth:\n  local:\n    enabled: true\nurls:\n  mode: path\n  uiHost: javo.example.com\n  appsDomain: apps.example.com\n", "urls.appsDomain"},
		{"oidc without issuer", "auth:\n  local:\n    enabled: false\n  oidc:\n    clientID: javo\n", "auth.oidc.issuer"},
		{"nebi without url", oidcOn + pixiOn + "nebi:\n  enabled: true\n", "nebi.url"},
		{"nebi url not http", oidcOn + pixiOn + "nebi:\n  enabled: true\n  url: nebi.example.com\n", "nebi.url"},
		{"nebi without pixi image", oidcOn + "nebi:\n  enabled: true\n  url: https://nebi.example.com\n", "environments.pixiImage"},
		{"nebi without oidc", "auth:\n  local:\n    enabled: true\n" + pixiOn + "nebi:\n  enabled: true\n  url: https://nebi.example.com\n", "nebi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want it to name %q", err, tc.key)
			}
		})
	}
}

// oidcOn is the auth section of a platform that signs in with OIDC.
const oidcOn = "auth:\n  local:\n    enabled: false\n  oidc:\n    issuer: https://idp.example.com\n    clientID: javo\n"

// pixiOn names the image that installs environments.
const pixiOn = "environments:\n  pixiImage: example.com/pixi:1\n"

func TestLoadReadsNebi(t *testing.T) {
	p, err := load(t, oidcOn+pixiOn+"nebi:\n  enabled: true\n  url: https://nebi.example.com\n")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Nebi.Enabled || p.Nebi.URL != "https://nebi.example.com" || p.Environments.PixiImage != "example.com/pixi:1" {
		t.Fatalf("nebi = %+v, environments = %+v", p.Nebi, p.Environments)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	_, err := load(t, "auth:\n  local:\n    enabled: false\nurls:\n  mode: subdomain\nnamespaces:\n  mode: per-team\n")
	if err == nil {
		t.Fatal("want error")
	}
	for _, key := range []string{"auth", "urls.appsDomain", "namespaces.teams"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("err %q does not name %q", err, key)
		}
	}
}

func TestContainerImagesDefaultToEnabledForAnyRegistry(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !p.ContainerImages.IsEnabled() || len(p.ContainerImages.AllowedRegistries) != 0 {
		t.Fatalf("containerImages = %+v", p.ContainerImages)
	}
	p, err = load(t, "auth:\n  local:\n    enabled: true\ncontainerImages:\n  enabled: false\n  allowedRegistries: [ghcr.io/example]\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.ContainerImages.IsEnabled() || p.ContainerImages.AllowedRegistries[0] != "ghcr.io/example" {
		t.Fatalf("containerImages = %+v", p.ContainerImages)
	}
}

func TestContainerImagesRegistriesAreHostsNotURLs(t *testing.T) {
	for _, entry := range []string{"https://ghcr.io", "", "ghcr.io example"} {
		_, err := load(t, "auth:\n  local:\n    enabled: true\ncontainerImages:\n  allowedRegistries: ['"+entry+"']\n")
		if err == nil || !strings.Contains(err.Error(), "containerImages.allowedRegistries[0]") {
			t.Fatalf("entry %q: err = %v", entry, err)
		}
	}
}

func TestLoadReadsAPITokensAndDocs(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\napi:\n  docs:\n    enabled: true\n  tokens:\n    groupsMaxAge: 72h\n    maxLifetime: 2160h\n")
	if err != nil {
		t.Fatal(err)
	}
	if !p.API.Docs.Enabled || p.API.Tokens.GroupsMaxAge.Duration.Hours() != 72 || p.API.Tokens.MaxLifetime.Duration.Hours() != 2160 {
		t.Fatalf("api = %+v", p.API)
	}
	p, err = load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.API.Docs.Enabled || p.API.Tokens.GroupsMaxAge.Duration != 30*24*time.Hour || p.API.Tokens.MaxLifetime.Duration != 0 {
		t.Fatalf("api defaults = %+v", p.API)
	}
	if _, err := load(t, "auth:\n  local:\n    enabled: true\napi:\n  tokens:\n    maxLifetime: -1h\n"); err == nil || !strings.Contains(err.Error(), "api.tokens.maxLifetime") {
		t.Fatalf("negative maxLifetime: %v", err)
	}
}

func TestIdleStopDefaultsToOffWithAWeekAtMost(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.IdleStop; got.DefaultMinutes != 0 || got.MinMinutes != 10 || got.MaxMinutes != 7*24*60 || !got.MayTurnOff() {
		t.Fatalf("idleStop = %+v", got)
	}
}

func TestIdleStopRejectsADefaultOutsideItsRange(t *testing.T) {
	for _, c := range []struct{ yaml, key string }{
		{"idleStop: {defaultMinutes: 5, minMinutes: 10, maxMinutes: 60}", "idleStop.defaultMinutes"},
		{"idleStop: {defaultMinutes: 90, minMinutes: 10, maxMinutes: 60}", "idleStop.defaultMinutes"},
		{"idleStop: {defaultMinutes: 0, allowOff: false}", "idleStop.defaultMinutes"},
		{"idleStop: {minMinutes: 60, maxMinutes: 30}", "idleStop.maxMinutes"},
		{"idleStop: {minMinutes: -1}", "idleStop.minMinutes"},
	} {
		_, err := load(t, "auth:\n  local:\n    enabled: true\n"+c.yaml+"\n")
		if err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s: err = %v, want one naming %s", c.yaml, err, c.key)
		}
	}
	p, err := load(t, "auth:\n  local:\n    enabled: true\nidleStop: {defaultMinutes: 60, minMinutes: 1, maxMinutes: 120, allowOff: false}\n")
	if err != nil || p.IdleStop.MayTurnOff() || p.IdleStop.MinMinutes != 1 {
		t.Fatalf("valid idleStop: %+v, %v", p.IdleStop, err)
	}
}

func TestATeamsStorageLimitIsReadAsAQuantity(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\nnamespaces:\n  mode: per-team\n  teams:\n    - {group: /a, slug: a, storageLimit: 100Gi}\n    - {group: /b, slug: b}\n")
	if err != nil {
		t.Fatal(err)
	}
	teams := p.Namespaces.Teams
	if teams[0].StorageLimit == nil || teams[0].StorageLimit.String() != "100Gi" || teams[1].StorageLimit != nil {
		t.Fatalf("limits = %v, %v", teams[0].StorageLimit, teams[1].StorageLimit)
	}
	if _, err := load(t, "auth:\n  local:\n    enabled: true\nnamespaces:\n  mode: per-team\n  teams:\n    - {group: /a, slug: a, storageLimit: lots}\n"); err == nil {
		t.Fatal("a limit that is not a quantity must be refused")
	}
}

// The storage section defaults like the old ClusterStorage object did, and
// its rules are checked at load.
func TestStorageDefaultsAndRules(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	s := p.Storage
	if s.Home.Mode != "auto" || s.Home.Size.String() != "10Gi" || s.Scratch.SizeLimit.String() != "20Gi" ||
		s.Migration.KeepOldFor.Duration != 168*time.Hour {
		t.Fatalf("storage = %+v", s)
	}
	p, err = load(t, "auth:\n  local:\n    enabled: true\nstorage:\n  home: {mode: shared, storageClassName: nfs, size: 5Gi, maxSize: 50Gi}\n  classOverrides: [{name: longhorn, rwxFile: false}]\n")
	if err != nil || p.Storage.Home.Mode != "shared" || p.Storage.ClassOverrides[0].Name != "longhorn" {
		t.Fatalf("storage = %+v, %v", p.Storage, err)
	}
	_, err = load(t, "auth:\n  local:\n    enabled: true\nstorage:\n  home: {mode: fast, size: 50Gi, maxSize: 20Gi}\n")
	if err == nil || !strings.Contains(err.Error(), "storage.home.mode") || !strings.Contains(err.Error(), "size is larger than maxSize") {
		t.Fatalf("err = %v", err)
	}
	// Probe and schedule requests are made at run time, not in the file.
	if _, err := load(t, "auth:\n  local:\n    enabled: true\nstorage:\n  probe: {nonce: x}\n"); err == nil {
		t.Fatal("a probe request in the config file was accepted")
	}
}

// Group folder gids come from their own range, which must not overlap uids.
func TestGIDRange(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if r := p.Identity.GIDRange; r.First != 200000 || r.Last != 299999 {
		t.Fatalf("gidRange = %+v", r)
	}
	_, err = load(t, "auth:\n  local:\n    enabled: true\nidentity:\n  uidRange: {first: 1000, last: 5000}\n  gidRange: {first: 4000, last: 6000}\n")
	if err == nil || !strings.Contains(err.Error(), "identity.gidRange") {
		t.Fatalf("overlapping ranges: %v", err)
	}
}

func TestSourcePreviewsDefaultsToOffWithAGentlePoll(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	pv := p.Source.Previews
	if pv.Enabled || pv.PollInterval.Duration != 2*time.Minute || pv.MaxPerApp != 10 || pv.GitHub.APIURL != "https://api.github.com" || pv.GitHub.TokenFile != "" {
		t.Fatalf("source.previews = %+v", pv)
	}
}

func TestSourceGitConfigIsReadAndEmptyByDefault(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.GitConfig != "" {
		t.Fatalf("default git config = %q", p.Source.GitConfig)
	}
	p, err = load(t, "auth:\n  local:\n    enabled: true\nsource:\n  gitConfig: http.sslVerify:false\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.GitConfig != "http.sslVerify:false" {
		t.Fatalf("git config = %q", p.Source.GitConfig)
	}
}

func TestSourcePreviewsReadsItsSettings(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\nsource:\n  previews:\n    enabled: true\n    pollInterval: 45s\n    maxPerApp: 4\n    github:\n      apiURL: https://ghe.example.com/api/v3\n      tokenFile: /etc/javo/github/token\n")
	if err != nil {
		t.Fatal(err)
	}
	pv := p.Source.Previews
	if !pv.Enabled || pv.PollInterval.Duration != 45*time.Second || pv.MaxPerApp != 4 || pv.GitHub.APIURL != "https://ghe.example.com/api/v3" || pv.GitHub.TokenFile != "/etc/javo/github/token" {
		t.Fatalf("source.previews = %+v", pv)
	}
}

func TestSourcePreviewsRejectsBadValues(t *testing.T) {
	const head = "auth:\n  local:\n    enabled: true\nsource:\n  previews:\n"
	for name, tc := range map[string]struct{ yaml, key string }{
		"poll under 30s":        {head + "    pollInterval: 10s\n", "source.previews.pollInterval"},
		"negative poll":         {head + "    pollInterval: -1m\n", "source.previews.pollInterval"},
		"cap above ten":         {head + "    maxPerApp: 11\n", "source.previews.maxPerApp"},
		"negative cap":          {head + "    maxPerApp: -1\n", "source.previews.maxPerApp"},
		"api url without host":  {head + "    github:\n      apiURL: https://\n", "source.previews.github.apiURL"},
		"api url not http(s)":   {head + "    github:\n      apiURL: ftp://api.example.com\n", "source.previews.github.apiURL"},
		"api url with password": {head + "    github:\n      apiURL: https://u:p@api.example.com\n", "source.previews.github.apiURL"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want it to name %q", err, tc.key)
			}
			if strings.Contains(err.Error(), "u:p") {
				t.Fatalf("err repeats the password: %v", err)
			}
		})
	}
}

func TestS3BackupTargetIsOffUnlessSetAndGetsDefaults(t *testing.T) {
	p, err := load(t, "auth:\n  local:\n    enabled: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Backup.S3 != nil {
		t.Fatalf("no target set, got %+v", p.Backup.S3)
	}
	p, err = load(t, "auth:\n  local:\n    enabled: true\nbackup:\n  s3:\n    endpoint: https://s3.example.com\n    bucket: backups\n")
	if err != nil {
		t.Fatal(err)
	}
	s := p.Backup.S3
	if s.Prefix != "javo" || s.BucketLookup != "auto" ||
		s.Homes.KeepDaily != 7 || s.Homes.KeepWeekly != 4 || s.Homes.KeepMonthly != 6 {
		t.Fatalf("defaults = %+v", s)
	}
}

func TestS3BackupTargetIsChecked(t *testing.T) {
	_, err := load(t, "auth:\n  local:\n    enabled: true\nbackup:\n  s3:\n    endpoint: ftp://user:pw@s3.example.com\n    bucketLookup: virtual\n    homes:\n      keepDaily: -1\n")
	if err == nil {
		t.Fatal("want error")
	}
	for _, key := range []string{"backup.s3.endpoint", "backup.s3.bucket", "backup.s3.bucketLookup", "backup.s3.homes"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("err %q does not name %q", err, key)
		}
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("err repeats the address with its password: %q", err)
	}
}

func TestS3BackupTargetReadsItsSecretsFromFiles(t *testing.T) {
	dir := t.TempDir()
	for name, v := range map[string]string{"access-key-id": "id\n", "secret-access-key": "key\n", "password": "pw\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := S3Cfg{Endpoint: "https://s3.example.com", Bucket: "b", Region: "eu-west-1", CredentialsDir: dir, PasswordFile: filepath.Join(dir, "password")}
	got, err := s.Target()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "id" || got.SecretAccessKey != "key" || got.Password != "pw" || got.Bucket != "b" || got.Region != "eu-west-1" {
		t.Fatalf("target = %+v", got)
	}
	s.PasswordFile = filepath.Join(dir, "none")
	if _, err := s.Target(); err == nil {
		t.Fatal("a missing password file is an error")
	}
}

// Agents get absolute approve links once the UI host is known; an explicit
// mcp.publicURL wins, and with no host the links stay relative.
func TestMCPPublicURLDefaultsToTheUIHost(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"from the UI host", "urls:\n  uiHost: javo.example.com\n", "https://javo.example.com"},
		{"with the URL scheme", "urls:\n  uiHost: localhost:8080\n  scheme: http\n", "http://localhost:8080"},
		{"explicit", "urls:\n  uiHost: javo.example.com\nmcp:\n  publicURL: https://agents.example.com\n", "https://agents.example.com"},
		{"no UI host", "", ""},
	} {
		p, err := load(t, "auth:\n  local:\n    enabled: true\n"+tc.yaml)
		if err != nil {
			t.Fatal(err)
		}
		if p.MCP.PublicURL != tc.want {
			t.Errorf("%s: mcp.publicURL = %q, want %q", tc.name, p.MCP.PublicURL, tc.want)
		}
	}
}
