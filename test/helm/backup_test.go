package helm

import (
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

var (
	sqliteBackupArgs   = append(slices.Clone(sqliteArgs), "--set", "database.backup.schedule=0 3 * * *", "--set", "database.backup.keep=5")
	postgresBackupArgs = append(slices.Clone(postgresArgs), "--set", "database.backup.schedule=0 3 * * *", "--set", "database.backup.keep=5")
)

// A fresh install backs its database up every night: SQLite in the server,
// Postgres with the CronJob.
func TestDatabaseIsBackedUpNightlyByDefault(t *testing.T) {
	out, err := render(t, sqliteArgs...)
	if err != nil {
		t.Fatalf("sqlite: %v\n%s", err, out)
	}
	var d appsv1.Deployment
	object(t, out, "Deployment", "javo", &d)
	for _, want := range []string{"--backup-schedule=0 2 * * *", "--backup-keep=7"} {
		if !slices.Contains(d.Spec.Template.Spec.Containers[0].Args, want) {
			t.Errorf("sqlite: server args %v lack %s", d.Spec.Template.Spec.Containers[0].Args, want)
		}
	}
	if !object(t, out, "PersistentVolumeClaim", "javo-backups", &corev1.PersistentVolumeClaim{}) {
		t.Error("sqlite: default render has no backup claim")
	}

	out, err = render(t, postgresArgs...)
	if err != nil {
		t.Fatalf("postgres: %v\n%s", err, out)
	}
	var cj batchv1.CronJob
	if !object(t, out, "CronJob", "javo-db-backup", &cj) {
		t.Fatal("postgres: default render has no backup CronJob")
	}
	if cj.Spec.Schedule != "0 2 * * *" {
		t.Errorf("postgres: schedule %q, want 0 2 * * *", cj.Spec.Schedule)
	}
	if script := strings.Join(cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Command, " "); !strings.Contains(script, "tail -n +8") {
		t.Errorf("postgres: command %q does not keep the 7 newest", script)
	}
	if v := cronJobBackupsVolume(t, out); v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "javo-backups" {
		t.Errorf("postgres: CronJob backups volume = %+v, want the claim javo-backups", v)
	}
	if !slices.Contains(controllerArgs(t, out), "--backup-claim=javo-backups") {
		t.Error("postgres: the controller does not make the backup claim the CronJob writes to")
	}
}

// An empty schedule turns backups off, for a managed Postgres with its own.
func TestEmptyScheduleTurnsBackupsOff(t *testing.T) {
	for _, engine := range engines {
		out, err := render(t, append(slices.Clone(engineArgs(engine)), "--set", "database.backup.schedule=")...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", engine, err, out)
		}
		kinds := kindsOf(t, out)
		if len(kinds["CronJob"]) != 0 || slices.Contains(kinds["PersistentVolumeClaim"], "javo-backups") {
			t.Errorf("%s: render with no schedule has backup objects: %v", engine, kinds)
		}
		if strings.Contains(out, "--backup-") {
			t.Errorf("%s: render with no schedule passes backup flags", engine)
		}
	}
}

func TestSQLiteBackupsRunInTheServerIntoTheirOwnKeptClaim(t *testing.T) {
	out, err := render(t, sqliteBackupArgs...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	var pvc corev1.PersistentVolumeClaim
	if !object(t, out, "PersistentVolumeClaim", "javo-backups", &pvc) {
		t.Fatal("no backup claim")
	}
	if pvc.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Errorf("backup claim annotations %v: it must survive helm uninstall", pvc.Annotations)
	}
	if len(kindsOf(t, out)["CronJob"]) != 0 {
		t.Error("a SQLite install must not render a CronJob: the server takes its own backups")
	}

	var d appsv1.Deployment
	object(t, out, "Deployment", "javo", &d)
	args := d.Spec.Template.Spec.Containers[0].Args
	var dir string
	for _, want := range []string{"--backup-schedule=0 3 * * *", "--backup-keep=5"} {
		if !slices.Contains(args, want) {
			t.Errorf("server args %v lack %s", args, want)
		}
	}
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--backup-dir="); ok {
			dir = v
		}
	}
	if dir == "" {
		t.Fatalf("server args %v lack --backup-dir", args)
	}
	var mounted bool
	for _, m := range d.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.MountPath == dir {
			mounted = m.Name == "backups"
		}
	}
	if !mounted {
		t.Errorf("--backup-dir=%s is not the mount of the backups volume", dir)
	}
	for _, v := range d.Spec.Template.Spec.Volumes {
		if v.Name == "backups" && (v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "javo-backups") {
			t.Errorf("backups volume = %+v, want the claim javo-backups", v)
		}
	}
	if dir == "/var/lib/javo" {
		t.Error("backups must not be written into the database volume")
	}
}

