package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// AISetting is the platform admin's AI settings, used in place of the
// platform config's provider, base URL, model and key. KeySealed is the key
// as the gateway sealed it (nil: none stored) and KeyLast4 what is shown of
// it. Rev is new on every write.
type AISetting struct {
	Provider, BaseURL, Model string
	KeySealed                []byte
	KeyLast4, Rev            string
	UpdatedAt                time.Time
}

// AISettings is the ai_settings table: one row or none.
type AISettings struct{ s *Store }

// AISettings returns the ai_settings table.
func (s *Store) AISettings() *AISettings { return &AISettings{s} }

// aiSettingsAudit names the row on the audit log.
const aiSettingsKind, aiSettingsName = "settings", "ai"

// Put replaces the settings with a under a new revision, and records actor
// and the names of the fields that changed (never a value) on the audit log.
func (x *AISettings) Put(ctx context.Context, actor model.Actor, a AISetting, changed []string) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		err := q.UpsertAISetting(ctx, sqlcgen.UpsertAISettingParams{
			Provider: a.Provider, BaseUrl: a.BaseURL, Model: a.Model, KeySealed: a.KeySealed, KeyLast4: a.KeyLast4,
			Rev: newID(), UpdatedAt: dbtime.From(now()),
		})
		return effect{audit: &auditRow{action: "ai.settings.update", kind: aiSettingsKind, name: aiSettingsName,
			detail: map[string][]string{"fields": changed}}}, err
	})
}

// Get returns the settings, or ErrNotFound when the admin set none.
func (x *AISettings) Get(ctx context.Context) (AISetting, error) {
	r, err := x.s.q.GetAISetting(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return AISetting{}, ErrNotFound
	}
	if err != nil {
		return AISetting{}, err
	}
	return AISetting{
		Provider: r.Provider, BaseURL: r.BaseUrl, Model: r.Model, KeySealed: r.KeySealed, KeyLast4: r.KeyLast4,
		Rev: r.Rev, UpdatedAt: r.UpdatedAt.Time(),
	}, nil
}

// Reset removes the settings, so the platform config applies again, and
// records it on the audit log; none stored is fine and records nothing.
func (x *AISettings) Reset(ctx context.Context, actor model.Actor) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.DeleteAISetting(ctx)
		if err != nil || n == 0 {
			return effect{}, err
		}
		return effect{audit: &auditRow{action: "ai.settings.reset", kind: aiSettingsKind, name: aiSettingsName}}, nil
	})
}
