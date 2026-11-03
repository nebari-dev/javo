// Package dbcmd is `javo db`: migrate, back up and restore the database.
package dbcmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/store"
)

// New is `javo db`: the database commands. openStore nil means open the
// database the flags name.
func New(openStore dbflag.Opener) *cobra.Command {
	db := &cobra.Command{Use: "db", Short: "Migrate, back up and restore the database"}
	var flags dbflag.Flags
	flags.Register(db.PersistentFlags())
	if openStore == nil {
		openStore = flags.Open
	}
	db.AddCommand(&cobra.Command{
		Use:   "upgrade",
		Short: "Migrate the schema",
		Long:  "Brings the database schema up to date. The chart runs it in an init container before the server starts.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer s.Close()
			fmt.Fprintln(cmd.OutOrStdout(), "database schema is current")
			return nil
		},
	})
	db.AddCommand(&cobra.Command{
		Use:   "backup <file>",
		Short: "Write a consistent copy of the SQLite database to a new file",
		Long: "Copies the live SQLite database to <file>, which must not exist. It is safe while the server runs. " +
			"With Postgres the chart's backup CronJob runs pg_dump instead (see the Backups page of the admin guide).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := flags.Config()
			if err != nil {
				return err
			}
			// Open read-only and without migrating: a backup must not change the
			// database it copies, and a mistyped path must fail instead of
			// creating an empty database.
			cfg.ReadOnly = true
			s, err := store.Open(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer s.Close()
			if err := s.Backup(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "backup written to %s\n", args[0])
			return nil
		},
	})
	db.AddCommand(&cobra.Command{
		Use:   "restore <file>",
		Short: "Replace the SQLite database with a backup",
		Long: "Replaces the SQLite database with <file>. Scale the server to 0 first: it refuses while any process has " +
			"the database open. The database it replaces is kept as <database>.before-restore. Every restored app waits, " +
			"held under the backup's file name, until a platform admin releases the restored apps under Settings, Backups " +
			"(or POST /api/admin/restores/{name}/release), so they do not all start at once. For Postgres use pg_restore " +
			"(see the Backups page of the admin guide).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.Engine != store.EngineSQLite {
				return errors.New("restore is for SQLite; restore a Postgres backup with pg_restore (see the Backups page of the admin guide)")
			}
			if err := store.RestoreSQLite(cmd.Context(), flags.Path, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restored %s from %s\n", flags.Path, args[0])
			return nil
		},
	})
	return db
}
