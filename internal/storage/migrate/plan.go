// Package migrate holds the pure part of a home mode change: which homes
// would move and how much data that is.
package migrate

import (
	"github.com/nebari-dev/javo/internal/model"
)

// Plan summarises the move of every home to the chosen mode. Users counts the
// homes on another mode plus those already moved to the chosen mode, so Done
// and Failed read against it. A switched home whose mode is no longer the
// chosen one has to move again and counts as still to move. A home that has no claim yet has no mode and is not
// part of it: it is created on the chosen mode.
func Plan(homes []model.Home, choice model.StorageChoice) model.MigrationStatus {
	out := model.MigrationStatus{To: choice.Mode}
	for i := range homes {
		h := &homes[i]
		phase := model.MigrationPhase("")
		if h.Status.Migration != nil {
			phase = h.Status.Migration.Phase
		}
		switch {
		case phase == model.MigrationSwitched && h.Status.Mode == choice.Mode:
			out.Users++
			out.Done++
		case h.Status.Mode != "" && h.Status.Mode != choice.Mode:
			out.Users++
			if out.From == "" {
				out.From = h.Status.Mode
			}
			if h.Spec.Size != nil {
				out.RequestedBytes += h.Spec.Size.Value()
			}
			if phase == model.MigrationFailed {
				out.Failed++
			}
		}
	}
	return out
}

// copyBytesPerSecond is the rate the notice assumes for the verified copy:
// 100 MiB/s, a typical network disk. It is an estimate for the user's banner.
const copyBytesPerSecond = 100 << 20

// StopMinutes estimates how long a home's apps stay stopped while its files
// copy and verify, rounded up to a multiple of five minutes (at least five).
// A home of unknown size (bytes 0) gets the shortest estimate.
func StopMinutes(bytes int64) int {
	seconds := bytes / copyBytesPerSecond
	return max(1, int((seconds+299)/300)) * 5
}
