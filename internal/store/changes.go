package store

// changeBuffer is how many announcements wait for the consumer.
const changeBuffer = 1024

// Change says which row of which table was written. It is announced after
// the write commits, so a reader that acts on it sees the new row.
type Change struct {
	Table string
	ID    string
}

// TableResync is the Table of the Change that says "announcements may have
// been missed: look at everything". The Postgres feed emits it after every
// reconnect.
const TableResync = "*"

// Resync is that change.
var Resync = Change{Table: TableResync}

// Changes returns the announcements of committed writes made through this
// store. Status writes are not announced (they are the controller's own), and
// a full buffer drops an announcement: the controller's periodic resync
// covers it, so a slow consumer never blocks a request.
func (s *Store) Changes() <-chan Change { return s.changes }
