package usercmd

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/cli/kubeclient"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// cliActor is who the audit log names for changes made by `javo user remove`.
var cliActor = model.Actor{System: model.SystemCLI}

func newUserRemoveCmd(newClient kubeclient.Factory, openStore dbflag.Opener) *cobra.Command {
	var namespace string
	var deleteHome, deleteWork bool
	cmd := &cobra.Command{
		Use:   "remove <identity name | username | email>",
		Short: "Remove a person (local account or single sign-on): end their sessions, revoke their API tokens, delete their secrets and forget their home",
		Long: "Finds the person by identity name (such as alice-1a2b3c4d), by local account name, or by the " +
			"username or email they sign in with (ignoring case). When more than one person matches, nothing is " +
			"removed and the command lists them; rerun with the identity name of the one to remove.\n\n" +
			"A local account is deleted, so it cannot sign in again. Someone who signs in through the identity " +
			"provider can sign in again, as the same person, until the provider stops them.\n\n" +
			"While the person has apps or jobs nothing is removed, unless --delete-apps-and-jobs deletes them too.\n\n" +
			"The person's home folder is forgotten: it is no longer listed or backed up. Its files stay on " +
			"their claim, and the same person coming back gets them back, unless --delete-home deletes them too.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			r := remover{db: db, newClient: newClient, deleteWork: deleteWork, deleteHome: deleteHome}
			summary, err := r.remove(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), summary)
			return nil
		},
	}
	cmd.Flags().StringVar(&namespace, "namespace", "", "")
	_ = cmd.Flags().MarkDeprecated("namespace", "API tokens live in the database; the flag is ignored")
	cmd.Flags().BoolVar(&deleteWork, "delete-apps-and-jobs", false, "also delete the person's apps (with their previews) and jobs; without it the command refuses while they have any")
	cmd.Flags().BoolVar(&deleteHome, "delete-home", false, "also delete the home folder's files (its claims); they cannot be brought back except from a backup")
	return cmd
}

// removal is the person a `javo user remove` argument names.
type removal struct {
	// name is the identity name: the owner of their sessions, tokens,
	// secrets, apps, jobs and home.
	name string
	// account is their local account's username; empty for single sign-on.
	account string
	// label describes them in messages.
	label string
	// issuer is the identity provider of someone who signs in through one.
	issuer string
}

type remover struct {
	db                     *store.Store
	newClient              kubeclient.Factory
	deleteWork, deleteHome bool
}

// find resolves who to one person. An identity name is unique and wins;
// otherwise a local account name, usernames and emails are gathered, and more
// than one person among them is refused.
func (r remover) find(ctx context.Context, who string) (removal, error) {
	people := r.db.People(store.IDRange{})
	if p, err := people.ByName(ctx, who); err == nil {
		return removalOf(p), nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return removal{}, err
	}
	found := map[string]removal{}
	if _, err := r.db.LocalAccounts().Get(ctx, who); err == nil {
		found[local.UserID(who)] = removal{name: local.UserID(who), account: who, label: who + " (local)"}
	} else if !errors.Is(err, store.ErrNotFound) {
		return removal{}, err
	}
	matches, err := people.ByLogin(ctx, who)
	if err != nil {
		return removal{}, err
	}
	for _, p := range matches {
		found[p.Name] = removalOf(p)
	}
	switch len(found) {
	case 0:
		return removal{}, fmt.Errorf("no person or local account matches %q (give an identity name, a local account name, or the username or email a person signs in with)", who)
	case 1:
		for _, m := range found {
			return m, nil
		}
	}
	lines := make([]string, 0, len(found))
	for _, m := range found {
		lines = append(lines, "  "+m.name+"  "+m.label)
	}
	sort.Strings(lines)
	return removal{}, fmt.Errorf("%q matches %d people, so nothing was removed; rerun with the identity name of the one to remove:\n%s",
		who, len(found), strings.Join(lines, "\n"))
}

func removalOf(p model.Person) removal {
	m := removal{name: p.Name, label: p.Label()}
	if p.Email != "" {
		m.label += " <" + p.Email + ">"
	}
	if p.Local() {
		m.account = p.Subjects[0].Subject
	} else if len(p.Subjects) > 0 {
		m.issuer = p.Subjects[0].Issuer
	}
	return m
}

