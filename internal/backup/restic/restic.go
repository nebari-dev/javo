// Package restic runs the restic command line (restic.net) against one
// S3-compatible bucket: the database copies and every home folder are restic
// repositories under the bucket's prefix. Backups are encrypted with the
// repository password and upload only what changed.
package restic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/reasons"
)

// Host is the host name every snapshot is taken under, so the parent of a
// backup is found whatever pod took the one before.
const Host = "javo"

// Target is the S3-compatible bucket backups go to.
type Target struct {
	// Endpoint is the S3 address with its scheme: https://s3.amazonaws.com,
	// http://minio.minio.svc:9000.
	Endpoint string
	Bucket   string
	Region   string
	// Prefix is the folder in the bucket all repositories live under.
	Prefix string
	// BucketLookup is auto, dns or path (path for most S3-compatible servers).
	BucketLookup string
	// InsecureTLS skips checking the endpoint's certificate; CACertFile
	// trusts the certificates in that file instead.
	InsecureTLS bool
	CACertFile  string

	AccessKeyID, SecretAccessKey string
	// Password encrypts every repository.
	Password string
}

// Dest names the repository at sub for people: s3://bucket/prefix/sub.
func (t Target) Dest(sub string) string {
	return "s3://" + t.Bucket + "/" + strings.Trim(path.Join(strings.Trim(t.Prefix, "/"), sub), "/")
}

// Repo is the repository at sub (such as "database" or "homes/ns/alice")
// under the target's prefix, run with the restic binary bin.
func (t Target) Repo(bin, sub string) Repo {
	p := strings.Trim(path.Join(strings.Trim(t.Prefix, "/"), sub), "/")
	url := "s3:" + strings.TrimRight(t.Endpoint, "/") + "/" + t.Bucket + "/" + p
	env := []string{
		"AWS_ACCESS_KEY_ID=" + t.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + t.SecretAccessKey,
		"RESTIC_PASSWORD=" + t.Password,
	}
	var args []string
	if t.Region != "" {
		env = append(env, "AWS_DEFAULT_REGION="+t.Region)
		args = append(args, "-o", "s3.region="+t.Region)
	}
	if t.BucketLookup != "" {
		args = append(args, "-o", "s3.bucket-lookup="+t.BucketLookup)
	}
	if t.InsecureTLS {
		args = append(args, "--insecure-tls")
	}
	if t.CACertFile != "" {
		args = append(args, "--cacert", t.CACertFile)
	}
	return Repo{Bin: bin, URL: url, Env: env, Args: args}
}

// Repo is one restic repository.
type Repo struct {
	// Bin is the restic binary; empty means "restic" on PATH.
	Bin string
	// URL is the repository (an s3: address, or a folder in tests).
	URL string
	// Env holds the password and the credentials; Args the global options.
	Env  []string
	Args []string
}

// Keep says which snapshots Forget keeps: the Last newest, and the newest
// of each of the last Daily days, Weekly weeks and Monthly months.
type Keep struct{ Last, Daily, Weekly, Monthly int }

// Summary is what one backup did.
type Summary struct {
	SnapshotID     string `json:"snapshot_id"`
	FilesNew       int    `json:"files_new"`
	FilesChanged   int    `json:"files_changed"`
	FilesUnchanged int    `json:"files_unmodified"`
	DirsNew        int    `json:"dirs_new"`
	DirsChanged    int    `json:"dirs_changed"`
	DirsUnchanged  int    `json:"dirs_unmodified"`
	// DataAdded is how many bytes of new data the backup found, and
	// DataAddedPacked how many went to the bucket (compressed).
	DataAdded       int64   `json:"data_added"`
	DataAddedPacked int64   `json:"data_added_packed"`
	TotalFiles      int     `json:"total_files_processed"`
	TotalBytes      int64   `json:"total_bytes_processed"`
	TotalDuration   float64 `json:"total_duration"` // seconds
}

// Progress is one of restic's --json status lines while a backup runs.
type Progress struct {
	SecondsElapsed   int64   `json:"seconds_elapsed"`
	SecondsRemaining int64   `json:"seconds_remaining"`
	PercentDone      float64 `json:"percent_done"`
	TotalFiles       int64   `json:"total_files"`
	FilesDone        int64   `json:"files_done"`
	TotalBytes       int64   `json:"total_bytes"`
	BytesDone        int64   `json:"bytes_done"`
	ErrorCount       int64   `json:"error_count"`
}

// Forgotten is what a forget kept and removed.
type Forgotten struct{ Kept, Removed int }

// Snapshot is one backup in a repository.
type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
	// Summary is set on snapshots made by restic 0.17 and later.
	Summary *struct {
		TotalBytes int64 `json:"total_bytes_processed"`
		TotalFiles int   `json:"total_files_processed"`
	} `json:"summary,omitempty"`
}

