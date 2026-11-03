package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Help text is read by admins in the pod, who have no checkout of the
// repository: it names the admin guide's pages, never a source file path.
func TestHelpAndErrorsHaveNoRepositoryPaths(t *testing.T) {
	bad := func(s string) bool { return strings.Contains(s, "site/src") || strings.Contains(s, ".mdx") }
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if bad(c.Short) || bad(c.Long) || bad(c.Example) {
			t.Errorf("%q help names a repository file: %q", c.CommandPath(), c.Short+" "+c.Long)
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if bad(f.Usage) {
				t.Errorf("%q flag --%s names a repository file: %q", c.CommandPath(), f.Name, f.Usage)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(New(nil, nil))

	root := New(nil, nil)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"db", "restore", "--database-engine", "postgres", "--database-dsn-file", "/nonexistent", "x.dump"})
	err := root.Execute()
	if err == nil || bad(err.Error()) {
		t.Errorf("restore on postgres = %v, want an error without a repository path", err)
	}
}
