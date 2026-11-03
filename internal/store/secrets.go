package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Secrets is the secrets, app_secrets and job_secrets tables.
// The store never sees a value: callers hand it the value sealed for the row
// (package secrets), and only the controller asks for sealed values back. No
// audit row, change or error carries a value or a ciphertext.
type Secrets struct{ s *Store }

// Secrets returns the secrets tables.
func (s *Store) Secrets() *Secrets { return &Secrets{s} }

// SecretApp is a live app that uses a secret.
type SecretApp struct {
	ID, Name, DisplayName string
}

// SecretJob is a live job whose runs get a secret.
type SecretJob struct {
	ID, Name, DisplayName string
}

// SecretInfo is a secret and the live apps and jobs that use it.
type SecretInfo struct {
	model.Secret
	Apps []SecretApp
	Jobs []SecretJob
}

// SealedBinding is one secret an app or a job's runs get, with its sealed value.
type SealedBinding struct {
	Env        string
	Secret     model.Secret
	Ciphertext []byte
}

// SealedSecret is a stored ciphertext and the row it was sealed for.
type SealedSecret struct {
	ID, Owner, Name string
	Ciphertext      []byte
}

// SecretMissingError is a binding that names no secret of the app's or job's owner.
type SecretMissingError struct{ Name string }

func (e *SecretMissingError) Error() string { return fmt.Sprintf("store: no secret %q", e.Name) }

// SecretInUseError refuses to delete a secret live apps or jobs use.
type SecretInUseError struct {
	Apps []SecretApp
	Jobs []SecretJob
}

func (e *SecretInUseError) Error() string {
	var names []string
	for _, a := range e.Apps {
		names = append(names, a.Name)
	}
	for _, j := range e.Jobs {
		names = append(names, j.Name)
	}
	return "store: secret is used by " + strings.Join(names, ", ")
}

// ErrTooManySecrets refuses a secret past model.MaxSecretsPerOwner.
var ErrTooManySecrets = errors.New("store: too many secrets")

// sealFunc seals the value for the row with this id.
type sealFunc func(id string) ([]byte, error)

var secretKind = "secret"

func secretAudit(action string, sec model.Secret, detail any) *auditRow {
	return &auditRow{action: action, kind: secretKind, name: sec.Name, id: sec.ID, owner: sec.Owner, detail: detail}
}

func encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func secretOf(r sqlcgen.Secret) model.Secret {
	return model.Secret{ID: r.ID, Owner: r.Owner, Name: r.Name, Note: r.Note, Version: r.Version,
		CreatedAt: r.CreatedAt.Time(), UpdatedAt: r.UpdatedAt.Time()}
}

func readSecret(ctx context.Context, q *sqlcgen.Queries, owner, name string) (sqlcgen.Secret, error) {
	row, err := q.GetSecretByOwnerName(ctx, sqlcgen.GetSecretByOwnerNameParams{Owner: owner, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// Create stores a new secret for owner. seal gets the new row's id and
// returns the value sealed for it. A taken name is ErrExists. It is audited
// (name and note only).
func (x *Secrets) Create(ctx context.Context, actor model.Actor, owner, name, note string, seal sealFunc) (model.Secret, error) {
	id := newID()
	sealed, err := seal(id)
	if err != nil {
		return model.Secret{}, err
	}
	var out model.Secret
	err = x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		if n, err := q.CountSecretsForOwner(ctx, owner); err != nil {
			return effect{}, err
		} else if n >= model.MaxSecretsPerOwner {
			return effect{}, ErrTooManySecrets
		}
		n, err := q.InsertSecret(ctx, sqlcgen.InsertSecretParams{
			ID: id, Owner: owner, Name: name, Note: note, Ciphertext: encode(sealed), CreatedAt: dbtime.From(now()),
		})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrExists
		}
		row, err := q.GetSecret(ctx, id)
		if err != nil {
			return effect{}, err
		}
		out = secretOf(row)
		return effect{audit: secretAudit("secret.create", out, nil)}, nil
	})
	return out, err
}

// Get returns owner's secret called name, or ErrNotFound.
func (x *Secrets) Get(ctx context.Context, owner, name string) (model.Secret, error) {
	row, err := readSecret(ctx, x.s.q, owner, name)
	if err != nil {
		return model.Secret{}, err
	}
	return secretOf(row), nil
}