func TestPostgresBackupsAreACronJobWithThePinnedImage(t *testing.T) {
	out, err := render(t, postgresBackupArgs...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	var cj batchv1.CronJob
	if !object(t, out, "CronJob", "javo-db-backup", &cj) {
		t.Fatal("no backup CronJob")
	}
	if cj.Spec.Schedule != "0 3 * * *" || cj.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Errorf("schedule %q, concurrency %q", cj.Spec.Schedule, cj.Spec.ConcurrencyPolicy)
	}
	pod := cj.Spec.JobTemplate.Spec.Template.Spec
	c := pod.Containers[0]
	if !strings.Contains(c.Image, "postgres:") || !strings.Contains(c.Image, "@sha256:") {
		t.Errorf("image %q must be postgres pinned by digest", c.Image)
	}
	script := strings.Join(c.Command, " ") + " " + strings.Join(c.Args, " ")
	if !strings.Contains(script, "pg_dump --format=custom") {
		t.Errorf("command %q does not run pg_dump --format=custom", script)
	}
	if !strings.Contains(script, "tail -n +6") {
		t.Errorf("command %q does not keep the 5 newest", script)
	}
	// The connection string holds a password: it comes from the Secret as an
	// environment variable, never as a literal in the manifest.
	var fromSecret bool
	for _, e := range c.Env {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "javo-db" && e.ValueFrom.SecretKeyRef.Key == "dsn" {
			fromSecret = true
		}
	}
	if !fromSecret {
		t.Errorf("env %+v does not read the dsn key of Secret javo-db", c.Env)
	}
	if pod.RestartPolicy != corev1.RestartPolicyNever && pod.RestartPolicy != corev1.RestartPolicyOnFailure {
		t.Errorf("restartPolicy %q", pod.RestartPolicy)
	}
	if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("backup container may escalate privileges")
	}
	for _, name := range deploymentNames("postgres") {
		var d appsv1.Deployment
		object(t, out, "Deployment", name, &d)
		if args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " "); strings.Contains(args, "--backup-schedule") || strings.Contains(args, "--backup-dir") {
			t.Errorf("%s gets backup flags: a Postgres install is backed up by the CronJob", name)
		}
	}
}

// With Postgres the backup claim's only user is the nightly CronJob, so on
// WaitForFirstConsumer storage (local-path, kind) a claim in the release
// would stay Pending and `helm install --wait` would time out on it. The
// controller makes the claim instead (and never deletes it); the chart only
// names it.
func TestPostgresBackupClaimIsMadeByTheControllerNotTheRelease(t *testing.T) {
	out, err := render(t, append(slices.Clone(postgresBackupArgs), "--set", "database.backup.size=9Gi", "--set", "database.backup.storageClass=fast")...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if object(t, out, "PersistentVolumeClaim", "javo-backups", &corev1.PersistentVolumeClaim{}) {
		t.Error("the release renders the backup claim: helm --wait blocks on it until the first backup")
	}
	args := controllerArgs(t, out)
	for _, want := range []string{"--backup-claim=javo-backups", "--backup-claim-size=9Gi", "--backup-claim-storage-class=fast"} {
		if !slices.Contains(args, want) {
			t.Errorf("controller args %v lack %s", args, want)
		}
	}
	if v := cronJobBackupsVolume(t, out); v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != "javo-backups" {
		t.Errorf("CronJob backups volume = %+v, want the claim javo-backups", v)
	}
	if !releaseRoleGrants(t, out, "javo-controller", "persistentvolumeclaims", "create") {
		t.Error("the controller's Role in the release namespace does not let it create the backup claim")
	}
}

// With a bucket every dump is copied off the cluster, so the CronJob only
// needs scratch space for it: no claim at all.
func TestPostgresBackupsToABucketUseScratchSpace(t *testing.T) {
	out, err := render(t, bucketArgs("postgres")...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	if object(t, out, "PersistentVolumeClaim", "javo-backups", &corev1.PersistentVolumeClaim{}) {
		t.Error("a backup claim is rendered although the dumps go to the bucket")
	}
	if v := cronJobBackupsVolume(t, out); v.EmptyDir == nil {
		t.Errorf("CronJob backups volume = %+v, want an emptyDir", v)
	}
	for _, a := range controllerArgs(t, out) {
		if strings.HasPrefix(a, "--backup-claim") {
			t.Errorf("controller makes a backup claim with a bucket: %s", a)
		}
	}
	if releaseRoleGrants(t, out, "javo-controller", "persistentvolumeclaims", "create") {
		t.Error("the controller may create claims in the release namespace although it makes none")
	}
}

// SQLite: the server mounts the claim, so it binds at install; the chart
// keeps rendering it and the controller makes nothing.
func TestSQLiteBackupClaimStaysInTheRelease(t *testing.T) {
	out, err := render(t, sqliteBackupArgs...)
	if err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	for _, a := range controllerArgs(t, out) {
		if strings.HasPrefix(a, "--backup-claim") {
			t.Errorf("SQLite server makes a backup claim: %s", a)
		}
	}
	if releaseRoleGrants(t, out, "javo-controller", "persistentvolumeclaims", "create") {
		t.Error("SQLite: the controller may create claims in the release namespace")
	}
}

func cronJobBackupsVolume(t *testing.T, out string) corev1.Volume {
	t.Helper()
	var cj batchv1.CronJob
	if !object(t, out, "CronJob", "javo-db-backup", &cj) {
		t.Fatal("no backup CronJob")
	}
	for _, v := range cj.Spec.JobTemplate.Spec.Template.Spec.Volumes {
		if v.Name == "backups" {
			return v
		}
	}
	t.Fatal("CronJob has no backups volume")
	return corev1.Volume{}
}

// releaseRoleGrants reports whether the Role name in the release namespace
// allows verb on resource (core group).
func releaseRoleGrants(t *testing.T, out, name, resource, verb string) bool {
	t.Helper()
	for _, o := range docs(t, out) {
		r, ok := o.(*rbacv1.Role)
		if !ok || r.Namespace != "javo-system" || r.Name != name {
			continue
		}
		for _, rule := range r.Rules {
			if slices.Contains(rule.APIGroups, "") && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, verb) {
				return true
			}
		}
	}
	return false
}

func TestBackupKeepMustBePositive(t *testing.T) {
	for _, engine := range engines {
		args := append(slices.Clone(engineArgs(engine)), "--set", "database.backup.schedule=0 3 * * *", "--set", "database.backup.keep=0")
		out, err := render(t, args...)
		if err == nil || !strings.Contains(out+err.Error(), "database.backup.keep") {
			t.Errorf("%s: keep=0 rendered (err %v)", engine, err)
		}
	}
}
