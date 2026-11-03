// Package storagecmd is `javo storage`: check, test and change the cluster's
// storage, and the steps its helper Jobs run.
package storagecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/storage/prepare"
	"github.com/nebari-dev/javo/internal/storage/probestep"
	storagereport "github.com/nebari-dev/javo/internal/storage/report"
	"github.com/nebari-dev/javo/internal/storage/storageadmin"
	"github.com/nebari-dev/javo/internal/store"
)

// verifyPoll is how often `storage verify` reads the probe's status; verifyGrace
// is how long past the probe's own timeout it keeps waiting for the result.
// Tests shorten both.
var (
	verifyPoll  = time.Second
	verifyGrace = 30 * time.Second
)

// storageActor is who the audit log names for changes made with `javo storage`.
var storageActor = model.Actor{System: model.SystemCLI}

// New checks and tests the cluster's storage. The admin's requests
// and the findings live in the database, so the commands that read or change
// them take the same database flags as the server (run them in the server
// pod: kubectl exec deploy/javo -- javo storage report). openStore nil means
// open the database the flags name. probe-step and prepare are what helper
// Jobs run and need no database.
func New(openStore dbflag.Opener) *cobra.Command {
	storage := &cobra.Command{Use: "storage", Short: "Check and test cluster storage"}
	openStore = dbflag.OrRegister(openStore, storage.PersistentFlags())
	storage.AddCommand(newProbeStepCmd())
	storage.AddCommand(newPrepareCmd())
	storage.AddCommand(newCopyCmd())
	storage.AddCommand(newVerifyCmd(openStore))
	storage.AddCommand(newMigrateCmd(openStore))
	storage.AddCommand(newReportCmd(openStore))
	storage.AddCommand(newFolderCmd(openStore))
	storage.AddCommand(newHomeSizeCmd(openStore))
	return storage
}

// readClusterStorage is the storage view as the controller and the admin API
// see it. Only the admin's requests and the findings are read; neither the
// report nor the move commands need the config file.
func readClusterStorage(ctx context.Context, db *store.Store) (*model.ClusterStorage, error) {
	var spec model.ClusterStorageSpec
	spec.ApplyDefaults()
	cs, err := db.Storage().Cluster(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("read the cluster's storage: %w", err)
	}
	return &cs, nil
}

// newReportCmd prints what the cluster's storage is, from the same rows the
// admin API returns.
func newReportCmd(openStore dbflag.Opener) *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Say what storage the cluster offers and what is in use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				cs, err := readClusterStorage(cmd.Context(), db)
				if err != nil {
					return err
				}
				homes, err := db.Homes().List(cmd.Context())
				if err != nil {
					return fmt.Errorf("list homes: %w", err)
				}
				return storagereport.Text(cmd.OutOrStdout(), storagereport.Build(cs, homes))
			})
		},
	}
}

// newProbeStepCmd is what the probe Jobs run inside the cluster.
func newProbeStepCmd() *cobra.Command {
	var step, dir string
	cmd := &cobra.Command{
		Use:   "probe-step",
		Short: "Run one step of the storage probe against a volume (used by the probe Jobs)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return probestep.Run(dir, step, os.Getuid(), os.Getgid())
		},
	}
	cmd.Flags().StringVar(&step, "step", "", "write, check or confirm")
	cmd.Flags().StringVar(&dir, "dir", "", "directory the volume is mounted at")
	_ = cmd.MarkFlagRequired("step")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
}

// newPrepareCmd is what a home's prepare Job runs inside the cluster.
func newPrepareCmd() *cobra.Command {
	var (
		dir, mode string
		uid, gid  int
		noMkdir   bool
	)
	cmd := &cobra.Command{
		Use:   "prepare",
		Short: "Ready a new home volume: set the root's mode and check the user can write it (used by the prepare Jobs)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			m, err := prepare.ParseMode(mode)
			if err != nil {
				return err
			}
			return prepare.Run(dir, uid, gid, m, !noMkdir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory the volume is mounted at")
	cmd.Flags().IntVar(&uid, "uid", os.Getuid(), "user who must be able to write the volume root")
	cmd.Flags().IntVar(&gid, "gid", 0, "group of that user")
	cmd.Flags().StringVar(&mode, "mode", "0775", "octal mode of the volume root, 02775 for setgid")
	cmd.Flags().BoolVar(&noMkdir, "no-mkdir", false, "fail when the directory does not exist instead of creating it")
	_ = cmd.MarkFlagRequired("dir")
	_ = cmd.MarkFlagRequired("gid")
	return cmd
}

func newVerifyCmd(openStore dbflag.Opener) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "verify <storage-class>",
		Short: "Run the two-node probe on a StorageClass and print each step as it happens",
		Long: "Asks the controller to probe the class: a shared claim, then a write, a check on " +
			"another node and a confirm back on the first. The probe costs a small volume for a " +
			"few minutes, so it only runs when you ask. Exits non-zero when the probe fails.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				return verify(cmd.Context(), db, cmd.OutOrStdout(), args[0], timeout)
			})
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "how long the probe may take")
	return cmd
}

