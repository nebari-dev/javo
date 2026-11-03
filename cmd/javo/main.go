// Command javo is the javo binary: the gateway, the controller, the steps the
// platform's pods run and the admin commands (internal/cli).
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/nebari-dev/javo/internal/cli"
	"github.com/nebari-dev/javo/internal/cli/exitcode"
	"github.com/nebari-dev/javo/internal/cli/importcmd"
	"github.com/nebari-dev/javo/internal/cli/kubeclient"
)

func main() {
	importcmd.FollowJobLog = importcmd.FollowJobLogInCluster
	if err := cli.New(kubeclient.Default, nil).Execute(); err != nil {
		var ee exitcode.Error
		if errors.As(err, &ee) {
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
