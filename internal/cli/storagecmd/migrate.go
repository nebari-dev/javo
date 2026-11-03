package storagecmd

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/storageadmin"
	"github.com/nebari-dev/javo/internal/store"
)

func newMigrateCmd(openStore dbflag.Opener) *cobra.Command {
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Plan, schedule and watch the move of homes to the chosen storage mode",
		Long: "Changing the storage mode never touches an existing home. Schedule the move when it " +
			"suits users: each home is copied to new storage, checked, and only then switched.",
	}
	migrate.AddCommand(newMigratePlanCmd(openStore), newMigrateScheduleCmd(openStore), newMigrateStatusCmd(openStore), newMigrateRetryCmd(openStore))
	return migrate
}

func newMigratePlanCmd(openStore dbflag.Opener) *cobra.Command {
	return &cobra.Command{
		Use:   "plan",
		Short: "Say which homes would move and how much space they ask for",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			cs, err := readClusterStorage(cmd.Context(), db)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			m := cs.Status.Migration
			if m == nil || m.Users == 0 {
				fmt.Fprintln(out, "No homes need to move.")
				return nil
			}
			size := resource.NewQuantity(m.RequestedBytes, resource.BinarySI)
			fmt.Fprintf(out, "%d homes are on %s storage and the cluster uses %s (%s requested in total).\n", m.Users, m.From, m.To, size)
			fmt.Fprintf(out, "%d done, %d failed. Nothing moves until you run: javo storage migrate schedule --at <RFC3339 time>\n", m.Done, m.Failed)
			return nil
		},
	}
}

func newMigrateScheduleCmd(openStore dbflag.Opener) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "schedule --at <RFC3339 time>",
		Short: "Schedule the move: users see a banner from now, apps stop when the time comes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			when, err := time.Parse(time.RFC3339, at)
			if err != nil {
				return fmt.Errorf("--at must be an RFC 3339 time such as 2026-11-01T02:00:00Z: %w", err)
			}
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				if err := storageadmin.ScheduleMove(cmd.Context(), db.Storage(), storageActor, when); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "scheduled for %s\n", when.UTC().Format(time.RFC3339))
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "when the move starts, RFC 3339")
	_ = cmd.MarkFlagRequired("at")
	return cmd
}

func newMigrateStatusCmd(openStore dbflag.Opener) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show where each home's move stands, and how to watch a running copy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				homes, err := db.Homes().List(cmd.Context())
				if err != nil {
					return fmt.Errorf("list homes: %w", err)
				}
				printMigrations(cmd.OutOrStdout(), homes)
				return nil
			})
		},
	}
}

// newMigrateRetryCmd lets the homes whose move failed try again, as the
// admin storage page's Retry does.
func newMigrateRetryCmd(openStore dbflag.Opener) *cobra.Command {
	return &cobra.Command{
		Use:   "retry",
		Short: "Try the move again for the homes whose copy failed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				if err := storageadmin.RetryMove(cmd.Context(), db.Storage(), storageActor); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "failed homes will be tried again")
				return nil
			})
		},
	}
}

func printMigrations(out io.Writer, homes []model.Home) {
	listed := false
	for i := range homes {
		h, m := &homes[i], homes[i].Status.Migration
		if m == nil {
			continue
		}
		listed = true
		fmt.Fprintf(out, "%s: %s\n", h.Spec.Owner, m.Phase)
		switch m.Phase {
		case model.MigrationCopying:
			if m.Job != "" {
				// The copy's live progress is in the Job's log.
				fmt.Fprintf(out, "  watch the copy: kubectl logs -f job/%s -n %s\n", m.Job, h.Namespace)
			}
		case model.MigrationFailed:
			for _, e := range m.Evidence {
				fmt.Fprintf(out, "  %s\n", e)
			}
		case model.MigrationSwitched:
			if m.RetainUntil != nil {
				fmt.Fprintf(out, "  old claim %s is kept until %s\n", m.OldClaim, m.RetainUntil.UTC().Format(time.RFC3339))
			}
		}
	}
	if !listed {
		fmt.Fprintln(out, "No home is scheduled or moving.")
	}
}
