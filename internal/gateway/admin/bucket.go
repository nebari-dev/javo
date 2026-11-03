package admin

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Bucket is the S3-compatible bucket backups are copied to.
// A full backup is a run of the backup job, an ordinary job from the
// built-in template "backup" (`javo backup run`): its run asks for one and
// follows it here; restores are runs of the restore job the same way.
type Bucket struct {
	Endpoint, Name, Prefix string
	// Keep is which backups of each home and folder are kept.
	Keep restic.Keep
	// CopyDatabase copies a database backup file to the bucket as part of
	// the full backup run run (empty: none); nil when this gateway takes no
	// database backups (Postgres).
	CopyDatabase func(ctx context.Context, file, run string) error
	// DatabaseCopies lists the database copies in the bucket, newest first,
	// and FetchDatabase writes one of them to the file dest.
	DatabaseCopies func(ctx context.Context) ([]restic.Snapshot, error)
	FetchDatabase  func(ctx context.Context, copy restic.Snapshot, dest string) error
	// HomeSnapshots lists the backups of owner's home in namespace ns,
	// newest first.
	HomeSnapshots func(ctx context.Context, ns, owner string) ([]restic.Snapshot, error)
}

// BackupTemplate is the built-in template of the backup and restore jobs;
// their commands tell them apart.
const BackupTemplate = model.BackupTemplate

// The commands of the backup and restore jobs (`javo backup run`, `javo
// backup restore-run`).
var (
	backupJobCommand  = []string{"javo", "backup", "run"}
	restoreJobCommand = []string{"javo", "backup", "restore-run"}
)

// bucketTimeout bounds a call to the bucket: restic retries one that does
// not answer for minutes.
const bucketTimeout = 20 * time.Second

// staleAfter is how old the newest full backup may be before the page says
// backups stopped.
const staleAfter = 48 * time.Hour

// stepLogLines is how much of a step's own log a progress report carries.
const stepLogLines = 200

// BackupTarget names the bucket, without its key.
type BackupTarget struct {
	Endpoint string `json:"endpoint" doc:"The bucket's host"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
}

// BackupJobRef is the backup or the restore job.
type BackupJobRef struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// Schedule and TimeZone are the job's schedule; empty without one.
	Schedule string `json:"schedule,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
	Paused   bool   `json:"paused"`
}

// BackupJobs are the backup and restore jobs, when they exist.
type BackupJobs struct {
	Backup  *BackupJobRef `json:"backup,omitempty"`
	Restore *BackupJobRef `json:"restore,omitempty"`
}

// BackupRunSummary is one run of the backup or restore job, as a whole.
type BackupRunSummary struct {
	// Run is the run's Kubernetes Job name, what its steps carry; RunID is
	// the job run's id (its page and log).
	Run        string       `json:"run"`
	RunID      string       `json:"runId"`
	Trigger    string       `json:"trigger"`
	StartedAt  *time.Time   `json:"startedAt,omitempty"`
	FinishedAt *time.Time   `json:"finishedAt,omitempty"`
	Outcome    string       `json:"outcome"`
	Status     BackupStatus `json:"status"`
	// What it included and how much of it failed.
	Database      string `json:"database,omitempty" doc:"The database backup file it copied"`
	Homes         int    `json:"homes" doc:"Home folders of people with an account"`
	HomesFailed   int    `json:"homesFailed"`
	Volumes       int    `json:"volumes" doc:"Home volumes with no account"`
	VolumesFailed int    `json:"volumesFailed"`
	Folders       int    `json:"folders"`
	FoldersFailed int    `json:"foldersFailed"`
}

// BackupStep is one part of a full backup or restore.
type BackupStep struct {
	Kind     string       `json:"kind" enum:"database,apps,home,folder"`
	Name     string       `json:"name"`
	ID       string       `json:"id,omitempty"`
	State    string       `json:"state" enum:"Waiting,Running,Done,Failed"`
	Status   BackupStatus `json:"status"`
	Evidence []string     `json:"evidence"`
	// Log is the step's own log (logs=true): one key=value line per event.
	Log []string `json:"log"`
	// Person is who a home belongs to ("Alice Example (alice, local)"), or
	// that it is a home volume with no account (Account false).
	Person  string `json:"person,omitempty"`
	Account bool   `json:"account"`
	Claim   string `json:"claim,omitempty"`
	Node    string `json:"node,omitempty"`
	Dest    string `json:"dest,omitempty" doc:"Where in the bucket, as s3://bucket/prefix/..."`
	// What the newest backup of it did.
	Files    int     `json:"files,omitempty"`
	Bytes    int64   `json:"bytes,omitempty"`
	Uploaded int64   `json:"uploaded,omitempty"`
	Took     float64 `json:"took,omitempty" doc:"Seconds"`
}

