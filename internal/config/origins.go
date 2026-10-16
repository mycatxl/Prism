package config

import (
	"os"
	"strings"
	"sync"
)

// EnvSource says where the effective value of a setting came from.
//
// It exists for `prism check-config`, which prints the value that is actually
// in force together with the place it was taken from: an operator reading the
// report must be able to tell a shipped default from an override without
// re-reading the environment, the .env file and the compatibility layer in
// their head.
type EnvSource string

const (
	// EnvSourceDefault is a variable that carries nothing: the shipped default
	// (or, for the hot-updatable fields, the persisted runtime config) applies.
	EnvSourceDefault EnvSource = "default"
	// EnvSourceEnv is a PRISM_<X> variable that carries a value.
	EnvSourceEnv EnvSource = "env"
	// EnvSourceLegacy is the deprecated upstream spelling RESIN_<X> being used
	// because PRISM_<X> is unset. The loader logs a deprecation warning for it.
	EnvSourceLegacy EnvSource = "legacy"
	// EnvSourceEmpty is a variable that is set but empty. The loader reads that
	// as "not configured": the default applies, and for the alert settings the
	// persisted value stays in place.
	EnvSourceEmpty EnvSource = "empty"
)

// String renders the source the way check-config prints it.
func (s EnvSource) String() string {
	switch s {
	case EnvSourceEnv, EnvSourceLegacy, EnvSourceEmpty:
		return string(s)
	default:
		return string(EnvSourceDefault)
	}
}

// envOrigins records where the last load resolved each variable from. It is
// written by the load itself (lookupEnv, envBool, LoadQualityConfig), so the
// report can never disagree with the values it is printed next to.
var (
	envOriginMu sync.Mutex
	envOrigins  = map[string]EnvSource{}
)

// resetEnvOrigins drops the record so the next load reflects only the process
// environment as it is now. A second load in the same process (tests, and
// `prism check-config` after `prism run` in one shell) would otherwise keep
// reporting the first load's origins.
func resetEnvOrigins() {
	envOriginMu.Lock()
	envOrigins = map[string]EnvSource{}
	envOriginMu.Unlock()
}

// recordEnvOrigin stores where name was resolved from. The last read wins,
// which is the one the effective configuration was built from.
func recordEnvOrigin(name string, source EnvSource) {
	envOriginMu.Lock()
	envOrigins[name] = source
	envOriginMu.Unlock()
}

// envSourceForValue classifies a variable that was found. An empty value is
// reported as "set but empty" rather than as an override, because that is what
// it is: every reader in this package answers the default for it.
func envSourceForValue(value string, legacy bool) EnvSource {
	if strings.TrimSpace(value) == "" {
		return EnvSourceEmpty
	}
	if legacy {
		return EnvSourceLegacy
	}
	return EnvSourceEnv
}

// EnvSourceOf reports where the effective value of a PRISM_<X> variable came
// from during the last LoadEnvConfig.
//
// A variable the loader did not read - a switch that is only consulted when it
// carries a value, such as the usage-alert settings - falls back to the live
// environment, classified the same way the loader would have classified it.
func EnvSourceOf(name string) EnvSource {
	envOriginMu.Lock()
	source, ok := envOrigins[name]
	envOriginMu.Unlock()
	if ok {
		return source
	}
	return classifyEnv(name)
}

// classifyEnv reads name the way lookupEnv would and reports what a load would
// have concluded from it, without logging the deprecation warning: this is the
// read-only path used for a variable no load step touched.
func classifyEnv(name string) EnvSource {
	if value, ok := os.LookupEnv(name); ok {
		return envSourceForValue(value, false)
	}
	legacy := legacyEnvName(name)
	if legacy == "" {
		return EnvSourceDefault
	}
	if value, ok := os.LookupEnv(legacy); ok {
		return envSourceForValue(value, true)
	}
	return EnvSourceDefault
}

// EnvSettingSet reports whether an environment variable carries a value for
// this process, resolving it exactly like the loader does: PRISM_<X> first, the
// deprecated RESIN_<X> spelling as the fallback.
//
// Unlike os.Getenv(name) it therefore also sees a setting pinned through the
// legacy spelling, which is the difference between "applied" and "validated and
// then silently dropped" for a variable that is read through the compatibility
// layer. A variable that is unset, and one set to the empty string, both answer
// false: a deployment file with a bare `PRISM_X=` line must not pin a setting
// to nothing on every restart.
func EnvSettingSet(name string) bool {
	value, ok := lookupEnv(name)
	return ok && strings.TrimSpace(value) != ""
}
