package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Previews is the previews of an app: the child rows of the apps table that
// point at it, and the app_previews table (webhook and last sync). The
// preview reconciler writes child rows through Put and Remove, which refuse
// any row that is not a preview, as the system controller.
type Previews struct{ s *Store }

// Previews returns the previews of apps.
func (s *Store) Previews() *Previews { return &Previews{s} }

var systemController = model.Actor{System: model.SystemController}

// Children returns the preview rows of the app, deleted ones included (a row
// marked deleted still holds its slot until it is purged), by pull request.
func (p *Previews) Children(ctx context.Context, parentID string) ([]model.App, error) {
	rows, err := p.s.q.ListAppChildren(ctx, sql.NullString{String: parentID, Valid: true})
	if err != nil {
		return nil, err
	}
	out := make([]model.App, len(rows))
	for i, row := range rows {
		if out[i], err = loadApp(ctx, p.s.q, row, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Put creates the preview child describes (ParentID and PullNumber set), or,
// when the pull request already has one, brings it up to date with child:
// the head commit and every setting a preview takes from its parent (see
// inherit). A child equal to the stored row changes nothing and returns the
// row as it is. A child without ParentID or Source is ErrNotPreview; a
// missing or deleted parent is ErrNotFound; a preview marked deleted and not
// yet purged is ErrExists; a version conflict is ErrConflict.
func (p *Previews) Put(ctx context.Context, child *model.App) (model.App, error) {
	if child.ParentID == "" || child.Source == nil {
		return model.App{}, ErrNotPreview
	}
	// An early answer only: the insert below takes the lock that decides.
	if _, err := liveApp(ctx, p.s.q, child.ParentID); err != nil {
		return model.App{}, err
	}
	existing, err := p.s.q.GetAppChild(ctx, sqlcgen.GetAppChildParams{
		ParentID: sql.NullString{String: child.ParentID, Valid: true}, PullNumber: int64(child.PullNumber),
	})
	if errors.Is(err, sql.ErrNoRows) {
		// The lock makes a concurrent delete of the parent wait for this insert
		// (and then mark the new row) or be seen here (and refuse it).
		return p.s.Apps().create(ctx, systemController, child, func(q *sqlcgen.Queries, _ string) error {
			if n, err := q.LockLiveApp(ctx, child.ParentID); err != nil || n == 0 {
				if err == nil {
					err = ErrNotFound
				}
				return err
			}
			return nil
		})
	}
	if err != nil {
		return model.App{}, err
	}
	if existing.DeletedAt.Valid {
		return model.App{}, ErrExists
	}
	have, err := loadApp(ctx, p.s.q, existing, nil)
	if err != nil {
		return model.App{}, err
	}
	if have.Source == nil {
		return model.App{}, ErrNotPreview
	}
	want := inheritedOf(child)
	if equality.Semantic.DeepEqual(inheritedOf(&have), want) {
		return have, nil
	}
	return p.s.Apps().UpdateSpec(ctx, systemController, have.ID, have.Version, func(a *model.App) error {
		want.applyTo(a)
		return nil
	})
}

// inherited is every field of a preview that follows its parent or its pull
// request: all of the spec except the row's identity (id, namespace, name,
// owner, parent, pull request) and its pause state.
type inherited struct {
	DisplayName, Template, File string
	Sample                      bool
	Size                        string
	Resources                   *model.AppResources
	Command, Image              string
	Port                        int32
	APIAccess                   string
	Source                      *model.Source
	Sharing                     model.Sharing
}

func inheritedOf(a *model.App) inherited {
	i := inherited{
		a.DisplayName, a.Template, a.File, a.Sample, a.Size, a.Resources, a.Command, a.Image, a.Port, a.APIAccess, a.Source, a.Sharing,
	}
	if i.Sharing.Access == "" { // the zero value means owner only, which the store saves as Private
		i.Sharing.Access = model.AccessPrivate
	}
	return i
}

func (i inherited) applyTo(a *model.App) {
	a.DisplayName, a.Template, a.File, a.Sample, a.Size, a.Resources = i.DisplayName, i.Template, i.File, i.Sample, i.Size, i.Resources
	a.Command, a.Image, a.Port, a.APIAccess, a.Source, a.Sharing = i.Command, i.Image, i.Port, i.APIAccess, i.Source, i.Sharing
}

// Remove marks a preview deleted, as the controller. A row that is not a
// preview is ErrNotPreview; a missing one or one already marked is ErrNotFound.
func (p *Previews) Remove(ctx context.Context, childID string) error {
	app, err := p.s.Apps().Get(ctx, childID)
	if err != nil {
		return err
	}
	if app.ParentID == "" {
		return ErrNotPreview
	}
	return p.s.Apps().MarkDeleted(ctx, systemController, childID)
}

// RecordSync stores what the last look at the app's pull requests found, cut
// to what the store keeps (model.PreviewSync.Bounded). It is the controller's
// own write: not audited, not announced.
func (p *Previews) RecordSync(ctx context.Context, parentID string, sync model.PreviewSync, at time.Time) error {
	text, err := marshal(sync.Bounded(), "{}")
	if err != nil {
		return err
	}
	return p.s.q.UpsertPreviewSync(ctx, sqlcgen.UpsertPreviewSyncParams{AppID: parentID, SyncedAt: nullTime(at), Sync: text})
}

// Sync returns the last recorded result and when it was recorded (the zero
// time before the first one). An app with no row yet is ErrNotFound.
func (p *Previews) Sync(ctx context.Context, parentID string) (model.PreviewSync, time.Time, error) {
	row, err := p.s.q.GetPreviewSync(ctx, parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.PreviewSync{}, time.Time{}, ErrNotFound
	}
	if err != nil {
		return model.PreviewSync{}, time.Time{}, err
	}
	var sync model.PreviewSync
	if err := unmarshal(row.Sync, &sync); err != nil {
		return model.PreviewSync{}, time.Time{}, err
	}
	return sync, timeOf(row.SyncedAt), nil
}

// SetHook makes the app a new webhook address and secret (16 and 32 random
// bytes, base64url), replacing any earlier pair, whose id stops resolving at
// once. The secret is stored sealed (SetHookKey): the signature check needs
// it back, a copy of the database must not give it. A missing or deleted app
// is ErrNotFound. The audit row carries neither value.
func (p *Previews) SetHook(ctx context.Context, actor model.Actor, parentID string) (hookID, secret string, err error) {
	if hookID, secret, err = NewHook(); err != nil {
		return "", "", err
	}
	sealed, err := p.s.sealHook(hookID, secret)
	if err != nil {
		return "", "", err
	}
	err = p.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		app, err := liveApp(ctx, q, parentID)
		if err != nil {
			return effect{}, err
		}
		err = q.UpsertPreviewHook(ctx, sqlcgen.UpsertPreviewHookParams{
			AppID: parentID, HookID: sql.NullString{String: hookID, Valid: true}, HookSecret: sealed,
		})
		return effect{audit: appKind.effect("app.preview_hook_set", app).audit}, err
	})
	if err != nil {
		return "", "", err
	}
	return hookID, secret, nil
}

// NewHook makes a webhook address id and secret (16 and 32 random bytes,
// base64url) without storing them: SetHook stores them for an app, and
// CreateFromRepo for an app created with them.
func NewHook() (hookID, secret string, err error) {
	if hookID, err = randomToken(16); err != nil {
		return "", "", err
	}
	if secret, err = randomToken(32); err != nil {
		return "", "", err
	}
	return hookID, secret, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HasHook says whether the app has a webhook address and secret.
func (p *Previews) HasHook(ctx context.Context, appID string) (bool, error) {
	n, err := p.s.q.CountPreviewHooks(ctx, appID)
	return n > 0, err
}

// ByHook returns the app a webhook address belongs to and the secret that
// signs its deliveries. An unknown address is ErrNotFound.
func (p *Previews) ByHook(ctx context.Context, hookID string) (parentID string, secret []byte, err error) {
	if hookID == "" {
		return "", nil, ErrNotFound
	}
	row, err := p.s.q.GetPreviewByHook(ctx, sql.NullString{String: hookID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	secret, clear, err := p.s.openHook(hookID, row.HookSecret)
	if err != nil {
		return "", nil, err
	}
	if clear && len(secret) > 0 {
		if err := p.s.resealHook(ctx, row.AppID, hookID, row.HookSecret); err != nil {
			return "", nil, err
		}
	}
	return row.AppID, secret, nil
}

// PokedAt is when the app's webhook last delivered (the zero time if never).
func (p *Previews) PokedAt(ctx context.Context, appID string) (time.Time, error) {
	at, err := p.s.q.GetPreviewPoke(ctx, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return timeOf(at), nil
}

// Poke records that a delivery for the hook arrived at at and announces the
// app's previews so the reconciler looks at the pull requests early. It is
// per-request plumbing: not audited. An unknown address is ErrNotFound.
func (p *Previews) Poke(ctx context.Context, hookID string, at time.Time) error {
	return p.s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		appID, err := q.PokePreview(ctx, sqlcgen.PokePreviewParams{
			PokedAt: dbtime.NullTime{Time: dbtime.From(at), Valid: true}, HookID: sql.NullString{String: hookID, Valid: true},
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return []Change{{"app_previews", appID}}, err
	})
}
