// Package secretscmd is `javo secrets`: the keys of people's secrets.
package secretscmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/secrets"
)

// New looks after the keys of people's secrets. Like `javo user`,
// it reads the database flags of the server and runs in the controller pod,
// whose service account may read the key-encryption key's Secret:
//
//	kubectl exec deploy/javo -- /javo secrets rotate --key-secret javo-secrets-key
func New(openStore dbflag.Opener) *cobra.Command {
	cmd := &cobra.Command{Use: "secrets", Short: "Manage the keys of people's secrets"}
	openStore = dbflag.OrRegister(openStore, cmd.PersistentFlags())
	var keyFile, keySecret, namespace string
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Add a new data key and encrypt every stored secret value again with it",
		Long: "Adds a key to the data keyset and makes it the one new values use, then encrypts every stored value " +
			"again under it. No value changes and no app restarts. Values encrypted before keep working while it " +
			"runs; running it again is safe. A key file that does not open the stored keyset changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kek, err := loadKEK(cmd.Context(), keyFile, keySecret, namespace)
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
	rotate.Flags().StringVar(&keyFile, "key-file", "", "the key-encryption key file")
	rotate.Flags().StringVar(&keySecret, "key-secret", "", "the Secret holding the key-encryption key under kek (the chart's is javo-secrets-key, or secrets.existingKeySecret)")
	rotate.Flags().StringVar(&namespace, "namespace", os.Getenv("POD_NAMESPACE"), "namespace of --key-secret (default $POD_NAMESPACE)")
	rotate.MarkFlagsOneRequired("key-file", "key-secret")
	rotate.MarkFlagsMutuallyExclusive("key-file", "key-secret")
	cmd.AddCommand(rotate)
	return cmd
}

// loadKEK reads the key-encryption key from keyFile, or else from the Secret
// keySecret in namespace (never made here: rotating needs the key that
// wrapped the stored keyset).
func loadKEK(ctx context.Context, keyFile, keySecret, namespace string) (secrets.KEK, error) {
	if keyFile != "" {
		return secrets.KEKFromFile(keyFile)
	}
	if namespace == "" {
		return nil, errors.New("--key-secret needs --namespace (or POD_NAMESPACE)")
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{})
	if err != nil {
		return nil, err
	}
	return secretskey.Secret{Client: c, Namespace: namespace, Name: keySecret}.Load(ctx)
}
