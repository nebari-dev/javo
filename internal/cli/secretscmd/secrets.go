// Package secretscmd is `javo secrets`: the keys of people's secrets.
package secretscmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/secrets"
)

// New looks after the keys of people's secrets. Like `javo user`,
// it reads the database flags of the server and runs in the controller pod,
// where the key-encryption key is mounted:
//
//	kubectl exec deploy/javo -- /javo secrets rotate --key-file /etc/javo/secrets-key/kek
func New(openStore dbflag.Opener) *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Manage the keys of people's secrets"}
	openStore = dbflag.OrRegister(openStore, cmd.PersistentFlags())
	var keyFile string
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Add a new data key and encrypt every stored secret value again with it",
		Long: "Adds a key to the data keyset and makes it the one new values use, then encrypts every stored value " +
			"again under it. No value changes and no app restarts. Values encrypted before keep working while it " +
			"runs; running it again is safe. A key file that does not open the stored keyset changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kek, err := secrets.KEKFromFile(keyFile)
			if err != nil {
				return err
			}
			db, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			n, err := (&secrets.Keys{Store: db, KEK: kek}).Rotate(cmd.Context())
			if err != nil {
				return fmt.Errorf("rotate the secrets keys (%d values done; rerun to finish): %w", n, err)
			}
			noun := "secrets"
			if n == 1 {
				noun = "secret"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "new data key in use; %d %s encrypted again\n", n, noun)
			return nil
		},
	}
	rotate.Flags().StringVar(&keyFile, "key-file", "", "the key-encryption key file (the chart mounts it in the controller)")
	_ = rotate.MarkFlagRequired("key-file")
	cmd.AddCommand(rotate)
	return cmd
}
