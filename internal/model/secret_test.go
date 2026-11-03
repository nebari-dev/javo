package model

import (
	"strings"
	"testing"
)

func TestEnvNames(t *testing.T) {
	for _, ok := range []string{"OPENAI_API_KEY", "_PRIVATE", "db_password", "A1"} {
		if p := EnvNameProblem(ok); p != EnvNameOK {
			t.Errorf("%q refused: %v", ok, p)
		}
	}
	for _, bad := range []string{"", "1ABC", "MY-KEY", "MY KEY", "KEY=1", "KÉY", strings.Repeat("A", MaxEnvName+1)} {
		if p := EnvNameProblem(bad); p != EnvNameInvalid {
			t.Errorf("%q: %v, want invalid", bad, p)
		}
	}
	// What the platform sets itself, or a prefix it owns.
	for _, reserved := range []string{"JAVO_TOKEN", "JAVO_URL", "JAVO_ANYTHING", "PORT", "HOME", "PATH", "BASE_URL", "KUBERNETES_SERVICE_HOST", "LD_PRELOAD"} {
		if p := EnvNameProblem(reserved); p != EnvNameReserved {
			t.Errorf("%q: %v, want reserved", reserved, p)
		}
	}
}

func TestSecretValues(t *testing.T) {
	for _, ok := range []string{"x", "line one\nline two", "-----BEGIN KEY-----\nabc\n-----END KEY-----\n", strings.Repeat("v", MaxSecretValue)} {
		if !ValidSecretValue(ok) {
			t.Errorf("refused a value of %d bytes", len(ok))
		}
	}
	for _, bad := range []string{"", "a\x00b", strings.Repeat("v", MaxSecretValue+1), "\xff\xfe"} {
		if ValidSecretValue(bad) {
			t.Errorf("accepted a bad value of %d bytes", len(bad))
		}
	}
}