// List returns owner's secrets by name, each with the live apps and jobs
// that use it.
func (x *Secrets) List(ctx context.Context, owner string) ([]SecretInfo, error) {
	rows, err := x.s.q.ListSecretsForOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	uses, err := x.s.q.ListSecretUsesForOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	apps := map[string][]SecretApp{}
	for _, u := range uses {
		var spec appSpec
		if err := unmarshal(u.AppSpec, &spec); err != nil {
			return nil, err
		}
		list := apps[u.SecretID]
		// An app that gets one secret twice is listed once.
		if len(list) > 0 && list[len(list)-1].ID == u.AppID {
			continue
		}
		apps[u.SecretID] = append(list, SecretApp{ID: u.AppID, Name: u.AppName, DisplayName: spec.DisplayName})
	}
	jobUses, err := x.s.q.ListSecretJobUsesForOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	jobs := map[string][]SecretJob{}
	for _, u := range jobUses {
		var spec model.JobSpec
		if err := unmarshal(u.JobSpec, &spec); err != nil {
			return nil, err
		}
		list := jobs[u.SecretID]
		// A job that gets one secret twice is listed once.
		if len(list) > 0 && list[len(list)-1].ID == u.JobID {
			continue
		}
		jobs[u.SecretID] = append(list, SecretJob{ID: u.JobID, Name: u.JobName, DisplayName: spec.DisplayName})
	}
	out := make([]SecretInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, SecretInfo{
			Secret: model.Secret{ID: r.ID, Owner: r.Owner, Name: r.Name, Note: r.Note, Version: r.Version,
				CreatedAt: r.CreatedAt.Time(), UpdatedAt: r.UpdatedAt.Time()},
			Apps: apps[r.ID],
			Jobs: jobs[r.ID],
		})
	}
	return out, nil
}

// SetValue replaces the value of owner's secret called name with the one
// seal returns for its id. The version moves and every live app and job that
// uses it is announced: apps restart with the new value, and a job's next run
// gets it. It is audited.
func (x *Secrets) SetValue(ctx context.Context, actor model.Actor, owner, name string, seal sealFunc) (model.Secret, error) {
	var out model.Secret
	err := x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := readSecret(ctx, q, owner, name)
		if err != nil {
			return effect{}, err
		}
		sealed, err := seal(row.ID)
		if err != nil {
			return effect{}, err
		}
		if _, err := q.SetSecretValue(ctx, sqlcgen.SetSecretValueParams{Ciphertext: encode(sealed), UpdatedAt: dbtime.From(now()), ID: row.ID}); err != nil {
			return effect{}, err
		}
		if row, err = q.GetSecret(ctx, row.ID); err != nil {
			return effect{}, err
		}
		out = secretOf(row)
		changes, err := userChanges(ctx, q, row.ID)
		return effect{audit: secretAudit("secret.set_value", out, nil), changes: changes}, err
	})
	return out, err
}

// SetNote replaces the note of owner's secret called name. It is audited.
func (x *Secrets) SetNote(ctx context.Context, actor model.Actor, owner, name, note string) (model.Secret, error) {
	var out model.Secret
	err := x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := readSecret(ctx, q, owner, name)
		if err != nil {
			return effect{}, err
		}
		if _, err := q.SetSecretNote(ctx, sqlcgen.SetSecretNoteParams{Note: note, UpdatedAt: dbtime.From(now()), ID: row.ID}); err != nil {
			return effect{}, err
		}
		if row, err = q.GetSecret(ctx, row.ID); err != nil {
			return effect{}, err
		}
		out = secretOf(row)
		return effect{audit: secretAudit("secret.set_note", out, nil)}, nil
	})
	return out, err
}

// userChanges announces the live apps and jobs that use the secret.
func userChanges(ctx context.Context, q *sqlcgen.Queries, secretID string) ([]Change, error) {
	apps, err := q.ListAppsUsingSecret(ctx, secretID)
	if err != nil {
		return nil, err
	}
	jobs, err := q.ListJobsUsingSecret(ctx, secretID)
	if err != nil {
		return nil, err
	}
	out := make([]Change, 0, len(apps)+len(jobs))
	for _, id := range apps {
		out = append(out, Change{Table: appKind.table, ID: id})
	}
	for _, id := range jobs {
		out = append(out, Change{Table: jobKind.table, ID: id})
	}
	return out, nil
}

// Delete removes owner's secret called name. One a live app or job uses is
// refused with a *SecretInUseError; bindings of apps and jobs being deleted
// go with it. It is audited.
func (x *Secrets) Delete(ctx context.Context, actor model.Actor, owner, name string) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := readSecret(ctx, q, owner, name)
		if err != nil {
			return effect{}, err
		}
		appIDs, err := q.ListAppsUsingSecret(ctx, row.ID)
		if err != nil {
			return effect{}, err
		}
		jobIDs, err := q.ListJobsUsingSecret(ctx, row.ID)
		if err != nil {
			return effect{}, err
		}
		if len(appIDs) > 0 || len(jobIDs) > 0 {
			inUse := &SecretInUseError{}
			for _, id := range appIDs {
				app, err := readApp(ctx, q, id)
				if err != nil {
					return effect{}, err
				}
				inUse.Apps = append(inUse.Apps, SecretApp{ID: app.ID, Name: app.Name, DisplayName: app.DisplayName})
			}
			for _, id := range jobIDs {
				job, err := readJob(ctx, q, id)
				if err != nil {
					return effect{}, err
				}
				inUse.Jobs = append(inUse.Jobs, SecretJob{ID: job.ID, Name: job.Name, DisplayName: job.Spec.DisplayName})
			}
			return effect{}, inUse
		}
		if _, err := q.DeleteSecret(ctx, row.ID); err != nil {
			return effect{}, err
		}
		return effect{audit: secretAudit("secret.delete", secretOf(row), nil)}, nil
	})
}

