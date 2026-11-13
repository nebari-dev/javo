// Command javo is the javo binary: the gateway, the controller, the steps the
// platform's pods run and the admin commands (internal/cli).
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/nebari-dev/javo/internal/cli"
	"github.com/nebari-dev/javo/internal/cli/exitcode"
)

func main() {
	if err := cli.New(nil, nil).Execute(); err != nil {
		var ee exitcode.Error
		if errors.As(err, &ee) {
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
