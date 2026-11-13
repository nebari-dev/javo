package status

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapMessageKeepsFirstLines(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "x")
	}
	if got := strings.Count(CapMessage(lines, 20, 300), "\n"); got != 19 {
		t.Fatalf("newlines = %d, want 19 (20 lines)", got)
	}
}

func TestCapMessageCutsOnRuneBoundary(t *testing.T) {
	got := CapMessage([]string{strings.Repeat("é", 400)}, 20, 300)
	if !utf8.ValidString(got) || len(got) > 300 || !strings.HasSuffix(got, "...") {
		t.Fatalf("len %d valid %v", len(got), utf8.ValidString(got))
	}
}
