package store

import (
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
)

// nullTime is the column value of t; the zero time is NULL.
func nullTime(t time.Time) dbtime.NullTime {
	if t.IsZero() {
		return dbtime.NullTime{}
	}
	return dbtime.NullTime{Time: dbtime.From(t), Valid: true}
}

// timeOf is the time in a nullable column; NULL is the zero time.
func timeOf(n dbtime.NullTime) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return n.Time.Time()
}