// noAccountLabel is how a home volume with no account reads.
const noAccountLabel = "Home volume with no account"

// BackupRunProgress is where a full backup or restore stands.
type BackupRunProgress struct {
	Run    string       `json:"run"`
	Steps  []BackupStep `json:"steps"`
	Done   bool         `json:"done"`
	Failed int          `json:"failed"`
}

// Step states.
const (
	stepWaiting = "Waiting"
	stepRunning = "Running"
	stepDone    = "Done"
	stepFailed  = "Failed"
)

// HomeBackupItem is one home folder's backups, in the page's details.
type HomeBackupItem struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	// Person is who it belongs to, or noAccountLabel (Account false).
	Person    string       `json:"person"`
	Account   bool         `json:"account"`
	Claim     string       `json:"claim,omitempty"`
	Namespace string       `json:"namespace"`
	Status    BackupStatus `json:"status"`
	// LastAt is the newest backup that worked.
	LastAt   *time.Time `json:"lastAt,omitempty"`
	Evidence []string   `json:"evidence"`
}

// bucketState is the bucket's part of the Backups page.
type bucketState struct {
	Target BackupTarget `json:"target"`
	Jobs   BackupJobs   `json:"jobs"`
	// Status sums up the backups in plain words.
	Status BackupStatus `json:"status"`
	// LastRun is the newest run of the backup job; Runs are its runs that
	// can be restored, newest first.
	LastRun     *BackupRunSummary  `json:"lastRun,omitempty"`
	Runs        []BackupRunSummary `json:"runs"`
	LastRestore *BackupRunSummary  `json:"lastRestore,omitempty"`
	KeepDaily   int                `json:"keepDaily"`
	KeepWeekly  int                `json:"keepWeekly"`
	KeepMonthly int                `json:"keepMonthly"`
	// Homes and Folders are each one's newest backup, problems first.
	Homes   []HomeBackupItem `json:"homes"`
	Folders []HomeBackupItem `json:"folders"`
}

// backupJobs finds the backup and restore jobs among every job.
func (a *API) backupJobs(ctx context.Context) (backupJob, restoreJob *model.Job, err error) {
	all, err := a.DB.Jobs().ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range all {
		j := &all[i]
		if j.Spec.Template != BackupTemplate || !j.DeletedAt.IsZero() {
			continue
		}
		switch {
		case isCommand(j.Spec.Command, backupJobCommand) && backupJob == nil:
			backupJob = j
		case isCommand(j.Spec.Command, restoreJobCommand) && restoreJob == nil:
			restoreJob = j
		}
	}
	return backupJob, restoreJob, nil
}

// isCommand matches a job's command against want, with or without a path
// to the binary.
func isCommand(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	return filepath.Base(got[0]) == want[0] && slices.Equal(got[1:], want[1:])
}

func jobRef(j *model.Job) *BackupJobRef {
	if j == nil {
		return nil
	}
	r := &BackupJobRef{ID: j.ID, Name: j.Name, Owner: j.Owner}
	if s := j.Spec.Schedule; s != nil {
		r.Schedule, r.TimeZone, r.Paused = s.Cron, s.TimeZone, s.Paused
	}
	return r
}

