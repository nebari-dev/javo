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

// With a bucket: the config names it, the password is made once and kept,
// and every component that runs restic or starts backup Jobs has the key and
// the password mounted where the config says.
func TestBucketIsConfiguredAndItsSecretsMounted(t *testing.T) {
	for _, engine := range engines {
		out, err := render(t, bucketArgs(engine)...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", engine, err, out)
		}
		s := loadConfig(t, out).Backup.S3
		if s == nil || s.Endpoint != "https://s3.example.com" || s.Bucket != "backups" {
			t.Fatalf("%s: config = %+v", engine, s)
		}
		var pw corev1.Secret
		if !object(t, out, "Secret", "javo-backup-password", &pw) {
			t.Fatalf("%s: no backup password", engine)
		}
		if len(pw.Data["password"]) < 32 || pw.Immutable == nil || !*pw.Immutable || pw.Annotations["helm.sh/resource-policy"] != "keep" {
			t.Errorf("%s: password secret = %+v", engine, pw.ObjectMeta)
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
		}
	}
}

// Postgres: the backup CronJob copies each dump to the bucket with the javo
// image, after pg_dump wrote it.
func TestPostgresBackupCronJobCopiesTheDumpToTheBucket(t *testing.T) {
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

func TestBucketNeedsItsKey(t *testing.T) {
	args := append(slices.Clone(sqliteArgs), "--set", "backupTarget.enabled=true", "--set", "backupTarget.endpoint=https://s3.example.com", "--set", "backupTarget.bucket=b")
	if out, err := render(t, args...); err == nil || !strings.Contains(out+err.Error(), "backupTarget.existingSecret") {
		t.Fatalf("render without the key: %v", err)
	}
}

// With a bucket the chart ships the "backup" job template: javo's own image
// (it carries restic and the javo binary), an API token for its runs, and
// only platform admins may use it. Without a bucket there is none.
func TestBackupJobTemplateComesWithTheBucketForAdminsOnly(t *testing.T) {
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
