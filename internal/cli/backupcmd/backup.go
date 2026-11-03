// Package backupcmd is `javo backup`: the database copies and the home
// folders' backups in the S3-compatible bucket, and what backup Jobs run.
package backupcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// backupActor is who the audit log names for restores asked for with the CLI.
var backupActor = model.Actor{System: model.SystemCLI}

// New is `javo backup`: the database copies and the home folders'
// backups in the S3-compatible bucket (site/src/content/docs/admin/backups.mdx). Run it in
// the server pod (kubectl exec deploy/javo -- /javo backup status), where the
// config, the bucket's key and the backup password are mounted. `backup
// job` is what the controller's Jobs run.
func New(openStore dbflag.Opener) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Backups in the S3-compatible bucket: database copies and home folders"}
	openStore = dbflag.OrRegister(openStore, cmd.PersistentFlags())
	load := wiring.ConfigFlag(cmd.PersistentFlags())
	bucket := func() (*config.S3Cfg, restic.Target, error) {
		p, err := load()
		if err != nil {
			return nil, restic.Target{}, err
		}
		if p.Backup.S3 == nil {
			return nil, restic.Target{}, errors.New("no backup bucket is configured (backupTarget in the Helm values)")
		}
		t, err := p.Backup.S3.Target()
		return p.Backup.S3, t, err
	}
	cmd.AddCommand(newBackupRunCmds()...)
	cmd.AddCommand(newBackupJobCmd(), newBackupDBCmd(bucket, openStore), newBackupStatusCmd(bucket, openStore),
		newBackupSnapshotsCmd(bucket, openStore), newBackupRestoreCmd(bucket, openStore))
	return cmd
}

type bucketLoader func() (*config.S3Cfg, restic.Target, error)