// bucket fills the bucket's part of the Backups page.
func (a *API) bucket(ctx context.Context) (*bucketState, error) {
	b := a.Backups.Bucket
	if b == nil {
		return nil, nil
	}
	host := b.Endpoint
	if u, err := url.Parse(b.Endpoint); err == nil {
		host = u.Host
	}
	out := &bucketState{
		Target: BackupTarget{Endpoint: host, Bucket: b.Name, Prefix: b.Prefix}, Runs: []BackupRunSummary{},
		KeepDaily: b.Keep.Daily, KeepWeekly: b.Keep.Weekly, KeepMonthly: b.Keep.Monthly,
		Homes: []HomeBackupItem{}, Folders: []HomeBackupItem{},
	}
	bj, rj, err := a.backupJobs(ctx)
	if err != nil {
		return nil, err
	}
	out.Jobs = BackupJobs{Backup: jobRef(bj), Restore: jobRef(rj)}
	homes, err := a.DB.Homes().List(ctx)
	if err != nil {
		return nil, err
	}
	folders, err := a.DB.SharedFolders().List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range homes {
		h := &homes[i]
		it := volumeItem(h.ID, h.Spec.Owner, h.Namespace, h.Status.Backup)
		if it.Person, it.Account, err = a.personOf(ctx, h.Spec.Owner); err != nil {
			return nil, err
		}
		it.Claim = homeClaim(h)
		out.Homes = append(out.Homes, it)
	}
	for i := range folders {
		it := volumeItem(folders[i].ID, folders[i].Name, folders[i].Namespace, folders[i].Status.Backup)
		it.Person, it.Account, it.Claim = folders[i].Spec.Group, true, folders[i].Status.ClaimName
		out.Folders = append(out.Folders, it)
	}
	problemsFirst(out.Homes)
	problemsFirst(out.Folders)

	if bj == nil {
		out.Status = noticeOf(reasons.BackupsNotSetUp, true)
		return out, nil
	}
	runs, err := a.DB.Jobs().Runs(ctx, bj.ID)
	if err != nil {
		return nil, err
	}
	var lastGood time.Time
	for i, r := range runs {
		if r.K8sJob == "" && r.Outcome != "" && r.Outcome != model.RunOutcomeRunning {
			continue
		}
		var s BackupRunSummary
		if len(out.Runs) == 0 && i == 0 {
			p, err := a.backupProgress(ctx, r.K8sJob, false)
			if err != nil {
				return nil, err
			}
			s = runSummary(r, &p)
			out.LastRun = &s
		} else {
			s = runSummary(r, nil)
		}
		if r.Outcome == model.RunOutcomeSucceeded || r.Outcome == model.RunOutcomeFailed {
			out.Runs = append(out.Runs, s)
			if r.Outcome == model.RunOutcomeSucceeded && lastGood.IsZero() {
				lastGood = r.FinishTime
			}
		}
	}
	switch {
	case out.LastRun == nil:
		out.Status = noticeOf(reasons.FullBackupNone, false)
	case out.LastRun.Outcome == model.RunOutcomeRunning || out.LastRun.Outcome == "":
		out.Status = noticeOf(reasons.FullBackupRunning, false)
	case lastGood.IsZero() || a.Now().Sub(lastGood) > staleAfter:
		out.Status = noticeOf(reasons.FullBackupStale, true)
		if out.LastRun.Status.Problem {
			out.Status = out.LastRun.Status
		}
	default:
		out.Status = out.LastRun.Status
	}
	if rj != nil {
		rruns, err := a.DB.Jobs().Runs(ctx, rj.ID)
		if err != nil {
			return nil, err
		}
		if len(rruns) > 0 {
			p, err := a.restoreProgress(ctx, rruns[0].K8sJob, false)
			if err != nil {
				return nil, err
			}
			s := runSummary(rruns[0], &p)
			out.LastRestore = &s
		}
	}
	return out, nil
}

// runSummary sums a job run up, with its steps when p is given.
func runSummary(r model.Run, p *BackupRunProgress) BackupRunSummary {
	s := BackupRunSummary{Run: r.K8sJob, RunID: r.ID, Trigger: r.Trigger, Outcome: r.Outcome}
	if !r.StartTime.IsZero() {
		t := r.StartTime
		s.StartedAt = &t
	} else if !r.RequestedAt.IsZero() {
		t := r.RequestedAt
		s.StartedAt = &t
	}
	if !r.FinishTime.IsZero() {
		t := r.FinishTime
		s.FinishedAt = &t
	}
	switch r.Outcome {
	case "", model.RunOutcomeRunning:
		s.Status = noticeOf(reasons.FullBackupRunning, false)
	case model.RunOutcomeSucceeded:
		s.Status = noticeOf(reasons.FullBackupDone, false)
	default:
		s.Status = noticeOf(reasons.FullBackupProblems, true)
	}
	if p == nil {
		return s
	}
	for _, st := range p.Steps {
		failed := st.State == stepFailed
		switch st.Kind {
		case "database":
			if st.State == stepDone {
				s.Database = st.Name
			}
		case "home":
			if st.Account {
				s.Homes++
				if failed {
					s.HomesFailed++
				}
			} else {
				s.Volumes++
				if failed {
					s.VolumesFailed++
				}
			}
		case "folder":
			s.Folders++
			if failed {
				s.FoldersFailed++
			}
		}
	}
	return s
}

