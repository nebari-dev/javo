package helm

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/templates"
)

func bucketArgs(engine string) []string {
	return append(slices.Clone(engineArgs(engine)),
		"--set", "backupTarget.enabled=true",
		"--set", "backupTarget.endpoint=https://s3.example.com",
		"--set", "backupTarget.bucket=backups",
		"--set", "backupTarget.existingSecret=bucket-key",
		"--set", "database.backup.schedule=0 3 * * *")
}

// loadConfig loads the rendered platform config as the components do.
func loadConfig(t *testing.T, out string) config.Platform {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(platformConfigMap(t, out).Data["config.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBucketIsOffByDefault(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, engineArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", engine, err, out)
		}
		if p := loadConfig(t, out); p.Backup.S3 != nil {
			t.Errorf("%s: default config names a bucket: %+v", engine, p.Backup.S3)
		}
		if object(t, out, "Secret", "javo-backup-password", &corev1.Secret{}) {
			t.Errorf("%s: a backup password without a bucket", engine)
		}
	}
}

// With a bucket: the config names it, and every component that runs restic
// or starts backup Jobs has the key and the password mounted where the
// config says. The chart does not make the password (a render without the
// cluster, helm template or Argo CD, would make a new one each time, and a
// Secret deleted and synced again would open none of the backups): the
// components make it once at start and read it through the API.
func TestBucketIsConfiguredAndItsSecretsMounted(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		out, err := render(t, bucketArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", engine, err, out)
		}
		s := loadConfig(t, out).Backup.S3
		if s == nil || s.Endpoint != "https://s3.example.com" || s.Bucket != "backups" {
			t.Fatalf("%s: config = %+v", engine, s)
		}
		if object(t, out, "Secret", "javo-backup-password", &corev1.Secret{}) {
			t.Errorf("%s: the chart renders the backup password", engine)
		}
		if g := releaseSecretGrants(t, out); !slices.Contains(g["javo-backup-password"], "get") || !slices.Contains(g[""], "create") {
			t.Errorf("%s: release namespace secrets grants %v", engine, g)
		}
		components := []string{"controller"}
		if engine == "postgres" {
			components = append(components, "gateway")
		}
		for _, c := range components {
			d := deploymentOf(t, out, engine, c)
			if !hasVolumeFromSecret(d, "bucket-key") || !hasVolumeFromSecret(d, "javo-backup-password") ||
				!mounts(d, s.CredentialsDir) || !mounts(d, filepath.Dir(s.PasswordFile)) {
				t.Errorf("%s %s: volumes %+v", engine, c, d.Spec.Template.Spec.Volumes)
			}
			if controllerArg(d, "backup-password-secret") != "javo-backup-password" || controllerArg(d, "backup-password-create") != "true" {
				t.Errorf("%s %s: args %v", engine, c, d.Spec.Template.Spec.Containers[0].Args)
			}
		}
	}
}

// Postgres: the backup CronJob copies each dump to the bucket with the javo
// image, after pg_dump wrote it.
func TestPostgresBackupCronJobCopiesTheDumpToTheBucket(t *testing.T) {
	t.Parallel()
	out, err := render(t, bucketArgs("postgres")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var cj batchv1.CronJob
	if !object(t, out, "CronJob", "javo-db-backup", &cj) {
		t.Fatal("no CronJob")
	}
	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	if len(pod.InitContainers) != 1 || pod.InitContainers[0].Name != "pg-dump" {
		t.Fatalf("init containers = %+v", pod.InitContainers)
	}
	c := pod.Containers[0]
	if args := strings.Join(c.Args, " "); !strings.Contains(args, "backup db copy --newest-in /backups") || !strings.Contains(args, "--keep=7") {
		t.Errorf("copy args = %q", args)
	}
	if !strings.HasPrefix(c.Image, "javo:") {
		t.Errorf("copy image = %s", c.Image)
	}
}

// A render with a bucket is the same every time, so a GitOps tool sees no
// change and never replaces the password.
func TestTwoBucketRendersAreIdentical(t *testing.T) {
	t.Parallel()
	for _, engine := range engines {
		first, err := render(t, bucketArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", engine, err, first)
		}
		second, err := render(t, bucketArgs(engine)...)
		if err != nil || first != second {
			t.Fatalf("%s: two renders differ (%v)", engine, err)
		}
	}
}

// A password the admin keeps elsewhere is only read, never made.
func TestBucketPasswordFromAnExistingSecret(t *testing.T) {
	t.Parallel()
	out, err := render(t, append(bucketArgs("postgres"), "--set", "backupTarget.password.existingSecret=my-pw")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, c := range []string{"controller", "gateway"} {
		d := deploymentOf(t, out, "postgres", c)
		if controllerArg(d, "backup-password-secret") != "my-pw" || controllerArg(d, "backup-password-create") != "" || !hasVolumeFromSecret(d, "my-pw") {
			t.Errorf("%s: args %v", c, d.Spec.Template.Spec.Containers[0].Args)
		}
	}
}

func TestBucketNeedsItsKey(t *testing.T) {
	t.Parallel()
	args := append(slices.Clone(sqliteArgs), "--set", "backupTarget.enabled=true", "--set", "backupTarget.endpoint=https://s3.example.com", "--set", "backupTarget.bucket=b")
	if out, err := render(t, args...); err == nil || !strings.Contains(out+err.Error(), "backupTarget.existingSecret") {
		t.Fatalf("render without the key: %v", err)
	}
}

// With a bucket the chart ships the "backup" job template: javo's own image
// (it carries restic and the javo binary), an API token for its runs, and
// only platform admins may use it. Without a bucket there is none.
func TestBackupJobTemplateComesWithTheBucketForAdminsOnly(t *testing.T) {
	t.Parallel()
	out, err := render(t, append(bucketArgs("sqlite"), "--set", "config.roles.platformAdmin.groups={/javo-admins}", "--set", "image.tag=9.9.9")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	tpl, ok := renderedTemplates(t, out)["backup"]
	if !ok {
		t.Fatal("no backup template")
	}
	var cm corev1.ConfigMap
	object(t, out, "ConfigMap", "javo-builtin-templates", &cm)
	if _, err := templates.Parse([]byte(cm.Data["backup.yaml"])); err != nil {
		t.Fatalf("the gateway would refuse the template: %v", err)
	}
	spec := tpl.Object["spec"].(map[string]any)
	if img, _ := spec["image"].(string); !strings.HasSuffix(img, ":9.9.9") {
		t.Errorf("image = %v", spec["image"])
	}
	if spec["apiToken"] != true || spec["jobOnly"] != true {
		t.Errorf("spec = %v", spec)
	}
	if g, _ := spec["allowedGroups"].([]any); len(g) != 1 || g[0] != "/javo-admins" {
		t.Errorf("allowedGroups = %v", spec["allowedGroups"])
	}
	out, err = render(t, sqliteArgs...)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := renderedTemplates(t, out)["backup"]; ok {
		t.Error("a backup template without a bucket")
	}
}