func verify(ctx context.Context, db *store.Store, out io.Writer, class string, timeout time.Duration) error {
	nonce, err := storageadmin.RequestProbe(ctx, db.Storage(), storageActor, class, timeout)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "probe started on StorageClass %s\n", class)

	ctx, cancel := context.WithTimeout(ctx, timeout+verifyGrace)
	defer cancel()
	var seen string
	for {
		st, err := db.Storage().Status(ctx)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("read probe status: %w", err)
		}
		if p := st.Probe; err == nil && p != nil && p.Nonce == nonce {
			if p.Result != model.ProbeRunning {
				return report(out, p)
			}
			if line := "step: " + p.Step; p.Step != "" && line != seen {
				fmt.Fprintln(out, line)
				seen = line
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no probe result after %s: is the controller running?", timeout+verifyGrace)
		case <-time.After(verifyPoll):
		}
	}
}

// report prints the final result and returns an error when the probe failed.
func report(out io.Writer, p *model.ProbeStatus) error {
	if p.Result == model.ProbePassed {
		fmt.Fprintf(out, "result: Passed (%d nodes tested)\n", p.NodesTested)
		return nil
	}
	fmt.Fprintf(out, "result: Failed at %s\n", p.FailedStep)
	for _, e := range p.Evidence {
		fmt.Fprintf(out, "  %s\n", e)
	}
	return fmt.Errorf("probe failed at %s", p.FailedStep)
}

// newFolderCmd declares the folders groups share. A folder is mounted at
// /shared/<dir> in the pods of the group's members once the controller has
// made and readied its claim.
func newFolderCmd(openStore dbflag.Opener) *cobra.Command {
	folder := &cobra.Command{Use: "folder", Short: "Declare, list and remove the folders groups share"}
	var namespace, name, size, class string
	add := &cobra.Command{
		Use:   "add <group>",
		Short: "Give a group a shared folder (the group as the identity provider names it, such as /org/data-science)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := model.SharedFolder{Namespace: namespace, Name: name, Spec: model.SharedFolderSpec{Group: args[0], StorageClassName: class}}
			if f.Name == "" {
				f.Name = naming.GroupDir(args[0])
			}
			if size != "" {
				q, err := resource.ParseQuantity(size)
				if err != nil {
					return fmt.Errorf("--size: %w", err)
				}
				f.Spec.Size = &q
			}
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				got, err := db.SharedFolders().Create(cmd.Context(), storageActor, f)
				if errors.Is(err, store.ErrExists) {
					return fmt.Errorf("a folder named %q already exists in %s", f.Name, f.Namespace)
				} else if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "folder %s for group %s added; members see it at /shared/%s once it is ready\n",
					got.Name, got.Spec.Group, naming.GroupDir(got.Spec.Group))
				return nil
			})
		},
	}
	add.Flags().StringVar(&name, "name", "", "the folder's name, which names its claim (default: the group's directory)")
	add.Flags().StringVar(&size, "size", "", "capacity, such as 50Gi (default: storage.sharedFolders.defaultSize)")
	add.Flags().StringVar(&class, "storage-class", "", "StorageClass of the claim (default: chosen by the platform)")

	list := &cobra.Command{
		Use:   "list",
		Short: "List the shared folders and whether each is ready",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				folders, err := db.SharedFolders().List(cmd.Context())
				if err != nil {
					return err
				}
				if len(folders) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No shared folders.")
				}
				for _, f := range folders {
					printFolder(cmd.OutOrStdout(), f)
				}
				return nil
			})
		},
	}

	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Stop offering a shared folder; its claim and files stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				f, err := db.SharedFolders().ByName(cmd.Context(), namespace, args[0])
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("no folder named %q in %s", args[0], namespace)
				} else if err != nil {
					return err
				}
				if err := db.SharedFolders().Delete(cmd.Context(), storageActor, f.ID); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "folder %s removed; claim %s and its files are kept\n", f.Name, naming.FolderClaimName(f.Name))
				return nil
			})
		},
	}
	folder.PersistentFlags().StringVar(&namespace, "namespace", model.DefaultWorkspaceNamespace, "namespace the folder's claim is in (the apps namespace)")
	folder.AddCommand(add, list, remove)
	return folder
}

// printFolder is one line per folder: name, group, mount, and readiness with
// the reason from the folder's Ready condition.
func printFolder(out io.Writer, f model.SharedFolder) {
	state := "not looked at yet"
	for _, c := range f.Status.Conditions {
		if c.Type != model.ConditionReady {
			continue
		}
		state = "ready"
		if c.Status != "True" {
			state = c.Reason + ": " + c.Message
		}
	}
	fmt.Fprintf(out, "%s\tgroup %s\t/shared/%s\t%s\n", f.Name, f.Spec.Group, naming.GroupDir(f.Spec.Group), state)
}

// newHomeSizeCmd changes the size a person's home asks for. The controller
// grows the claim where its StorageClass allows it, and never shrinks one.
func newHomeSizeCmd(openStore dbflag.Opener) *cobra.Command {
	var namespace string
	cmd := &cobra.Command{
		Use:   "home-size <person> <size>",
		Short: "Set the size of a person's home, such as 20Gi (grows the claim where the storage allows it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := resource.ParseQuantity(args[1])
			if err != nil {
				return fmt.Errorf("size: %w", err)
			}
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				h, err := db.Homes().ByOwner(cmd.Context(), namespace, args[0])
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("%s has no home in %s yet", args[0], namespace)
				} else if err != nil {
					return err
				}
				if _, err := db.Homes().SetSize(cmd.Context(), storageActor, h.ID, size); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "home of %s asks for %s\n", args[0], size.String())
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", model.DefaultWorkspaceNamespace, "namespace the home is in")
	return cmd
}