// newBackupJobCmd runs in a home's backup or restore Job: the bucket comes
// from flags, the key and the password from the environment, and the result
// goes to the termination message.
func newBackupJobCmd() *cobra.Command {
	var t restic.Target
	var repo, dir, snapshot, mode, folder, resultFile, runID string
	var fields []string
	var keep restic.Keep
	job := &cobra.Command{Use: "job", Short: "What a home's backup and restore Jobs run", Hidden: true}
	run := func(cmd *cobra.Command, do func(backup.HomeJob) backup.JobResult) error {
		t.AccessKeyID, t.SecretAccessKey, t.Password = os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("RESTIC_PASSWORD")
		// The log: one key=value line per event, with the step's context
		// first (the controller's --field values and the node it runs on).
		var ctxFields []string
		for _, f := range fields {
			if k, v, ok := strings.Cut(f, "="); ok {
				ctxFields = append(ctxFields, k, v)
			}
		}
		if node := os.Getenv("NODE_NAME"); node != "" {
			ctxFields = append(ctxFields, "node", node)
		}
		log := &backup.EventLog{W: cmd.OutOrStdout(), Fields: ctxFields}
		res := do(backup.HomeJob{Repo: t.Repo(wiring.ResticBin(), repo), Dir: dir, Keep: keep, Run: runID, Dest: t.Dest(repo), Log: log})
		msg := res.Message()
		if resultFile != "" {
			if err := os.WriteFile(resultFile, []byte(msg), 0o644); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "write the result:", err)
			}
		}
		if res.Snapshot == "" && res.Reason != "" {
			return fmt.Errorf("%s: %s", res.Reason, strings.Join(res.Evidence, "; "))
		}
		return nil
	}
	flags := func(c *cobra.Command) {
		f := c.Flags()
		f.StringVar(&t.Endpoint, "endpoint", "", "bucket address")
		f.StringVar(&t.Bucket, "bucket", "", "bucket name")
		f.StringVar(&t.Prefix, "prefix", "", "folder in the bucket")
		f.StringVar(&t.Region, "region", "", "bucket region")
		f.StringVar(&t.BucketLookup, "bucket-lookup", "auto", "auto, dns or path")
		f.BoolVar(&t.InsecureTLS, "insecure-tls", false, "do not check the bucket's certificate")
		f.StringVar(&t.CACertFile, "cacert", "", "trust these certificates for the bucket")
		f.StringVar(&repo, "repo", "", "repository under the prefix (homes/<namespace>/<owner>)")
		f.StringVar(&dir, "dir", "", "where the home is mounted")
		f.StringVar(&resultFile, "result-file", "/dev/termination-log", "where the result goes")
		f.StringArrayVar(&fields, "field", nil, "key=value to put on every log line (repeatable)")
		_ = c.MarkFlagRequired("endpoint")
		_ = c.MarkFlagRequired("bucket")
		_ = c.MarkFlagRequired("repo")
		_ = c.MarkFlagRequired("dir")
	}
	b := &cobra.Command{Use: "backup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(j backup.HomeJob) backup.JobResult { return j.Backup(cmd.Context()) })
	}}
	flags(b)
	b.Flags().IntVar(&keep.Daily, "keep-daily", 7, "daily backups kept")
	b.Flags().IntVar(&keep.Weekly, "keep-weekly", 4, "weekly backups kept")
	b.Flags().IntVar(&keep.Monthly, "keep-monthly", 6, "monthly backups kept")
	b.Flags().StringVar(&runID, "run", "", "the full backup run this backup is part of")
	r := &cobra.Command{Use: "restore", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(j backup.HomeJob) backup.JobResult { return j.Restore(cmd.Context(), snapshot, mode, folder) })
	}}
	flags(r)
	r.Flags().StringVar(&snapshot, "snapshot", "", "the backup to restore")
	r.Flags().StringVar(&mode, "mode", model.RestoreReplace, "replace or beside")
	r.Flags().StringVar(&folder, "folder", "", "with beside: the folder in the home the files go into")
	o := &cobra.Command{Use: "owner", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		ownerOf := func(path string) (backup.Owner, bool) {
			fi, err := os.Lstat(path)
			if err != nil {
				return backup.Owner{}, false
			}
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				return backup.Owner{}, false
			}
			return backup.Owner{UID: int64(st.Uid), GID: int64(st.Gid)}, true
		}
		root, ok := ownerOf(dir)
		if !ok {
			return errors.New("cannot read who owns " + dir)
		}
		var entries []backup.Owner
		if names, err := os.ReadDir(dir); err == nil {
			for _, e := range names {
				if o, ok := ownerOf(filepath.Join(dir, e.Name())); ok {
					entries = append(entries, o)
				}
			}
		}
		who, ok := backup.PickOwner(root, entries)
		res := backup.JobResult{UID: who.UID, GID: who.GID}
		if !ok {
			res.Reason, res.Evidence = string(reasons.BackupFailed), []string{dir + " and everything in it belong to root; a backup does not run as root"}
		}
		(&backup.EventLog{W: cmd.OutOrStdout()}).Event("owner", "dir", dir, "uid", res.UID, "gid", res.GID, "root_uid", root.UID, "entries", len(entries))
		if err := os.WriteFile(resultFile, []byte(res.Message()), 0o644); err != nil {
			return err
		}
		if res.Reason != "" {
			return errors.New(res.Evidence[0])
		}
		return nil
	}}
	o.Flags().StringVar(&dir, "dir", "", "the folder")
	o.Flags().StringVar(&resultFile, "result-file", "/dev/termination-log", "where the result goes")
	job.AddCommand(b, r, o)
	return job
}

func newBackupDBCmd(bucket bucketLoader, openStore dbflag.Opener) *cobra.Command {
	db := &cobra.Command{Use: "db", Short: "The database copies in the bucket"}
	copier := func(s *store.Store, keep int) (backup.DatabaseCopier, error) {
		_, t, err := bucket()
		if err != nil {
			return backup.DatabaseCopier{}, err
		}
		c := backup.DatabaseCopier{Repo: t.Repo(wiring.ResticBin(), backup.DatabaseRepo), Keep: keep}
		if s != nil {
			c.Facts = s.Facts()
		}
		return c, nil
	}
	list := &cobra.Command{Use: "list", Short: "List the database copies in the bucket, newest first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := copier(nil, 0)
			if err != nil {
				return err
			}
			snaps, err := c.List(cmd.Context())
			if err != nil {
				return err
			}
			for _, s := range snaps {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", s.ID[:min(8, len(s.ID))], s.Time.UTC().Format(time.RFC3339), s.File())
			}
			return nil
		}}
	fetch := &cobra.Command{Use: "fetch <copy id|latest> <file>", Short: "Download a database copy from the bucket into file",
		Long: "Download a copy, then restore it as the Backups page of the admin guide shows (javo db restore for SQLite, pg_restore for Postgres).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := copier(nil, 0)
			if err != nil {
				return err
			}
			snaps, err := c.List(cmd.Context())
			if err != nil {
				return err
			}
			i := slices.IndexFunc(snaps, func(s restic.Snapshot) bool { return args[0] == "latest" || strings.HasPrefix(s.ID, args[0]) })
			if i < 0 || len(snaps) == 0 {
				return fmt.Errorf("no database copy %q in the bucket", args[0])
			}
			if err := c.Fetch(cmd.Context(), snaps[i], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (copy of %s taken %s) written to %s\n", snaps[i].ID[:8], snaps[i].File(), snaps[i].Time.UTC().Format(time.RFC3339), args[1])
			return nil
		}}
	var keep int
	var newestIn string
	cp := &cobra.Command{Use: "copy [file]", Short: "Copy a database backup file to the bucket (the Postgres backup CronJob runs this)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file := ""
			switch {
			case len(args) == 1:
				file = args[0]
			case newestIn != "":
				var err error
				if file, err = newestDump(newestIn); err != nil {
					return err
				}
			default:
				return errors.New("name a file or --newest-in a directory")
			}
			return dbflag.With(cmd.Context(), openStore, func(s *store.Store) error {
				c, err := copier(s, keep)
				if err != nil {
					return err
				}
				if err := c.Copy(cmd.Context(), file); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s copied to the bucket\n", filepath.Base(file))
				return nil
			})
		}}
	cp.Flags().IntVar(&keep, "keep", 7, "copies kept in the bucket")
	cp.Flags().StringVar(&newestIn, "newest-in", "", "copy the newest backup file (javo-*.dump or javo-*.db) in this directory")
	db.AddCommand(list, fetch, cp)
	return db
}

