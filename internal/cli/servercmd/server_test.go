package servercmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestParseComponents(t *testing.T) {
	for _, tc := range []struct {
		in                  []string
		gateway, controller bool
		bad                 string
	}{
		{[]string{"gateway", "controller"}, true, true, ""},
		{[]string{"controller"}, false, true, ""},
		{[]string{"gateway", "gateway"}, true, false, ""},
		{nil, false, false, "at least one"},
		{[]string{"gateway", "proxy"}, false, false, `"proxy"`},
	} {
		g, c, err := parseComponents(tc.in)
		if tc.bad != "" {
			if err == nil || !strings.Contains(err.Error(), tc.bad) {
				t.Errorf("parseComponents(%v) = %v, want an error naming %s", tc.in, err, tc.bad)
			}
			continue
		}
		if err != nil || g != tc.gateway || c != tc.controller {
			t.Errorf("parseComponents(%v) = %v %v %v, want %v %v", tc.in, g, c, err, tc.gateway, tc.controller)
		}
	}
}

// One process serves both components from one set of flags: every flag either
// component takes is also a flag of `javo server`, and the database one is a
// single flag, not two.
func TestServerTakesTheFlagsOfBothComponents(t *testing.T) {
	server := NewServer().Flags()
	for _, c := range []*cobra.Command{NewGateway(), NewController()} {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Deprecated != "" {
				return
			}
			if server.Lookup(f.Name) == nil {
				t.Errorf("javo server lacks --%s", f.Name)
			}
		})
	}
	if server.Lookup("components") == nil {
		t.Error("javo server lacks --components")
	}
}

func TestServerRefusesScheduledBackupsOnPostgres(t *testing.T) {
	cmd := NewServer()
	cmd.SetArgs([]string{"--database-engine=postgres", "--database-dsn-file=/nonexistent", "--backup-schedule=0 3 * * *", "--backup-dir=/backups"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pg_dump") {
		t.Fatalf("server = %v, want a refusal that points to the pg_dump CronJob", err)
	}
}

func TestServerNeedsADirectoryForScheduledBackups(t *testing.T) {
	cmd := NewServer()
	cmd.SetArgs([]string{"--backup-schedule=0 3 * * *"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "--backup-dir") {
		t.Fatalf("server = %v, want an error naming --backup-dir", err)
	}
}
