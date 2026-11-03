// Package exitcode carries the exit code a command wants javo to end with.
package exitcode

import "fmt"

// Error makes main exit with Code without printing a second error line: the
// command has already said why (on stderr or in the termination message).
type Error struct{ Code int }

func (e Error) Error() string { return fmt.Sprintf("run exited with code %d", e.Code) }