// volumeItem is what the details say about one home's or folder's newest
// backup.
func volumeItem(id, name, ns string, b *model.HomeBackup) HomeBackupItem {
	it := HomeBackupItem{ID: id, Owner: name, Namespace: ns, Evidence: []string{}, Status: noticeOf(reasons.HomeBackupNone, false)}
	if b == nil {
		return it
	}
	if b.LastAt != nil {
		at := b.LastAt.Time
		it.LastAt = &at
	}
	switch {
	case b.Job != "":
		it.Status = noticeOf(reasons.HomeBackupRunning, false)
	case b.Reason != "":
		it.Status, it.Evidence = noticeOf(reasons.Code(b.Reason), true), append(it.Evidence, b.Evidence...)
	case b.LastAt != nil:
		it.Status = noticeOf(reasons.HomeBackedUp, false)
	}
	return it
}

func problemsFirst(items []HomeBackupItem) {
	slices.SortStableFunc(items, func(x, y HomeBackupItem) int {
		if x.Status.Problem != y.Status.Problem {
			if x.Status.Problem {
				return -1
			}
			return 1
		}
		return cmp.Or(strings.Compare(x.Owner, y.Owner), strings.Compare(x.Namespace, y.Namespace))
	})
}

// volumeStep is a home's or folder's part of the full backup run.
func volumeStep(kind, id, name string, b *model.HomeBackup, run string) BackupStep {
	st := BackupStep{Kind: kind, ID: id, Name: name, Evidence: []string{}, Log: []string{}}
	switch {
	case b == nil || b.Run != run:
		st.State, st.Status = stepWaiting, noticeOf(reasons.BackupWaiting, false)
	case b.Job != "":
		st.State, st.Status = stepRunning, noticeOf(reasons.HomeBackupRunning, false)
	case b.Reason != "":
		st.State, st.Status, st.Evidence = stepFailed, noticeOf(reasons.Code(b.Reason), true), append(st.Evidence, b.Evidence...)
	default:
		st.State, st.Status = stepDone, noticeOf(reasons.HomeBackedUp, false)
	}
	if b != nil && b.Run == run {
		st.Files, st.Bytes, st.Uploaded, st.Took = b.Files, b.Bytes, b.Uploaded, b.Took
	}
	return st
}

// backupProgress is where the full backup run run stands. With logs, every
// step that ended carries its Job's own log.
func (a *API) backupProgress(ctx context.Context, run string, logs bool) (BackupRunProgress, error) {
	p := BackupRunProgress{Run: run, Steps: []BackupStep{}}
	db := BackupStep{Kind: "database", Name: "database", Account: true, Evidence: []string{}, Log: []string{}, Dest: a.dest(backup.DatabaseRepo)}
	switch {
	case a.Backups.Engine == store.EnginePostgres:
		db.State, db.Status = stepDone, noticeOf(reasons.DatabaseBackupsByCronJob, false)
	case a.Backups.Bucket.CopyDatabase == nil || !a.filesHere():
		// SQLite without database.backup.schedule: no folder to back up into.
		db.State, db.Status = stepFailed, noticeOf(reasons.DatabaseBackupsOff, true)
	default:
		c, ok, err := backup.LastDatabaseCopy(ctx, a.DB.Facts())
		if err != nil {
			return p, err
		}
		switch {
		case !ok || c.Run != run:
			db.State, db.Status = stepWaiting, noticeOf(reasons.BackupWaiting, false)
		case c.Reason != "" && !c.FailedAt.Before(c.At):
			db.State, db.Status, db.Evidence = stepFailed, noticeOf(reasons.Code(c.Reason), true), append(db.Evidence, c.Evidence...)
		default:
			db.State, db.Status, db.Name = stepDone, noticeOf(reasons.DatabaseCopied, false), c.File
			db.Bytes, db.Uploaded, db.Took = c.Size, c.Uploaded, c.Took
			if c.Dest != "" {
				db.Dest = c.Dest
			}
			if logs {
				db.Log = databaseLog(c)
			}
		}
	}
	p.Steps = append(p.Steps, db)
	homes, err := a.DB.Homes().List(ctx)
	if err != nil {
		return p, err
	}
	for i := range homes {
		h := &homes[i]
		if h.Spec.Backup == nil || h.Spec.Backup.Run != run {
			continue
		}
		st := volumeStep("home", h.ID, h.Spec.Owner, h.Status.Backup, run)
		if st.Person, st.Account, err = a.personOf(ctx, h.Spec.Owner); err != nil {
			return p, err
		}
		st.Claim, st.Dest = homeClaim(h), a.dest(backup.HomeRepo(h.Namespace, h.Spec.Owner))
		if logs {
			st.Log, st.Node = a.stepLog(ctx, h.Namespace, st.State, h.Status.Backup)
		}
		p.Steps = append(p.Steps, st)
	}
	folders, err := a.DB.SharedFolders().List(ctx)
	if err != nil {
		return p, err
	}
	for _, f := range folders {
		if f.Spec.Backup == nil || f.Spec.Backup.Run != run {
			continue
		}
		st := volumeStep("folder", f.ID, f.Name, f.Status.Backup, run)
		st.Person, st.Account, st.Claim, st.Dest = f.Spec.Group, true, f.Status.ClaimName, a.dest(backup.FolderRepo(f.Namespace, f.Name))
		if logs {
			st.Log, st.Node = a.stepLog(ctx, f.Namespace, st.State, f.Status.Backup)
		}
		p.Steps = append(p.Steps, st)
	}
	finish(&p)
	return p, nil
}

