// Package usercmd is `javo user`: local accounts, and removing people.
package usercmd

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/kubeclient"
	"github.com/nebari-dev/javo/internal/store"
)

// New manages local accounts. Accounts and sessions live in the
// database, so the command reads the same database flags as the server (run it
// in the server pod: kubectl exec deploy/javo -- javo user add ...). openStore
// nil means open the database the flags name.
func New(newClient kubeclient.Factory, openStore dbflag.Opener) *cobra.Command {
	user := &cobra.Command{Use: "user", Short: "Manage local accounts and remove people"}
	openStore = dbflag.OrRegister(openStore, user.PersistentFlags())
	user.AddCommand(newUserAddCmd(openStore))
	user.AddCommand(newUserRemoveCmd(newClient, openStore))
	return user
}

func newUserAddCmd(openStore dbflag.Opener) *cobra.Command {
	var passwordFile, namespace string
	var groups []string
	var force, reusePerson bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a local account with a generated password",
		Long: "Creates a local account with a random 24-byte password. The argon2id hash goes into the database; " +
			"the password is written (mode 0600) to --password-file and never printed. " +
			"With --password-file - it goes to standard output alone, for a pod without a shell: " +
			"kubectl exec deploy/javo -- /javo user add alice --password-file - > alice.password\n\n" +
			"The username of a removed local account is refused: the new account would sign in as the removed " +
			"person, with their kept home files and the apps shared with them. Pass --reuse-person only when it " +
			"is the same person coming back.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			if err := addUser(cmd, db, args[0], passwordFile, groups, force, reusePerson); err != nil {
				return err
			}
			if passwordFile == stdoutFile {
				fmt.Fprintf(cmd.ErrOrStderr(), "user %q added; password written to standard output\n", args[0])
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "user %q added; password written to %s\n", args[0], passwordFile)
			return nil
		},
	}
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "file to write the generated password to (mode 0600); - for standard output")
	cmd.Flags().StringVar(&namespace, "namespace", "", "")
	_ = cmd.Flags().MarkDeprecated("namespace", "accounts live in the database; the flag is ignored")
	cmd.Flags().StringArrayVar(&groups, "group", nil, "group the account belongs to, as an identity provider would emit it (repeatable)")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing user and password file; ends their sessions and revokes their API tokens")
	cmd.Flags().BoolVar(&reusePerson, "reuse-person", false, "give the username of a removed local account to the same person coming back: they get its kept home files and the apps still shared with it")
	_ = cmd.MarkFlagRequired("password-file")
	return cmd
}

// stdoutFile as --password-file sends the password to standard output.
const stdoutFile = "-"

func addUser(cmd *cobra.Command, db *store.Store, name, passwordFile string, groups []string, force, reusePerson bool) error {
	if !local.ValidUsername(name) {
		return fmt.Errorf("invalid username %q: use letters, digits, '.', '_' or '-', up to 63 characters", name)
	}
	users := &local.DBUsers{Accounts: db.LocalAccounts()}
	// The person this account signs in as, when there has been one: a
	// local sign-in's subject is the username.
	person, err := db.People(store.IDRange{}).BySubject(cmd.Context(), local.Issuer, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return fmt.Errorf("look up the person of %q: %w", name, err)
	default:
		if _, err := users.Lookup(cmd.Context(), name); errors.Is(err, local.ErrUserNotFound) && !reusePerson {
			return fmt.Errorf("%q was the username of %s, whose account was removed; a new account would sign in as them, "+
				"with their kept home files and the apps shared with them. Pick another username, or pass --reuse-person "+
				"if this is the same person coming back", name, person.Name)
		}
	}
	personName := local.UserID(name)
	if person.Name != "" {
		personName = person.Name
	}
	toStdout := passwordFile == stdoutFile
	if !force && !toStdout {
		if _, err := os.Lstat(passwordFile); err == nil {
			return fmt.Errorf("%s already exists (use --force to replace it)", passwordFile)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate password: %w", err)
	}
	pw := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := local.HashPassword(pw)
	if err != nil {
		return err
	}

	// Stage the password file first so a failed write cannot leave an
	// account whose password was never saved; publish it only after the
	// account write succeeds so a refused add never touches the target file.
	var tmp string
	if !toStdout {
		if tmp, err = stagePasswordFile(passwordFile, pw); err != nil {
			return err
		}
	}
	if err := users.Add(cmd.Context(), name, hash, groups, force); err != nil {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
		if errors.Is(err, local.ErrUserExists) {
			return fmt.Errorf("user %q already exists (use --force to replace)", name)
		}
		return err
	}
	if toStdout {
		if _, err := io.WriteString(cmd.OutOrStdout(), pw); err != nil {
			return fmt.Errorf("account stored but password not written (rerun with --force): %w", err)
		}
	} else if err := publishPasswordFile(tmp, passwordFile, force); err != nil {
		return fmt.Errorf("account stored but password file not written (rerun with --force): %w", err)
	}
	if force {
		// A replaced password or group list must not leave the old sessions
		// signed in, nor API tokens made with them working.
		id := personName
		sessions := &session.DBStore{Sessions: db.Sessions()}
		n, err := sessions.DeleteFor(cmd.Context(), id)
		if err != nil {
			return fmt.Errorf("password replaced, but ending the old sessions failed (rerun with --force): %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "signed out %d sessions\n", n)
		revoked, err := (&token.DBStore{Tokens: db.Tokens()}).DeleteFor(cmd.Context(), cliActor, id)
		if err != nil {
			return fmt.Errorf("password replaced, but revoking the API tokens failed (rerun with --force): %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "revoked %d API tokens\n", revoked)
	}
	return nil
}

// publishPasswordFile moves the staged file to target and removes the staging
// path. Without force it uses a hard link, which fails if target exists, so a
// file created after the earlier existence check is never overwritten.
func publishPasswordFile(tmp, target string, force bool) error {
	defer func() { _ = os.Remove(tmp) }()
	if force {
		return os.Rename(tmp, target)
	}
	return os.Link(tmp, target)
}

// stagePasswordFile writes pw to a new 0600 file next to target and returns its path.
func stagePasswordFile(target, pw string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), ".javo-pw-*") // created with mode 0600
	if err != nil {
		return "", fmt.Errorf("create password file: %w", err)
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("write password file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.WriteString(pw); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("write password file: %w", err)
	}
	return name, nil
}
