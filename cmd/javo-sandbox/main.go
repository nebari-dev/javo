// Command javo-sandbox runs a command locked in with Landlock, a Linux
// security module any unprivileged process can use to give up file access:
//
//	javo-sandbox [--rw DIR]... [--ro DIR]... [--min-abi N] -- COMMAND ARGS...
//
// The command, and everything it starts, may then read and write only under
// the --rw folders and read and execute only under the --ro folders; any
// other file is refused with "Permission denied". The coding agent apps start
// through it when the platform turns the lock on (config workloads.landlock),
// so an agent, and every command it runs, sees its project and not the rest
// of the owner's home (~/.ssh, other projects).
//
// It fails closed: on a kernel without Landlock, or with an older version
// than --min-abi, it says so and exits with exitRefused without running the
// command, rather than run it unlocked.
package main

import (
	"fmt"
	"os"
)

// exitRefused is the exit code when the command could not be locked in (and
// so never ran), or the command line was wrong: 125, as env, docker run and
// git bisect use for "the wrapper failed, not the command".
const exitRefused = 125

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "javo-sandbox:", err)
		os.Exit(exitRefused)
	}
}