// databaseLog is the database step's log, from what its copy recorded.
func databaseLog(c backup.DatabaseCopy) []string {
	var buf strings.Builder
	l := &backup.EventLog{W: &buf, Now: func() time.Time { return c.At }, Fields: []string{"step", "database"}}
	l.Event("dump", "file", c.File, "size", backup.Size(c.Size))
	l.Event("upload", "snapshot", shortID(c.Snapshot), "added", backup.Size(c.Added), "uploaded", backup.Size(c.Uploaded),
		"took", backup.Took(c.Took), "dest", c.Dest)
	l.Event("forget", "kept", c.Kept, "removed", c.Removed)
	return strings.Split(strings.TrimSpace(buf.String()), "\n")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// dest names a repository under the bucket's prefix for people.
func (a *API) dest(sub string) string {
	b := a.Backups.Bucket
	return "s3://" + b.Name + "/" + strings.Trim(path.Join(strings.Trim(b.Prefix, "/"), sub), "/")
}

// homeClaim is the claim a home records, or the one it will get.
func homeClaim(h *model.Home) string {
	if h.Status.ClaimName != "" {
		return h.Status.ClaimName
	}
	return naming.HomeClaimName(h.Spec.Owner)
}

// personOf is how a home's owner reads, and whether they have an account.
func (a *API) personOf(ctx context.Context, owner string) (string, bool, error) {
	if a.People == nil {
		return owner, true, nil
	}
	p, err := a.People.ByName(ctx, owner)
	if errors.Is(err, store.ErrNotFound) {
		return noAccountLabel, false, nil
	} else if err != nil {
		return "", false, err
	}
	return p.Label(), true, nil
}

// stepLog is the log of a step's Job: the one running, or the one that
// ran; and the node it ran on.
func (a *API) stepLog(ctx context.Context, ns, state string, b *model.HomeBackup) ([]string, string) {
	if b == nil {
		return []string{}, ""
	}
	switch state {
	case stepRunning:
		return a.jobLog(ctx, ns, b.Job)
	case stepDone, stepFailed:
		return a.jobLog(ctx, ns, b.LastJob)
	}
	return []string{}, ""
}

func finish(p *BackupRunProgress) {
	p.Done = true
	for _, s := range p.Steps {
		switch s.State {
		case stepWaiting, stepRunning:
			p.Done = false
		case stepFailed:
			p.Failed++
		}
	}
}

// jobLog is the tail of the log of the Job name's pod in ns; empty when it
// cannot be read (the Job is gone after a day).
func (a *API) jobLog(ctx context.Context, ns, name string) ([]string, string) {
	out := []string{}
	if name == "" || a.Client == nil || a.Logs == nil {
		return out, ""
	}
	var pods corev1.PodList
	if err := a.Client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{"batch.kubernetes.io/job-name": name}); err != nil || len(pods.Items) == 0 {
		return out, ""
	}
	node := pods.Items[0].Spec.NodeName
	rc, err := a.Logs.Stream(ctx, ns, pods.Items[0].Name, &corev1.PodLogOptions{TailLines: ptrInt64(stepLogLines)})
	if err != nil {
		return out, node
	}
	defer rc.Close()
	sc := bufio.NewScanner(io.LimitReader(rc, 256<<10))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, node
}

