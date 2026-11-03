package model

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Secret is one of a person's named secrets. Its value is
// write-only: it is stored sealed (package secrets) and never part of this
// record, of any API answer, log line, audit row or export.
type Secret struct {
	ID string
	// Owner is the person's name, as on apps. Only the owner sees, changes
	// or uses the secret.
	Owner string
	// Name follows the rules of an environment variable name, so it is also
	// the default variable an app gets it as. It never changes.
	Name string
	Note string
	// Version counts value changes; apps that use the secret restart when it
	// moves. Re-sealing the same value under a new key does not move it.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SecretBinding is one secret an app gets, as the environment variable Env.
// Secret is the secret's name (its owner is the app's owner).
type SecretBinding struct {
	Env    string
	Secret string
}

// GitTokenEnv is the key under which an app's repository token is bound to
// one of the owner's secrets, beside its variables. It is not
// a valid variable name, so no variable can take it.
const GitTokenEnv = "@git-token"

// Limits on secrets and variables. A secret's value is at most 16 KiB and an
// app or a job has at most 50 variables, so one app's values stay well under
// the 1 MiB a Kubernetes Secret holds. A plain value is at most 4 KiB.
const (
	MaxSecretValue     = 16 << 10
	MaxSecretNote      = 200
	MaxEnvName         = 100
	MaxEnvVars         = 50
	MaxEnvValue        = 4 << 10
	MaxSecretsPerOwner = 200
)

// EnvNameCheck is the outcome of EnvNameProblem.
type EnvNameCheck int

const (
	EnvNameOK EnvNameCheck = iota
	// EnvNameInvalid is not a portable environment variable name.
	EnvNameInvalid
	// EnvNameReserved is a name the platform sets or owns.
	EnvNameReserved
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnv are the variables the platform sets in an app's container
// (workspace.BuildPod, podspec, podstorage, the API token and Git source
// wiring; a test there keeps this list complete) and the ones that would
// change how every program in it starts. The colour defaults
// (podspec.ColorEnv) are not: an app or job may set its own.
var reservedEnv = []string{
	"HOME", "PATH", "PORT", "BASE_URL", "USER", "HOSTNAME", "TZ", "CI",
	"XDG_CACHE_HOME", "UV_CACHE_DIR", "PIP_CACHE_DIR", "PIXI_CACHE_DIR", "CONDA_PKGS_DIRS", "CONDA_ENVS_PATH",
	"LD_PRELOAD", "LD_LIBRARY_PATH", "npm_config_cache",
}

// reservedEnvPrefixes are owned whole: JAVO_ by the platform (JAVO_URL,
// JAVO_TOKEN, JAVO_USER, ...), KUBERNETES_ by Kubernetes' service variables.
var reservedEnvPrefixes = []string{ProductEnvPrefix, "KUBERNETES_"}

// ProductEnvPrefix starts every variable the platform sets for its own use.
const ProductEnvPrefix = "JAVO_"

// EnvNameProblem says whether name may be the variable a secret becomes:
// letters, digits and underscores, not starting with a digit, at most
// MaxEnvName long, and none the platform sets.
func EnvNameProblem(name string) EnvNameCheck {
	if len(name) > MaxEnvName || !envName.MatchString(name) {
		return EnvNameInvalid
	}
	if slices.Contains(reservedEnv, name) {
		return EnvNameReserved
	}
	for _, p := range reservedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return EnvNameReserved
		}
	}
	return EnvNameOK
}

// ValidSecretValue is 1 to MaxSecretValue bytes of UTF-8 without a NUL (an
// environment variable cannot hold one).
func ValidSecretValue(v string) bool {
	return v != "" && len(v) <= MaxSecretValue && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}

// ValidEnvValue is a plain value of at most MaxEnvValue bytes of UTF-8
// without a NUL; it may be empty.
func ValidEnvValue(v string) bool {
	return len(v) <= MaxEnvValue && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}