// remove removes the person who names and returns what was done. A step that
// fails after the first change says so; rerunning finishes the rest.
func (r remover) remove(ctx context.Context, who string) (string, error) {
	m, err := r.find(ctx, who)
	if err != nil {
		return "", err
	}
	apps, jobs, err := r.work(ctx, m.name)
	if err != nil {
		return "", err
	}
	if !r.deleteWork && len(apps)+len(jobs) > 0 {
		return "", fmt.Errorf("%s %s still has %s; rerun with --delete-apps-and-jobs to delete them too (nothing was removed)",
			m.name, m.label, describeWork(apps, jobs))
	}
	failed := func(step string, err error) error {
		return fmt.Errorf("%s: %s failed, after earlier steps were done (rerun to finish): %w", m.name, step, err)
	}
	// The local account goes first, so no new session can start with it.
	if m.account != "" {
		users := &local.DBUsers{Accounts: r.db.LocalAccounts()}
		if err := users.Remove(ctx, m.account); err != nil && !errors.Is(err, local.ErrUserNotFound) {
			return "", failed("deleting the local account", err)
		}
	}
	if _, err := (&session.DBStore{Sessions: r.db.Sessions()}).DeleteFor(ctx, m.name); err != nil {
		return "", failed("ending their sessions", err)
	}
	if _, err := (&token.DBStore{Tokens: r.db.Tokens()}).DeleteFor(ctx, cliActor, m.name); err != nil {
		return "", failed("revoking their API tokens", err)
	}
	// The controller removes each app's and job's objects, then its row.
	for _, a := range apps {
		if err := r.db.Apps().MarkDeleted(ctx, cliActor, a.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", failed("deleting app "+a.Name, err)
		}
	}
	for _, j := range jobs {
		if err := r.db.Jobs().MarkDeleted(ctx, cliActor, j.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", failed("deleting job "+j.Name, err)
		}
	}
	if _, err := r.db.Secrets().DeleteFor(ctx, cliActor, m.name); err != nil {
		return "", failed("deleting their secrets", err)
	}
	homes, err := r.db.Homes().ListByOwner(ctx, m.name)
	if err != nil {
		return "", failed("reading their home", err)
	}
	if r.deleteHome && len(homes) > 0 {
		if err := deleteHomeClaims(ctx, r.newClient, homes); err != nil {
			return "", failed("deleting their home's files (rerun with --delete-home)", err)
		}
	}
	if _, err := r.db.Homes().DeleteFor(ctx, cliActor, m.name); err != nil {
		return "", failed("forgetting their home", err)
	}

	done := []string{"signed out", "API tokens revoked", "secrets deleted"}
	if m.account != "" {
		done = append([]string{"local account deleted"}, done...)
	}
	if len(apps)+len(jobs) > 0 {
		done = append(done, describeWork(apps, jobs)+" deleted")
	}
	if r.deleteHome {
		done = append(done, "home and its files deleted")
	} else {
		done = append(done, "home forgotten (its files are kept)")
	}
	out := fmt.Sprintf("removed %s %s: %s", m.name, m.label, strings.Join(done, ", "))
	if m.issuer != "" {
		out += fmt.Sprintf("\nthey sign in through %s and can sign in again until it stops them", m.issuer)
	}
	return out, nil
}

// work returns the person's live apps (previews included) and jobs.
func (r remover) work(ctx context.Context, owner string) ([]model.App, []model.Job, error) {
	apps, err := r.db.Apps().List(ctx, owner, true)
	if err != nil {
		return nil, nil, err
	}
	jobs, err := r.db.Jobs().List(ctx, owner)
	if err != nil {
		return nil, nil, err
	}
	return apps, jobs, nil
}

func describeWork(apps []model.App, jobs []model.Job) string {
	var names []string
	for _, a := range apps {
		names = append(names, "app "+a.Name)
	}
	for _, j := range jobs {
		names = append(names, "job "+j.Name)
	}
	return strings.Join(names, ", ")
}

// deleteHomeClaims deletes the claims of homes: every claim labelled with
// their owner in their namespace (a home moved between modes has more than
// one).
func deleteHomeClaims(ctx context.Context, newClient kubeclient.Factory, homes []model.Home) error {
	c, err := kubeclient.New(newClient)
	if err != nil {
		return err
	}
	for _, h := range homes {
		// The home's finished storage Jobs (backups, restores) keep their
		// pods for their logs; a pod that names the claim keeps it from
		// going, so they go first.
		var jobs batchv1.JobList
		if err := c.List(ctx, &jobs, client.InNamespace(h.Namespace), client.MatchingLabels{model.LabelOwner: h.Spec.Owner}); err != nil {
			return err
		}
		for i := range jobs.Items {
			if err := c.Delete(ctx, &jobs.Items[i], client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
		// List and delete each: the controller's account may delete claims,
		// not delete a collection of them.
		var claims corev1.PersistentVolumeClaimList
		if err := c.List(ctx, &claims, client.InNamespace(h.Namespace), client.MatchingLabels{model.LabelOwner: h.Spec.Owner}); err != nil {
			return err
		}
		for i := range claims.Items {
			if err := c.Delete(ctx, &claims.Items[i]); client.IgnoreNotFound(err) != nil {
				return err
			}
		}
	}
	return nil
}