// newestDump is the newest backup file in dir: names sort in time order.
func newestDump(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, "javo-") && (strings.HasSuffix(n, ".dump") || strings.HasSuffix(n, ".db")) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no backup file in %s", dir)
	}
	slices.Sort(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

func newBackupStatusCmd(bucket bucketLoader, openStore dbflag.Opener) *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Say how the database copies and every home folder's backups stand", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, _, err := bucket(); err != nil {
				return err
			}
			return dbflag.With(cmd.Context(), openStore, func(s *store.Store) error {
				out := cmd.OutOrStdout()
				c, ok, err := backup.LastDatabaseCopy(cmd.Context(), s.Facts())
				if err != nil {
					return err
				}
				switch {
				case !ok:
					fmt.Fprintln(out, "database: not copied to the bucket yet")
				case c.Reason != "" && c.FailedAt.After(c.At):
					fmt.Fprintf(out, "database: last copy failed %s: %s\n", c.FailedAt.UTC().Format(time.RFC3339), title(c.Reason))
					for _, l := range c.Evidence {
						fmt.Fprintf(out, "  %s\n", l)
					}
				default:
					fmt.Fprintf(out, "database: %s copied %s\n", c.File, c.At.UTC().Format(time.RFC3339))
				}
				homes, err := s.Homes().List(cmd.Context())
				if err != nil {
					return err
				}
				for _, h := range homes {
					fmt.Fprintf(out, "%s (%s): %s\n", h.Spec.Owner, h.Namespace, homeLine(&h))
				}
				return nil
			})
		}}
}

func title(code string) string {
	if r, ok := reasons.Lookup(reasons.Code(code)); ok {
		return r.Title
	}
	return code
}

func homeLine(h *model.Home) string {
	var parts []string
	b := h.Status.Backup
	switch {
	case b == nil || b.LastAt == nil && b.Reason == "":
		parts = append(parts, "not backed up yet")
	case b.Reason != "":
		parts = append(parts, "last backup failed: "+title(b.Reason))
	default:
		parts = append(parts, "backed up "+b.LastAt.UTC().Format(time.RFC3339))
	}
	if b != nil && b.Job != "" {
		parts = append(parts, "backing up now (job "+b.Job+")")
	}
	if req, st := h.Spec.Restore, h.Status.Restore; req != nil {
		phase := model.RestoreWaiting
		if st != nil && st.ID == req.ID {
			phase = st.Phase
		}
		parts = append(parts, fmt.Sprintf("restore of %s (%s): %s", req.SnapshotTime.UTC().Format(time.DateOnly), req.Mode, phase))
	}
	return strings.Join(parts, "; ")
}

func newBackupSnapshotsCmd(bucket bucketLoader, openStore dbflag.Opener) *cobra.Command {
	return &cobra.Command{Use: "snapshots <person>", Short: "List the backups of a person's home folder, newest first", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, t, err := bucket()
			if err != nil {
				return err
			}
			return dbflag.With(cmd.Context(), openStore, func(s *store.Store) error {
				homes, err := s.Homes().ListByOwner(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if len(homes) == 0 {
					return fmt.Errorf("%s has no home folder", args[0])
				}
				for _, h := range homes {
					snaps, err := backup.HomeSnapshots(cmd.Context(), t, wiring.ResticBin(), h.Namespace, h.Spec.Owner)
					if err != nil {
						return err
					}
					for _, sn := range snaps {
						fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", h.Namespace, sn.ID[:min(8, len(sn.ID))], sn.Time.UTC().Format(time.RFC3339))
					}
				}
				return nil
			})
		}}
}