func ptrInt64(n int64) *int64 { return &n }

// startBackup starts the full backup run run: the database is backed up and
// copied now, and every home and shared folder is asked to follow.
func (a *API) startBackup(ctx context.Context, run string) error {
	b := a.Backups.Bucket
	if b.CopyDatabase != nil && a.filesHere() {
		c, ok, err := backup.LastDatabaseCopy(ctx, a.DB.Facts())
		if err != nil {
			return err
		}
		if !ok || c.Run != run {
			name := store.BackupName(a.Now())
			file := filepath.Join(a.Backups.Dir, name)
			if err := a.DB.Backup(ctx, file); err != nil {
				return err
			}
			if err := store.PruneBackups(a.Backups.Dir, max(a.Backups.Keep, 1)); err != nil {
				return err
			}
			// The outcome is recorded for the run's progress.
			if err := b.CopyDatabase(ctx, file, run); err != nil {
				slog.Warn("copy the database backup to the bucket", "run", run, "err", err)
			}
		}
	}
	_, _, err := a.DB.Homes().RequestBackupRun(ctx, authn.Actor(ctx), run, a.Now())
	return err
}

// restoreProgress is where the restore run run stands.
func (a *API) restoreProgress(ctx context.Context, run string, logs bool) (BackupRunProgress, error) {
	p := BackupRunProgress{Run: run, Steps: []BackupStep{}}
	homes, err := a.DB.Homes().List(ctx)
	if err != nil {
		return p, err
	}
	for _, h := range homes {
		req := h.Spec.Restore
		if req == nil || req.Run != run || run == "" {
			continue
		}
		st := BackupStep{Kind: "home", ID: h.ID, Name: h.Spec.Owner, Evidence: []string{}, Log: []string{},
			State: stepWaiting, Status: noticeOf(reasons.HomeRestoreWaiting, false)}
		if s := h.Status.Restore; s != nil && s.ID == req.ID {
			switch s.Phase {
			case model.RestoreRestoring:
				st.State, st.Status = stepRunning, noticeOf(reasons.HomeRestoreRunning, false)
			case model.RestoreDone:
				st.State, st.Status = stepDone, noticeOf(reasons.HomeRestored, false)
			case model.RestoreFailed:
				st.State, st.Status, st.Evidence = stepFailed, noticeOf(reasons.HomeRestoreFailed, true), append(st.Evidence, s.Evidence...)
			}
			if logs && (st.State == stepDone || st.State == stepFailed) {
				st.Log, st.Node = a.jobLog(ctx, h.Namespace, s.Job)
			}
		}
		p.Steps = append(p.Steps, st)
	}
	finish(&p)
	return p, nil
}

type restoreRunBody struct {
	Run    string `json:"run" minLength:"1" maxLength:"63" doc:"The restore job's run (its Kubernetes Job name)"`
	From   string `json:"from" minLength:"1" maxLength:"63" doc:"The full backup run to restore (its Kubernetes Job name)"`
	Person string `json:"person,omitempty" maxLength:"253" doc:"Only this person's home folder and apps; empty: everyone's"`
	Mode   string `json:"mode,omitempty" enum:"replace,beside" doc:"How a person's files come back; everything is always replaced"`
}