// File is the name a stream was backed up under (BackupStdin).
func (s Snapshot) File() string {
	if len(s.Paths) == 0 {
		return ""
	}
	return path.Base(s.Paths[0])
}

// evidenceLines caps the output an Error keeps.
const evidenceLines = 12

// Error is a restic command that failed: the catalog reason it maps to,
// its exit code and the last lines it wrote.
type Error struct {
	Code  reasons.Code
	Exit  int
	Lines []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("restic: %s (exit %d): %s", e.Code, e.Exit, strings.Join(e.Lines, "; "))
}

// Ensure makes the repository unless it exists.
func (r Repo) Ensure(ctx context.Context) error {
	_, err := r.run(ctx, nil, "cat", "config")
	var re *Error
	if errors.As(err, &re) && re.Code == reasons.BackupRepoMissing {
		_, err = r.run(ctx, nil, "init")
	}
	return err
}

// BackupDir backs up the folder dir with tags.
func (r Repo) BackupDir(ctx context.Context, dir string, tags ...string) (Summary, error) {
	return r.backup(ctx, nil, nil, append(tagArgs(tags), dir)...)
}

// BackupDirProgress is BackupDir, giving each of restic's status lines to
// progress while it runs (RESTIC_PROGRESS_FPS in Env sets how often).
func (r Repo) BackupDirProgress(ctx context.Context, dir string, progress func(Progress), tags ...string) (Summary, error) {
	return r.backup(ctx, nil, progress, append(tagArgs(tags), dir)...)
}

// BackupStdin backs up what in holds as the file name.
func (r Repo) BackupStdin(ctx context.Context, name string, in io.Reader, tags ...string) (Summary, error) {
	return r.backup(ctx, in, nil, append(tagArgs(tags), "--stdin", "--stdin-filename", name)...)
}

func (r Repo) backup(ctx context.Context, in io.Reader, progress func(Progress), args ...string) (Summary, error) {
	out, err := r.runLines(ctx, in, func(line []byte) {
		if progress == nil || !bytes.Contains(line, []byte(`"status"`)) {
			return
		}
		var m struct {
			Type string `json:"message_type"`
			Progress
		}
		if json.Unmarshal(line, &m) == nil && m.Type == "status" {
			progress(m.Progress)
		}
	}, append([]string{"backup", "--json", "--host", Host}, args...)...)
	sum, found := summaryOf(out)
	var re *Error
	if errors.As(err, &re) && re.Exit == 3 && found {
		// Exit 3: the snapshot was made without some unreadable files.
		re.Code = reasons.BackupIncomplete
		return sum, re
	}
	if err != nil {
		return Summary{}, err
	}
	if !found {
		return Summary{}, &Error{Code: reasons.BackupFailed, Lines: []string{"restic backup printed no summary"}}
	}
	return sum, nil
}

func summaryOf(out []byte) (Summary, bool) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var m struct {
			Type string `json:"message_type"`
			Summary
		}
		if json.Unmarshal(sc.Bytes(), &m) == nil && m.Type == "summary" {
			return m.Summary, true
		}
	}
	return Summary{}, false
}

// Forget removes the snapshots with tags that keep does not keep, and the
// data only they used.
func (r Repo) Forget(ctx context.Context, keep Keep, tags ...string) error {
	_, err := r.ForgetCount(ctx, keep, tags...)
	return err
}

// ForgetCount is Forget, saying how many snapshots it kept and removed.
func (r Repo) ForgetCount(ctx context.Context, keep Keep, tags ...string) (Forgotten, error) {
	// Grouped by host only: every database copy has a file name of its own,
	// and every snapshot of a run carries that run's tag.
	args := append([]string{"forget", "--json", "--prune", "--host", Host, "--group-by", "host"}, tagArgs(tags)...)
	for _, k := range []struct {
		flag string
		n    int
	}{{"--keep-last", keep.Last}, {"--keep-daily", keep.Daily}, {"--keep-weekly", keep.Weekly}, {"--keep-monthly", keep.Monthly}} {
		if k.n > 0 {
			args = append(args, k.flag, strconv.Itoa(k.n))
		}
	}
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		return Forgotten{}, err
	}
	// The groups list is the first JSON line; prune's own output follows.
	var f Forgotten
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var groups []struct {
			Keep   []json.RawMessage `json:"keep"`
			Remove []json.RawMessage `json:"remove"`
		}
		if json.Unmarshal(sc.Bytes(), &groups) == nil {
			for _, g := range groups {
				f.Kept += len(g.Keep)
				f.Removed += len(g.Remove)
			}
			break
		}
	}
	return f, nil
}

