// Package secretscmd is `javo secrets`: the keys of people's secrets.
package secretscmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/secrets"
)

// New looks after the keys of people's secrets. Like `javo user`,
// it reads the database flags of the server and runs in the controller pod,
// whose service account may read the key-encryption key's Secret:
//
//	kubectl exec deploy/javo -- /javo secrets rotate --key-secret javo-secrets-key
func New(openStore dbflag.Opener, fromSecret KeyOfSecret) *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Manage the keys of people's secrets"}
	openStore = dbflag.OrRegister(openStore, cmd.PersistentFlags())
	var keyFile, keySecret, namespace string
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Add a new data key and encrypt every stored secret value again with it",
		Long: "Adds a key to the data keyset and makes it the one new values use, then encrypts every stored value " +
			"again under it: people's secrets and the bundles of values kept for workloads. It prints how many of " +
			"each. No value changes and no app restarts. Values encrypted before keep working while it " +
			"runs; running it again is safe. A key file that does not open the stored keyset changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kek, err := loadKEK(cmd.Context(), fromSecret, keyFile, keySecret, namespace)
			if err != nil {
				return err
			}
			db, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			n, bundles, err := (&secrets.Keys{Store: db, KEK: kek}).Rotate(cmd.Context())
			done := fmt.Sprintf("%s and %s", count(n, "secret"), count(bundles, "workload bundle"))
			if err != nil {
				return fmt.Errorf("rotate the secrets keys (%s done; rerun to finish): %w", done, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "new data key in use; %s encrypted again\n", done)
			return nil
		},
	}
	rotate.Flags().StringVar(&keyFile, "key-file", "", "the key-encryption key file")
	rotate.Flags().StringVar(&keySecret, "key-secret", "", "the Secret holding the key-encryption key under kek (the chart's is javo-secrets-key, or secrets.existingKeySecret)")
	rotate.Flags().StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"), "namespace of --key-secret (default $POD_NAMESPACE)")
	rotate.MarkFlagsOneRequired("key-file", "key-secret")
	rotate.MarkFlagsMutuallyExclusive("key-file", "key-secret")
	cmd.AddCommand(rotate)
	return cmd
}

// count is n things, in words: "1 secret", "2 secrets".
func count(n int, noun string) string {
	if n != 1 {
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}

// KeyOfSecret reads the key-encryption key from a Secret of a cluster.
type KeyOfSecret func(ctx context.Context, namespace, name string) (secrets.KEK, error)

// loadKEK reads the key-encryption key from keyFile, or else from the Secret
// keySecret in namespace through fromSecret.
func loadKEK(ctx context.Context, fromSecret KeyOfSecret, keyFile, keySecret, namespace string) (secrets.KEK, error) {
	if keyFile != "" {
		return secrets.KEKFromFile(keyFile)
	}
	if namespace == "" {
		return nil, errors.New("--key-secret needs --namespace (or POD_NAMESPACE)")
	}
	if fromSecret == nil {
		return nil, errors.New("--key-secret: no Kubernetes client to read the Secret with")
	}
	return fromSecret(ctx, namespace, keySecret)
}