// startRestore restores the full backup run from: the apps gone since come
// back held (the database copy of that run), and the home folders are
// restored from their backups of that run, for one person or everyone.
func (a *API) startRestore(ctx context.Context, in restoreRunBody) (BackupRunProgress, error) {
	b := a.Backups.Bucket
	mode := in.Mode
	if in.Person == "" || mode == "" {
		mode = model.RestoreReplace
	}
	p := BackupRunProgress{Run: in.Run, Steps: []BackupStep{}}
	short, cancel := context.WithTimeout(ctx, bucketTimeout)
	defer cancel()

	// The apps: from the database copy of that run, held until released.
	apps := BackupStep{Kind: "apps", Name: "apps", Evidence: []string{}, Log: []string{}}
	switch file, err := a.runDatabaseFile(short, in.From); {
	case errors.Is(err, errNoCopy):
		apps.State, apps.Status = stepDone, noticeOf(reasons.NotInBackupRun, false)
	case err != nil:
		return p, err
	default:
		rows, err := a.restoreApps(ctx, file, restoreScope{Owner: in.Person}, true)
		if err != nil {
			return p, err
		}
		apps.State, apps.Name, apps.Status = stepDone, file, noticeOf(reasons.DatabaseCopied, false)
		for _, r := range rows.Body.Rows {
			apps.Log = append(apps.Log, r.Owner+"/"+r.Name+": "+r.Message)
		}
	}
	p.Steps = append(p.Steps, apps)

	homes, err := a.DB.Homes().List(ctx)
	if err != nil {
		return p, err
	}
	for _, h := range homes {
		if in.Person != "" && h.Spec.Owner != in.Person {
			continue
		}
		st := BackupStep{Kind: "home", ID: h.ID, Name: h.Spec.Owner, Evidence: []string{}, Log: []string{}, State: stepWaiting,
			Status: noticeOf(reasons.HomeRestoreWaiting, false)}
		snaps, err := b.HomeSnapshots(short, h.Namespace, h.Spec.Owner)
		var re *restic.Error
		if err != nil && !(errors.As(err, &re) && re.Code == reasons.BackupRepoMissing) {
			return p, err
		}
		i := slices.IndexFunc(snaps, func(s restic.Snapshot) bool { return slices.Contains(s.Tags, backup.RunTag(in.From)) })
		if i < 0 {
			st.State, st.Status = stepFailed, noticeOf(reasons.NotInBackupRun, true)
			p.Steps = append(p.Steps, st)
			continue
		}
		req := model.HomeRestoreRequest{Run: in.Run, Snapshot: snaps[i].ID, SnapshotTime: metav1.NewTime(snaps[i].Time), Mode: mode}
		switch _, err := a.DB.Homes().RequestRestore(ctx, authn.Actor(ctx), h.ID, req, a.Now()); {
		case errors.Is(err, store.ErrRestoreRunning):
			st.State, st.Status = stepFailed, noticeOf(reasons.HomeRestoreFailed, true)
			st.Evidence = append(st.Evidence, reasons.Msg(reasons.MsgHomeRestoreRunning))
		case err != nil:
			return p, err
		}
		p.Steps = append(p.Steps, st)
	}
	finish(&p)
	return p, nil
}

var errNoCopy = errors.New("no database copy in that run")

// runDatabaseFile is the database backup file of the full backup run run in
// the backups folder, fetched from the bucket when it is no longer there.
func (a *API) runDatabaseFile(ctx context.Context, run string) (string, error) {
	b := a.Backups.Bucket
	if !a.filesHere() || b.DatabaseCopies == nil {
		return "", errNoCopy
	}
	copies, err := b.DatabaseCopies(ctx)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(copies, func(s restic.Snapshot) bool { return slices.Contains(s.Tags, backup.RunTag(run)) })
	if i < 0 {
		return "", errNoCopy
	}
	name := copies[i].File()
	if filepath.Base(name) != name || !strings.HasPrefix(name, "javo-") {
		return "", errNoCopy
	}
	dest := filepath.Join(a.Backups.Dir, name)
	if _, err := os.Stat(dest); err == nil {
		return name, nil
	}
	if err := b.FetchDatabase(ctx, copies[i], dest); err != nil {
		return "", err
	}
	return name, nil
}

type backupRunInput struct {
	Run  string `path:"run" maxLength:"63"`
	Logs bool   `query:"logs" doc:"Add each ended step's own log"`
}

type startRunInput struct {
	Body struct {
		Run string `json:"run" minLength:"1" maxLength:"63" doc:"The backup job's run (its Kubernetes Job name, JAVO_RUN_ID)"`
	}
}

type progressOutput struct {
	Body BackupRunProgress
}

type startRestoreInput struct {
	Body restoreRunBody
}

type runRequestBody struct {
	From   string `json:"from,omitempty" maxLength:"63" doc:"Restore: the full backup run to restore"`
	Person string `json:"person,omitempty" maxLength:"253"`
	Mode   string `json:"mode,omitempty" enum:"replace,beside"`
}

type runRequestInput struct {
	// Body is optional: Back up now takes none.
	Body *runRequestBody
}

type runRequestOutput struct {
	Body struct {
		Job   string `json:"job" doc:"The job's name"`
		Owner string `json:"owner"`
		RunID string `json:"runId"`
	}
}

