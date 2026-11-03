package dbflag

import "testing"

func TestDatabaseFlagsRefuseAnUnknownEngineAndAPostgresWithoutADSNFile(t *testing.T) {
	for name, d := range map[string]Flags{
		"unknown engine":        {Engine: "mysql"},
		"postgres without file": {Engine: "postgres"},
	} {
		if _, err := d.Config(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
