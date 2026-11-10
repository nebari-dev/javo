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

// DeviceLogin is one row of the device_logins table: a coding agent's
// sign-in waiting for a person to approve it. CodeHash is the SHA-256 of the
// device code, so the code itself is never stored, and neither is the token
// the approval leads to. State is pending, approved, denied or spent; Owner
// is the person who approved or denied. LastPollAt is zero until the agent
// first polls.
type DeviceLogin struct {
	ID         string
	UserCode   string
	CodeHash   []byte
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastPollAt time.Time
	State      string
	Owner      string
}

// The states of a DeviceLogin.
const (
	DevicePending  = "pending"
	DeviceApproved = "approved"
	DeviceDenied   = "denied"
	DeviceSpent    = "spent"
)

// DeviceLogins is the device_logins table. Only the decisions are audited:
// starting and polling are an agent's plumbing, and the token a poll takes
// is audited when it is made.
type DeviceLogins struct{ s *Store }

// DeviceLogins returns the device_logins table.
func (s *Store) DeviceLogins() *DeviceLogins { return &DeviceLogins{s} }

// Create stores a new request under a new id; l.ID is ignored.
func (d *DeviceLogins) Create(ctx context.Context, l DeviceLogin) error {
	return d.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.InsertDeviceLogin(ctx, sqlcgen.InsertDeviceLoginParams{
			ID: newID(), UserCode: l.UserCode, CodeHash: l.CodeHash, CreatedAt: dbtime.From(l.CreatedAt),
			ExpiresAt: dbtime.From(l.ExpiresAt), LastPollAt: dbtime.From(l.LastPollAt), State: l.State, Owner: l.Owner,
		})
	})
}

// ByUserCode returns the request with this user code, expired or not, or
// ErrNotFound.
func (d *DeviceLogins) ByUserCode(ctx context.Context, code string) (DeviceLogin, error) {
	return deviceLoginOf(d.s.q.GetDeviceLoginByUserCode(ctx, code))
}

// ByCodeHash returns the request whose device code hashes to hash, expired
// or not, or ErrNotFound.
func (d *DeviceLogins) ByCodeHash(ctx context.Context, hash []byte) (DeviceLogin, error) {
	return deviceLoginOf(d.s.q.GetDeviceLoginByCodeHash(ctx, hash))
}

// Decide records a pending request as approved or denied by owner, audited
// as device.approve or device.deny. It reports false, and changes nothing,
// when the request was already decided or is gone.
func (d *DeviceLogins) Decide(ctx context.Context, actor model.Actor, id, state, owner string) (bool, error) {
	decided := false
	err := d.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.DecideDeviceLogin(ctx, sqlcgen.DecideDeviceLoginParams{State: state, Owner: owner, ID: id})
		if err != nil || n == 0 {
			return effect{}, err
		}
		decided = true
		action := "device.deny"
		if state == DeviceApproved {
			action = "device.approve"
		}
		return effect{audit: &auditRow{action: action, kind: "device", name: id, owner: owner}}, nil
	})
	return decided, err
}

// Spend moves an approved request to spent in one conditional update. It
// reports true to exactly one caller, the poll that may make the token.
func (d *DeviceLogins) Spend(ctx context.Context, id string) (bool, error) {
	var n int64
	err := d.s.inTx(ctx, func(q *sqlcgen.Queries) (err error) {
		n, err = q.SpendDeviceLogin(ctx, id)
		return err
	})
	return n > 0, err
}

// Polled records when the agent last polled, for slow_down.
func (d *DeviceLogins) Polled(ctx context.Context, id string, at time.Time) error {
	return d.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.SetDeviceLoginPolled(ctx, sqlcgen.SetDeviceLoginPolledParams{LastPollAt: dbtime.From(at), ID: id})
	})
}

// Sweep deletes requests that expired before now and returns how many.
func (d *DeviceLogins) Sweep(ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := d.s.inTx(ctx, func(q *sqlcgen.Queries) (err error) {
		n, err = q.SweepDeviceLogins(ctx, dbtime.From(now))
		return err
	})
	return n, err
}

func deviceLoginOf(r sqlcgen.DeviceLogin, err error) (DeviceLogin, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceLogin{}, ErrNotFound
	}
	if err != nil {
		return DeviceLogin{}, err
	}
	return DeviceLogin{
		ID: r.ID, UserCode: r.UserCode, CodeHash: r.CodeHash, CreatedAt: r.CreatedAt.Time(), ExpiresAt: r.ExpiresAt.Time(),
		LastPollAt: r.LastPollAt.Time(), State: r.State, Owner: r.Owner,
	}, nil
}