// DeleteFor removes every secret of owner (their account is being removed)
// and returns how many. Apps and jobs that used them are announced. It is
// audited.
func (x *Secrets) DeleteFor(ctx context.Context, actor model.Actor, owner string) (int64, error) {
	var n int64
	err := x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		rows, err := q.ListSecretsForOwner(ctx, owner)
		if err != nil {
			return effect{}, err
		}
		var changes []Change
		for _, r := range rows {
			c, err := userChanges(ctx, q, r.ID)
			if err != nil {
				return effect{}, err
			}
			changes = append(changes, c...)
		}
		if n, err = q.DeleteSecretsForOwner(ctx, owner); err != nil {
			return effect{}, err
		}
		return effect{audit: &auditRow{action: "secret.delete_all", kind: secretKind, owner: owner, detail: map[string]int64{"count": n}}, changes: changes}, nil
	})
	return n, err
}

// Bindings returns the secrets app gets as variables, by variable name (its
// repository token is apart: GitTokenSecret).
func (x *Secrets) Bindings(ctx context.Context, appID string) ([]model.SecretBinding, error) {
	rows, err := x.s.q.ListAppSecretBindings(ctx, appID)
	if err != nil {
		return nil, err
	}
	out := make([]model.SecretBinding, 0, len(rows))
	for _, r := range rows {
		if r.Env != model.GitTokenEnv {
			out = append(out, model.SecretBinding{Env: r.Env, Secret: r.SecretName})
		}
	}
	return out, nil
}

// bindingNames is the audit detail of a set of bindings: variable to secret name.
func bindingNames(bindings []model.SecretBinding) map[string]any {
	names := make(map[string]string, len(bindings))
	for _, b := range bindings {
		names[b.Env] = b.Secret
	}
	return map[string]any{"secrets": names}
}

// writeAppBindings replaces app's bindings, resolving each secret name under owner.
func writeAppBindings(ctx context.Context, q *sqlcgen.Queries, appID, owner string, bindings []model.SecretBinding) error {
	if err := q.DeleteAppSecrets(ctx, appID); err != nil {
		return err
	}
	return resolveBindings(ctx, q, owner, bindings, func(env, secretID string) error {
		return q.InsertAppSecret(ctx, sqlcgen.InsertAppSecretParams{AppID: appID, Env: env, SecretID: secretID})
	})
}

// writeJobBindings replaces job's bindings, resolving each secret name under owner.
func writeJobBindings(ctx context.Context, q *sqlcgen.Queries, jobID, owner string, bindings []model.SecretBinding) error {
	if err := q.DeleteJobSecrets(ctx, jobID); err != nil {
		return err
	}
	return resolveBindings(ctx, q, owner, bindings, func(env, secretID string) error {
		return q.InsertJobSecret(ctx, sqlcgen.InsertJobSecretParams{JobID: jobID, Env: env, SecretID: secretID})
	})
}

