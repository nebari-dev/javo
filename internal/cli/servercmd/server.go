package servercmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/store"
)

// NewServer is `javo server`: the gateway and the controller in one process
// on one database handle. With SQLite that is the install (one process owns
// the file, and its change notices reach the controller through memory); with
// Postgres each component may instead run alone, as `javo gateway` and
// `javo controller` or with --components=gateway or --components=controller.
func NewServer() *cobra.Command {
	var common commonFlags
	var gw gatewayOptions
	var ctl controllerOptions
	var components []string
	var backup backupFlags
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the gateway and the controller in one process",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			runGateway, runController, err := parseComponents(components)
			if err != nil {
				return err
			}
			if err := backup.validate(common.database.Engine); err != nil {
				return err
			}
			platform, err := common.loadConfig()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			db, err := common.database.Open(ctx)
			if err != nil {
				return err
			}
			defer db.Close()
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			var wg sync.WaitGroup
			var mu sync.Mutex
			var errs []error
			start := func(run func(context.Context) error) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := run(ctx); err != nil {
						mu.Lock()
						errs = append(errs, err)
						mu.Unlock()
					}
					// Either component ending, with or without an error, ends the other.
					cancel()
				}()
			}
			plan := backup.plan()
			if backup.schedule != "" && platform.Backup.S3 != nil {
				// Every scheduled backup is also copied to the bucket.
				t, err := platform.Backup.S3.Target()
				if err != nil {
					return err
				}
				c := wiring.DatabaseCopier(t, db, plan.Keep)
				plan.After = func(ctx context.Context, file string) {
					if err := c.Copy(ctx, file); err != nil {
						slog.Error("copying the database backup to the bucket failed", "file", file, "error", err)
					}
				}
			}
			if backup.schedule != "" {
				start(func(ctx context.Context) error { return db.RunBackups(ctx, plan) })
			}
			if runController {
				start(func(ctx context.Context) error { return ctl.run(ctx, &common, platform, db) })
			}
			if runGateway {
				if backup.schedule != "" {
					gw.backups = plan
				}
				start(func(ctx context.Context) error { return gw.run(ctx, &common, platform, db) })
			}
			wg.Wait()
			return errors.Join(errs...)
		},
	}
	common.register(cmd.Flags())
	gw.register(cmd.Flags())
	ctl.register(cmd.Flags())
	backup.register(cmd.Flags())
	cmd.Flags().StringSliceVar(&components, "components", []string{"gateway", "controller"}, "what to run: gateway, controller or both")
	return cmd
}

// backupFlags are the server's own scheduled backups of its SQLite file.
type backupFlags struct {
	schedule, dir string
	keep          int
}

func (b *backupFlags) register(f *pflag.FlagSet) {
	f.StringVar(&b.schedule, "backup-schedule", "", "cron schedule of SQLite backups taken by this process (empty: none)")
	f.StringVar(&b.dir, "backup-dir", "", "directory the scheduled backups are written to")
	f.IntVar(&b.keep, "backup-keep", 7, "how many scheduled backups to keep")
}

func (b *backupFlags) validate(engine string) error {
	if b.schedule == "" {
		return nil
	}
	if engine != store.EngineSQLite {
		return errors.New("--backup-schedule is for SQLite; Postgres is backed up by the pg_dump CronJob of the chart")
	}
	if b.dir == "" {
		return errors.New("--backup-schedule needs --backup-dir")
	}
	if b.keep < 1 {
		return errors.New("--backup-keep must be at least 1")
	}
	if _, err := cron.ParseStandard(b.schedule); err != nil {
		return fmt.Errorf("--backup-schedule %q: %w", b.schedule, err)
	}
	return nil
}

func (b *backupFlags) plan() store.BackupPlan {
	return store.BackupPlan{Schedule: b.schedule, Dir: b.dir, Keep: b.keep}
}

// parseComponents reads --components of `javo server`.
func parseComponents(in []string) (gateway, controller bool, err error) {
	for _, c := range in {
		switch strings.TrimSpace(c) {
		case "gateway":
			gateway = true
		case "controller":
			controller = true
		default:
			return false, false, fmt.Errorf("--components: %q is not gateway or controller", c)
		}
	}
	if !gateway && !controller {
		return false, false, errors.New("--components needs at least one of gateway, controller")
	}
	return gateway, controller, nil
}
