package podcmd

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/cli/clitest"
)

// pause blocks, and returns without an error once the process is told to
// end.
func TestPauseBlocksUntilToldToEnd(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		// The test listens as well, so a signal sent before pause listens
		// does not end the test binary.
		mine := make(chan os.Signal, 1)
		signal.Notify(mine, sig)
		done := make(chan error, 1)
		go func() {
			root := clitest.Root(NewPause())
			root.SetArgs([]string{"pause"})
			done <- root.Execute()
		}()
		select {
		case err := <-done:
			t.Fatalf("pause returned before any signal: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		deadline := time.After(10 * time.Second)
		for ended := false; !ended; {
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("pause after %v: %v", sig, err)
				}
				ended = true
			case <-deadline:
				t.Fatalf("pause still blocks after %v", sig)
			case <-time.After(5 * time.Millisecond):
			}
		}
		signal.Stop(mine)
	}
}

func TestPauseIsHidden(t *testing.T) {
	if !NewPause().Hidden {
		t.Fatal("pause shows in the help: it is for the Docker executor only")
	}
}
