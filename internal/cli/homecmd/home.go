// Package homecmd is `javo home`: home folders whose owners are gone.
package homecmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/homeclaims"
	"github.com/nebari-dev/javo/internal/cli/wiring"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// New is `javo home`: home folders whose owners are gone. Run it in
// the server pod, which has the database and may read the claims.
func New(newClaims homeclaims.Factory, openStore dbflag.Opener) *cobra.Command {
	home := &cobra.Command{Use: "home", Short: "Home folders whose owners are gone"}
	openStore = dbflag.OrRegister(openStore, home.PersistentFlags())
	cluster := wiring.ClusterFlag(home.PersistentFlags())
	var orphans, yes bool
	prune := &cobra.Command{
		Use:   "prune --orphans [--yes]",
		Short: "Forget the homes whose owner has no account and whose files are gone",
		Long: "A home is an orphan when its owner was a local account that no longer exists and its claim " +
			"is gone: it holds nothing, yet it is still listed and asked to be backed up. Without --yes this " +
			"only lists them. Homes of people who sign in through an identity provider, and homes with files, " +
			"are never touched.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !orphans {
				return errors.New("say what to prune: --orphans")
			}
			// An orphan is told by its claim, which only a cluster has.
			if executor, onCluster, err := cluster(); err != nil {
				return err
			} else if !onCluster {
				return fmt.Errorf("javo home prune needs executor kubernetes: with executor %s a home is a Docker volume or a folder, which this command cannot see (nothing was changed)", executor)
			}
			c, err := homeclaims.Open(newClaims)
			if err != nil {
				return err
			}
			return dbflag.With(cmd.Context(), openStore, func(db *store.Store) error {
				found, err := orphanHomes(cmd.Context(), db, c)
				if err != nil {
					return err
				}
				out := cmd.OutOrStdout()
				if len(found) == 0 {
					fmt.Fprintln(out, "No orphan homes.")
					return nil
				}
				for _, h := range found {
					fmt.Fprintf(out, "%s (%s)\n", h.Spec.Owner, h.Namespace)
				}
				if !yes {
					fmt.Fprintf(out, "%d orphan homes. Nothing was changed; run again with --yes to forget them.\n", len(found))
					return nil
				}
				for _, h := range found {
					if err := db.Homes().Delete(cmd.Context(), cliActor, h.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
						return err
					}
				}
				fmt.Fprintf(out, "%d orphan homes forgotten.\n", len(found))
				return nil
			})
		},
	}
	prune.Flags().BoolVar(&orphans, "orphans", false, "the homes whose owner has no account and no claim")
	prune.Flags().BoolVar(&yes, "yes", false, "forget them; without it they are only listed")
	home.AddCommand(prune)
	return home
}

// cliActor is who the audit log names for homes `javo home prune` forgets.
var cliActor = model.Actor{System: model.SystemCLI}

// orphanHomes are the homes whose owner is a local account that is gone (no
// account, and no person or a local one) and that have no claim left.
func orphanHomes(ctx context.Context, db *store.Store, c homeclaims.Claims) ([]model.Home, error) {
	accounts, err := db.LocalAccounts().List(ctx)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, a := range accounts {
		live[local.UserID(a.Username)] = true
	}
	homes, err := db.Homes().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Home
	for _, h := range homes {
		if live[h.Spec.Owner] {
			continue
		}
		switch p, err := db.People(store.IDRange{}).ByName(ctx, h.Spec.Owner); {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return nil, err
		case len(p.Subjects) == 0 || p.Subjects[0].Issuer != local.Issuer:
			continue // signs in through an identity provider
		}
		left, err := c.Any(ctx, h.Namespace, h.Spec.Owner)
		if err != nil {
			return nil, fmt.Errorf("list the claims of %s: %w", h.Spec.Owner, err)
		}
		if !left {
			out = append(out, h)
		}
	}
	return out, nil
}
