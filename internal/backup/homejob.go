package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// HomeTag tags every snapshot of a home folder.
const HomeTag = "home"

// HomeRepo is the repository of owner's home in namespace ns under the
// bucket's prefix: one repository per home, so each can be listed, restored
// and removed on its own.
func HomeRepo(ns, owner string) string { return "homes/" + ns + "/" + owner }

// HomeSnapshots lists the backups of owner's home in namespace ns in the
// bucket t, newest first, running the restic binary bin.
func HomeSnapshots(ctx context.Context, t restic.Target, bin, ns, owner string) ([]restic.Snapshot, error) {
	return t.Repo(bin, HomeRepo(ns, owner)).Snapshots(ctx, HomeTag)
}

// FolderRepo is the repository of the shared folder name in namespace ns.
func FolderRepo(ns, name string) string { return "folders/" + ns + "/" + name }

// HomeJob is what the backup and restore Jobs of one home run (`javo backup
// job`), with the home mounted at Dir.
type HomeJob struct {
	Repo restic.Repo
	Dir  string
	Keep restic.Keep
	// Run is the full backup run the backup is part of; its snapshot is
	// tagged with RunTag(Run).
	Run string
	// Dest names the repository in the log, as s3://bucket/prefix/...; Log
	// is where each event goes (nil: nowhere).
	Dest string
	Log  *EventLog
	// ProgressEvery is how often restic's progress is logged; zero: 5s.
	ProgressEvery time.Duration
}

