// Package dbtime stores instants as integer Unix milliseconds, so SQLite and
// Postgres hold the same value in the same column type (BIGINT) and compare
// and sort alike. sqlc maps every time column to these types.
package dbtime

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// Time is an instant with millisecond precision. The zero value is the zero
// time.Time.
type Time struct{ t time.Time }

// From wraps t, dropping precision below a millisecond.
func From(t time.Time) Time { return Time{t: time.UnixMilli(t.UnixMilli()).UTC()} }

// Time returns the instant, in UTC.
func (t Time) Time() time.Time { return t.t }

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) { return t.t.UnixMilli(), nil }

// Scan implements sql.Scanner.
func (t *Time) Scan(src any) error {
	ms, ok := src.(int64)
	if !ok {
		return fmt.Errorf("dbtime: cannot scan %T, want int64 milliseconds", src)
	}
	t.t = time.UnixMilli(ms).UTC()
	return nil
}

// NullTime is a Time that may be NULL.
type NullTime struct {
	Time  Time
	Valid bool
}

// Value implements driver.Valuer.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Time.Value()
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(src any) error {
	if src == nil {
		*n = NullTime{}
		return nil
	}
	if err := n.Time.Scan(src); err != nil {
		return err
	}
	n.Valid = true
	return nil
}