func newBackupRestoreCmd(bucket bucketLoader, openStore dbflag.Opener) *cobra.Command {
	var c restoreChoice
	cmd := &cobra.Command{Use: "restore [person...] --snapshot <id|latest>",
		Short: "Bring home folders back from a backup; the person's apps stop meanwhile",
		Long: "Asks the controller to restore each person's home (or, with --all, every home) from the backup named " +
			"by --snapshot. Watch it with javo backup status or on Settings, Backups.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c.owners = args
			if c.all == (len(args) > 0) {
				return errors.New("name the people, or pass --all")
			}
			if c.mode != model.RestoreReplace && c.mode != model.RestoreBeside {
				return errors.New("--mode must be replace or beside")
			}
			_, t, err := bucket()
			if err != nil {
				return err
			}
			list := func(ctx context.Context, ns, owner string) ([]restic.Snapshot, error) {
				return backup.HomeSnapshots(ctx, t, wiring.ResticBin(), ns, owner)
			}
			return dbflag.With(cmd.Context(), openStore, func(s *store.Store) error {
				return restoreHomes(cmd.Context(), s, list, c, cmd.OutOrStdout())
			})
		}}
	cmd.Flags().BoolVar(&c.all, "all", false, "every home folder (disaster recovery)")
	cmd.Flags().StringVar(&c.snapshot, "snapshot", "latest", "a backup id from javo backup snapshots, or latest")
	cmd.Flags().StringVar(&c.mode, "mode", model.RestoreReplace, "replace: the home becomes as it was; beside: into a restored-<date> folder")
	return cmd
}

// restoreChoice is what `javo backup restore` was asked.
type restoreChoice struct {
	owners         []string
	all            bool
	snapshot, mode string
}

type snapshotLister func(ctx context.Context, ns, owner string) ([]restic.Snapshot, error)

// restoreHomes asks for the restores. With all, a home without a backup or
// with a restore under way is named and skipped; for named people it is an
// error.
func restoreHomes(ctx context.Context, db *store.Store, list snapshotLister, c restoreChoice, out io.Writer) error {
	var homes []model.Home
	if c.all {
		all, err := db.Homes().List(ctx)
		if err != nil {
			return err
		}
		homes = all
	} else {
		for _, o := range c.owners {
			hs, err := db.Homes().ListByOwner(ctx, o)
			if err != nil {
				return err
			}
			if len(hs) == 0 {
				return fmt.Errorf("%s has no home folder", o)
			}
			homes = append(homes, hs...)
		}
	}
	var errs []error
	skip := func(h *model.Home, why string) {
		fmt.Fprintf(out, "%s: %s\n", h.Spec.Owner, why)
		if !c.all {
			errs = append(errs, fmt.Errorf("%s: %s", h.Spec.Owner, why))
		}
	}
	for i := range homes {
		h := &homes[i]
		snaps, err := list(ctx, h.Namespace, h.Spec.Owner)
		var re *restic.Error
		if errors.As(err, &re) && re.Code == reasons.BackupRepoMissing {
			snaps, err = nil, nil
		}
		if err != nil {
			return err
		}
		j := slices.IndexFunc(snaps, func(s restic.Snapshot) bool {
			return c.snapshot == "latest" || s.ID == c.snapshot || strings.HasPrefix(s.ID, c.snapshot) && len(c.snapshot) >= 8
		})
		if j < 0 {
			skip(h, "no backup "+c.snapshot)
			continue
		}
		req := model.HomeRestoreRequest{Snapshot: snaps[j].ID, SnapshotTime: metav1.NewTime(snaps[j].Time), Mode: c.mode}
		switch _, err := db.Homes().RequestRestore(ctx, backupActor, h.ID, req, time.Now()); {
		case errors.Is(err, store.ErrRestoreRunning):
			skip(h, "a restore is under way")
		case err != nil:
			return err
		default:
			fmt.Fprintf(out, "%s: restoring the backup of %s (%s)\n", h.Spec.Owner, snaps[j].Time.UTC().Format(time.RFC3339), c.mode)
		}
	}
	return errors.Join(errs...)
}
