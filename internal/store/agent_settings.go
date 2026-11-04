package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// AgentSetting is a person's own model for their coding-agent apps: an
// OpenAI-compatible base URL and model, and the last four characters of
// the key, which is their secret model.AgentKeySecret.
type AgentSetting struct {
	Owner, BaseURL, Model, KeyLast4 string
	UpdatedAt                       time.Time
}

// AgentSettings is the agent_settings table.
type AgentSettings struct{ s *Store }

// AgentSettings returns the agent_settings table.
func (s *Store) AgentSettings() *AgentSettings { return &AgentSettings{s} }

// Put stores a's setting, replacing the owner's earlier one.
func (x *AgentSettings) Put(ctx context.Context, a AgentSetting) error {
	return x.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.UpsertAgentSetting(ctx, sqlcgen.UpsertAgentSettingParams{
			Owner: a.Owner, BaseUrl: a.BaseURL, Model: a.Model, KeyLast4: a.KeyLast4, UpdatedAt: dbtime.From(now()),
		})
	})
}

// Get returns the owner's setting, or ErrNotFound.
func (x *AgentSettings) Get(ctx context.Context, owner string) (AgentSetting, error) {
	r, err := x.s.q.GetAgentSetting(ctx, owner)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSetting{}, ErrNotFound
	}
	if err != nil {
		return AgentSetting{}, err
	}
	return AgentSetting{Owner: r.Owner, BaseURL: r.BaseUrl, Model: r.Model, KeyLast4: r.KeyLast4, UpdatedAt: r.UpdatedAt.Time()}, nil
}

// Delete removes the owner's setting; none is fine.
func (x *AgentSettings) Delete(ctx context.Context, owner string) error {
	return x.s.inTx(ctx, func(q *sqlcgen.Queries) error { return q.DeleteAgentSetting(ctx, owner) })
}