// JobResult is what a home's Job reports through its termination message.
type JobResult struct {
	Snapshot string `json:"snapshot,omitempty"`
	Files    int    `json:"files,omitempty"`
	Dirs     int    `json:"dirs,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Added    int64  `json:"added,omitempty"`
	// Uploaded is what went to the bucket (packed); Took the seconds the
	// backup took.
	Uploaded int64   `json:"uploaded,omitempty"`
	Took     float64 `json:"took,omitempty"`
	// UID and GID own a volume's files (`javo backup job owner`).
	UID int64 `json:"uid,omitempty"`
	GID int64 `json:"gid,omitempty"`
	// Reason is a catalog code when it did not work (or, with a Snapshot,
	// worked without some files); Evidence is restic's own lines.
	Reason   string   `json:"reason,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

// Backup backs up the home and then forgets the snapshots Keep does not
// keep. A backup that skipped unreadable files still counts: its result has
// both the snapshot and the reason.
func (j HomeJob) Backup(ctx context.Context) JobResult {
	j.Log.Event("start", "dir", j.Dir, "dest", j.Dest)
	if err := j.Repo.Ensure(ctx); err != nil {
		return j.failed(err)
	}
	tags := []string{HomeTag}
	if j.Run != "" {
		tags = append(tags, RunTag(j.Run))
	}
	every := j.ProgressEvery
	if every == 0 {
		every = 5 * time.Second
	}
	var last time.Time
	sum, err := j.Repo.BackupDirProgress(ctx, j.Dir, func(p restic.Progress) {
		if time.Since(last) < every {
			return
		}
		last = time.Now()
		rate := int64(0)
		if p.SecondsElapsed > 0 {
			rate = p.BytesDone / p.SecondsElapsed
		}
		j.Log.Event("progress", "files", fmt.Sprintf("%d/%d", p.FilesDone, p.TotalFiles),
			"bytes", Size(p.BytesDone)+"/"+Size(p.TotalBytes), "rate", Size(rate)+"/s",
			"percent", fmt.Sprintf("%.0f", p.PercentDone*100), "eta", fmt.Sprintf("%ds", p.SecondsRemaining), "errors", p.ErrorCount)
	}, tags...)
	if err != nil && sum.SnapshotID == "" {
		return j.failed(err)
	}
	out := JobResult{Snapshot: sum.SnapshotID, Files: sum.TotalFiles, Dirs: sum.DirsNew + sum.DirsChanged + sum.DirsUnchanged,
		Bytes: sum.TotalBytes, Added: sum.DataAdded, Uploaded: sum.DataAddedPacked, Took: sum.TotalDuration}
	j.Log.Event("snapshot", "id", short(sum.SnapshotID), "files", out.Files, "dirs", out.Dirs,
		"new", sum.FilesNew, "changed", sum.FilesChanged, "unchanged", sum.FilesUnchanged,
		"bytes", Size(sum.TotalBytes), "added", Size(sum.DataAdded), "uploaded", Size(sum.DataAddedPacked),
		"took", Took(sum.TotalDuration), "dest", j.Dest)
	if err != nil {
		f := j.failed(err)
		out.Reason, out.Evidence = f.Reason, f.Evidence
	}
	got, ferr := j.Repo.ForgetCount(ctx, j.Keep, HomeTag)
	if ferr != nil {
		f := j.failed(ferr)
		if out.Reason == "" {
			out.Reason, out.Evidence = f.Reason, f.Evidence
		}
		return out
	}
	j.Log.Event("forget", "kept", got.Kept, "removed", got.Removed,
		"policy", fmt.Sprintf("daily=%d weekly=%d monthly=%d", j.Keep.Daily, j.Keep.Weekly, j.Keep.Monthly))
	return out
}

// failed logs err as an event and returns it as a result.
func (j HomeJob) failed(err error) JobResult {
	r := failed(err)
	j.Log.Event("failed", "reason", r.Reason, "evidence", strings.Join(r.Evidence, "; "))
	return r
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Restore brings the home back from snapshot: with model.RestoreReplace it
// becomes exactly as it was, with model.RestoreBeside the files go into the
// folder named folder inside it and nothing else changes.
func (j HomeJob) Restore(ctx context.Context, snapshot, mode, folder string) JobResult {
	j.Log.Event("restore", "snapshot", short(snapshot), "mode", mode, "dir", j.Dir, "from", j.Dest)
	var err error
	switch mode {
	case model.RestoreReplace:
		err = j.Repo.Restore(ctx, snapshot, j.Dir, j.Dir, true)
	case model.RestoreBeside:
		if folder == "" || filepath.Base(folder) != folder || folder == "." || folder == ".." {
			return JobResult{Reason: string(reasons.BackupFailed), Evidence: []string{"no folder to restore beside the files into"}}
		}
		err = j.Repo.Restore(ctx, snapshot, j.Dir, filepath.Join(j.Dir, folder), false)
	default:
		return JobResult{Reason: string(reasons.BackupFailed), Evidence: []string{"unknown restore mode " + mode}}
	}
	if err != nil {
		return j.failed(err)
	}
	j.Log.Event("restored", "snapshot", short(snapshot), "mode", mode)
	return JobResult{Snapshot: snapshot}
}

func failed(err error) JobResult {
	var re *restic.Error
	if errors.As(err, &re) {
		return JobResult{Reason: string(re.Code), Evidence: re.Lines}
	}
	return JobResult{Reason: string(reasons.BackupFailed), Evidence: []string{err.Error()}}
}

// terminationLimit is below the 4096 bytes Kubernetes keeps.
const terminationLimit = 3800

// Message is the result as a termination message: JSON, with the oldest
// evidence lines dropped until it fits.
func (r JobResult) Message() string {
	for {
		b, _ := json.Marshal(r)
		if len(b) <= terminationLimit || len(r.Evidence) == 0 {
			return string(b)
		}
		r.Evidence = r.Evidence[1:]
	}
}

// ParseJobResult reads a termination message Message wrote.
func ParseJobResult(msg string) (JobResult, bool) {
	var r JobResult
	if json.Unmarshal([]byte(msg), &r) != nil || (r.Snapshot == "" && r.Reason == "" && r.UID == 0) {
		return JobResult{}, false
	}
	return r, true
}

// Owner is who owns a file.
type Owner struct{ UID, GID int64 }

// PickOwner says who owns a volume's files: the owner of its root, unless
// that is root (a storage driver made it), then whoever owns most of the
// entries in it, root aside. It is false when nobody but root owns anything.
func PickOwner(root Owner, entries []Owner) (Owner, bool) {
	if root.UID != 0 {
		return root, true
	}
	counts := map[Owner]int{}
	var best Owner
	for _, e := range entries {
		if e.UID == 0 {
			continue
		}
		counts[e]++
		if counts[e] > counts[best] || (counts[e] == counts[best] && e.UID < best.UID) {
			best = e
		}
	}
	return best, best.UID != 0
}