// Snapshots lists the snapshots with tags, newest first.
func (r Repo) Snapshots(ctx context.Context, tags ...string) ([]Snapshot, error) {
	out, err := r.run(ctx, nil, append([]string{"snapshots", "--json"}, tagArgs(tags)...)...)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, &Error{Code: reasons.BackupFailed, Lines: []string{"restic snapshots: " + err.Error()}}
	}
	slices.SortStableFunc(snaps, func(a, b Snapshot) int { return b.Time.Compare(a.Time) })
	return snaps, nil
}

// Restore writes the folder dir of snapshot into target. With exact, files
// in target that are not in the snapshot are deleted, so target ends up as
// dir was.
func (r Repo) Restore(ctx context.Context, snapshot, dir, target string, exact bool) error {
	args := []string{"restore", snapshot + ":" + dir, "--target", target}
	if exact {
		args = append(args, "--delete")
	}
	_, err := r.run(ctx, nil, args...)
	return err
}

// Dump writes the file of snapshot to w.
func (r Repo) Dump(ctx context.Context, snapshot, file string, w io.Writer) error {
	cmd := r.command(ctx, "dump", snapshot, "/"+strings.TrimPrefix(file, "/"))
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	return failure(ctx, cmd.Run(), stderr.Bytes())
}

func tagArgs(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	return []string{"--tag", strings.Join(tags, ",")}
}

func (r Repo) command(ctx context.Context, args ...string) *exec.Cmd {
	bin := r.Bin
	if bin == "" {
		bin = "restic"
	}
	all := append([]string{"--repo", r.URL, "--no-cache", "--retry-lock", "1m"}, r.Args...)
	cmd := exec.CommandContext(ctx, bin, append(all, args...)...)
	// Only what restic needs: never this process's own secrets.
	cmd.Env = append(slices.Clone(r.Env), "HOME="+os.TempDir(), "TMPDIR="+os.TempDir(), "PATH=/usr/bin:/bin")
	return cmd
}

// runLines runs restic, giving each line of its standard output to line as
// it comes, and returns all of it.
func (r Repo) runLines(ctx context.Context, in io.Reader, line func([]byte), args ...string) ([]byte, error) {
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	pr, pw := io.Pipe()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, io.MultiWriter(&stdout, pw), &stderr
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line(sc.Bytes())
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	err := cmd.Run()
	_ = pw.Close()
	<-done
	return stdout.Bytes(), failure(ctx, err, append(stderr.Bytes(), stdout.Bytes()...))
}

// run runs restic and returns its standard output.
func (r Repo) run(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
	cmd := r.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), failure(ctx, err, append(stderr.Bytes(), stdout.Bytes()...))
}

// failure maps a finished restic to nil or an *Error. restic retries an
// address that does not answer for minutes; a caller's deadline that ends
// it first is the bucket not answering.
func failure(ctx context.Context, err error, output []byte) error {
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: reasons.BackupUnreachable, Exit: -1, Lines: append(lastLines(output), "no answer before the deadline")}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return &Error{Code: reasons.BackupFailed, Exit: -1, Lines: []string{err.Error()}}
	}
	lines := lastLines(output)
	return &Error{Code: classify(exit.ExitCode(), string(output)), Exit: exit.ExitCode(), Lines: lines}
}

// classify names the reason of a failed restic from its exit code
// (restic.readthedocs.io, "Exit codes") and, for the network and the
// bucket, from its message.
func classify(code int, out string) reasons.Code {
	switch code {
	case 10:
		return reasons.BackupRepoMissing
	case 11:
		return reasons.BackupRepoLocked
	case 12:
		return reasons.BackupWrongPassword
	}
	lower := strings.ToLower(out)
	has := func(s ...string) bool {
		return slices.ContainsFunc(s, func(x string) bool { return strings.Contains(lower, strings.ToLower(x)) })
	}
	switch {
	case has("NoSuchBucket", "bucket does not exist", "The specified bucket does not exist"):
		return reasons.BackupBucketMissing
	case has("AccessDenied", "Access Denied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "403 Forbidden"):
		return reasons.BackupDenied
	case has("connection refused", "no such host", "i/o timeout", "dial tcp", "network is unreachable", "x509:", "tls:"):
		return reasons.BackupUnreachable
	}
	return reasons.BackupFailed
}

// lastLines is the last evidenceLines messages of out: its plain lines and,
// of its --json lines, the errors (progress and summaries are left out).
func lastLines(out []byte) []string {
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "{") {
			var m struct {
				Type    string `json:"message_type"`
				Message string `json:"message"`
				Error   struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(l), &m) != nil {
				continue
			}
			switch m.Type {
			case "exit_error":
				l = m.Message
			case "error":
				l = m.Error.Message
			default:
				continue
			}
		}
		if l == "" || strings.HasPrefix(l, "[") {
			continue
		}
		lines = append(lines, l)
	}
	return lines[max(0, len(lines)-evidenceLines):]
}