func (a *API) registerBucket(api huma.API) {
	noBucket := func() error { return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgNoBackupBucket)) }
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminStartBackupRun", Method: http.MethodPost, Path: "/api/admin/backups/runs",
		Summary: "Start a full backup as part of a run of the backup job (its run calls this)",
		Errors:  []int{http.StatusConflict},
	}, func(ctx context.Context, in *startRunInput) (*progressOutput, error) {
		if a.Backups.Bucket == nil {
			return nil, noBucket()
		}
		if err := a.startBackup(ctx, in.Body.Run); err != nil {
			return nil, problem.Internal("start a full backup", err)
		}
		p, err := a.backupProgress(ctx, in.Body.Run, false)
		if err != nil {
			return nil, problem.Internal("read a full backup", err)
		}
		return &progressOutput{Body: p}, nil
	})
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminBackupRun", Method: http.MethodGet, Path: "/api/admin/backups/runs/{run}",
		Summary: "Where a full backup stands, step by step",
		Errors:  []int{http.StatusConflict},
	}, func(ctx context.Context, in *backupRunInput) (*progressOutput, error) {
		if a.Backups.Bucket == nil {
			return nil, noBucket()
		}
		p, err := a.backupProgress(ctx, in.Run, in.Logs)
		if err != nil {
			return nil, problem.Internal("read a full backup", err)
		}
		return &progressOutput{Body: p}, nil
	})
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminStartRestoreRun", Method: http.MethodPost, Path: "/api/admin/backups/restores",
		Summary: "Restore a full backup, for everyone or one person, as part of a run of the restore job (its run calls this)",
		Errors:  []int{http.StatusConflict, http.StatusBadGateway},
	}, func(ctx context.Context, in *startRestoreInput) (*progressOutput, error) {
		if a.Backups.Bucket == nil {
			return nil, noBucket()
		}
		p, err := a.startRestore(ctx, in.Body)
		var re *restic.Error
		switch {
		case errors.As(err, &re):
			return nil, problem.Reason(http.StatusBadGateway, re.Code)
		case err != nil:
			return nil, problem.Internal("start a restore", err)
		}
		return &progressOutput{Body: p}, nil
	})
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminRestoreRun", Method: http.MethodGet, Path: "/api/admin/backups/restores/{run}",
		Summary: "Where a restore stands, home by home",
		Errors:  []int{http.StatusConflict},
	}, func(ctx context.Context, in *backupRunInput) (*progressOutput, error) {
		if a.Backups.Bucket == nil {
			return nil, noBucket()
		}
		p, err := a.restoreProgress(ctx, in.Run, in.Logs)
		if err != nil {
			return nil, problem.Internal("read a restore", err)
		}
		return &progressOutput{Body: p}, nil
	})
	for _, restore := range []bool{false, true} {
		op := huma.Operation{
			OperationID: "adminBackUpEverythingNow", Method: http.MethodPost, Path: "/api/admin/backups/now",
			Summary: "Run the backup job now (platform admins)", DefaultStatus: http.StatusCreated,
			Errors: []int{http.StatusConflict},
		}
		if restore {
			op.OperationID, op.Path, op.Summary = "adminRestoreNow", "/api/admin/backups/restore",
				"Run the restore job now: a full backup, for everyone or one person (platform admins)"
		}
		authn.RegisterAdmin(api, a.Roles, op, func(ctx context.Context, in *runRequestInput) (*runRequestOutput, error) {
			if a.Backups.Bucket == nil {
				return nil, noBucket()
			}
			bj, rj, err := a.backupJobs(ctx)
			if err != nil {
				return nil, problem.Internal("find the backup jobs", err)
			}
			body := runRequestBody{}
			if in.Body != nil {
				body = *in.Body
			}
			job, params := bj, map[string]string(nil)
			if restore {
				if body.From == "" {
					return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgBackupRunUnknown))
				}
				// Only what was chosen: the job's defaults stand for the rest.
				params = map[string]string{"from": body.From}
				if body.Person != "" {
					params["person"] = body.Person
				}
				if body.Mode != "" {
					params["mode"] = body.Mode
				}
				job = rj
			}
			if job == nil {
				return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgNoBackupJob))
			}
			run, err := a.DB.Jobs().RequestRun(ctx, authn.Actor(ctx), job.ID, params)
			if err != nil {
				return nil, problem.Internal("request a run", err)
			}
			out := &runRequestOutput{}
			out.Body.Job, out.Body.Owner, out.Body.RunID = job.Name, job.Owner, run.ID
			return out, nil
		})
	}
}
