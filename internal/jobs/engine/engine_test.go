package engine_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/jobs/engine"
)

func TestLastLineSkipsTrailingBlankLines(t *testing.T) {
	if got := engine.LastLine([]byte("a\nboom\n\n  \n")); got != "boom" {
		t.Errorf("LastLine = %q", got)
	}
	if got := engine.LastLine(nil); got != "" {
		t.Errorf("LastLine(nil) = %q", got)
	}
}