// resolveBindings looks up each binding's secret by name under owner and
// hands its id to insert. A name owner has no secret of is a
// *SecretMissingError: a binding never reaches another person's secret.
func resolveBindings(ctx context.Context, q *sqlcgen.Queries, owner string, bindings []model.SecretBinding, insert func(env, secretID string) error) error {
	for _, b := range bindings {
		row, err := readSecret(ctx, q, owner, b.Secret)
		if errors.Is(err, ErrNotFound) {
			return &SecretMissingError{Name: b.Secret}
		}
		if err != nil {
			return err
		}
		if err := insert(b.Env, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// JobBindings returns the secrets job's runs get, by variable name.
func (x *Secrets) JobBindings(ctx context.Context, jobID string) ([]model.SecretBinding, error) {
	rows, err := x.s.q.ListJobSecretBindings(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]model.SecretBinding, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.SecretBinding{Env: r.Env, Secret: r.SecretName})
	}
	return out, nil
}

// ForApp returns the secrets app gets with their sealed values, its
// repository token (model.GitTokenEnv) among them, for the controller to open. Callers must never log a value.
func (x *Secrets) ForApp(ctx context.Context, appID string) ([]SealedBinding, error) {
	rows, err := x.s.q.ListAppSecretValues(ctx, appID)
	if err != nil {
		return nil, err
	}
	out := make([]SealedBinding, 0, len(rows))
	for _, r := range rows {
		b, err := sealedBinding(r.Env, r.Ciphertext, model.Secret{ID: r.SecretID, Owner: r.SecretOwner, Name: r.SecretName, Version: r.SecretVersion})
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// ForJob returns the secrets job's runs get with their sealed values, for the
// controller to open. Callers must never log a value.
func (x *Secrets) ForJob(ctx context.Context, jobID string) ([]SealedBinding, error) {
	rows, err := x.s.q.ListJobSecretValues(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := make([]SealedBinding, 0, len(rows))
	for _, r := range rows {
		b, err := sealedBinding(r.Env, r.Ciphertext, model.Secret{ID: r.SecretID, Owner: r.SecretOwner, Name: r.SecretName, Version: r.SecretVersion})
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// sealedBinding decodes the stored ciphertext of sec, bound as env.
func sealedBinding(env, ciphertext string, sec model.Secret) (SealedBinding, error) {
	ct, err := decode(ciphertext)
	if err != nil {
		return SealedBinding{}, fmt.Errorf("store: secret %s holds no valid ciphertext", sec.ID)
	}
	return SealedBinding{Env: env, Ciphertext: ct, Secret: sec}, nil
}

// Sealed returns the stored ciphertext of the secret with this id.
func (x *Secrets) Sealed(ctx context.Context, id string) ([]byte, error) {
	row, err := x.s.q.GetSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decode(row.Ciphertext)
}

// AllSealed returns every stored ciphertext, for rotation.
func (x *Secrets) AllSealed(ctx context.Context) ([]SealedSecret, error) {
	rows, err := x.s.q.ListSecretCiphertexts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SealedSecret, 0, len(rows))
	for _, r := range rows {
		ct, err := decode(r.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("store: secret %s holds no valid ciphertext", r.ID)
		}
		out = append(out, SealedSecret{ID: r.ID, Owner: r.Owner, Name: r.Name, Ciphertext: ct})
	}
	return out, nil
}

// Reseal stores the same value sealed again (rotation) if the stored
// ciphertext is still old; false when a new value arrived since. The version
// stays, so no app restarts. Not audited: no value changes.
func (x *Secrets) Reseal(ctx context.Context, id string, old, resealed []byte) (bool, error) {
	n, err := x.s.q.ResealSecret(ctx, sqlcgen.ResealSecretParams{Ciphertext: encode(resealed), ID: id, Ciphertext_2: encode(old)})
	return n == 1, err
}

// SecretKeyset is the stored data keyset (see package secrets).
type SecretKeyset struct {
	Wrapped, Public []byte
	Version         int64
}

// SecretKeys is the secret_keysets table.
type SecretKeys struct{ s *Store }

// SecretKeys returns the secret_keysets table.
func (s *Store) SecretKeys() *SecretKeys { return &SecretKeys{s} }

// Get returns the data keyset; found is false before one was made.
func (k *SecretKeys) Get(ctx context.Context) (ks SecretKeyset, found bool, err error) {
	row, err := k.s.q.GetSecretKeyset(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return SecretKeyset{}, false, nil
	}
	if err != nil {
		return SecretKeyset{}, false, err
	}
	w, err := decode(row.Wrapped)
	if err != nil {
		return SecretKeyset{}, false, err
	}
	p, err := decode(row.Public)
	if err != nil {
		return SecretKeyset{}, false, err
	}
	return SecretKeyset{Wrapped: w, Public: p, Version: row.Version}, true, nil
}

// Insert stores the first data keyset; false when one already exists (it is
// kept). Not audited: it holds no person's data.
func (k *SecretKeys) Insert(ctx context.Context, ks SecretKeyset) (bool, error) {
	n, err := k.s.q.InsertSecretKeyset(ctx, sqlcgen.InsertSecretKeysetParams{
		Wrapped: encode(ks.Wrapped), Public: encode(ks.Public), UpdatedAt: dbtime.From(now()),
	})
	return n == 1, err
}

// Replace stores a rotated keyset if the stored one is still at version,
// else ErrConflict.
func (k *SecretKeys) Replace(ctx context.Context, ks SecretKeyset, version int64) error {
	n, err := k.s.q.UpdateSecretKeyset(ctx, sqlcgen.UpdateSecretKeysetParams{
		Wrapped: encode(ks.Wrapped), Public: encode(ks.Public), UpdatedAt: dbtime.From(now()), Version: version,
	})
	if err == nil && n == 0 {
		err = ErrConflict
	}
	return err
}
