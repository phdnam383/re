// Test-only compatibility seams for the white-box configuration tests. The
// shipping command keeps the corresponding logic inline in main.
package main

import (
	"fmt"
	"time"

	"re/internal/contextbuilder/configuration"
	"re/internal/ruleengine"
)

// Environment variables the engine reads. They are read once at startup and
// never re-read: a process that changed behaviour mid-flight because someone
// edited an environment would be impossible to reason about from a log.
const (
	envDBDSN                = "RE_DB_DSN"
	envGRPCAddr             = "RE_GRPC_ADDR"
	envConfigurationTimeout = "RE_CONFIGURATION_TIMEOUT"
	envRCARuleTimeout       = "RE_RCA_RULE_TIMEOUT"
)

// config is the validated runtime configuration.
type config struct {
	// DSN points at the PostgreSQL holding topology, context profiles and RCA
	// rules. There is no default and no development fallback compiled in: a
	// binary that would connect somewhere on its own is a binary that can
	// connect to the wrong place silently.
	DSN string

	// GRPCAddr is the listen address, for example ":30051".
	GRPCAddr string

	// ConfigurationTimeout bounds one Configuration Provider GET.
	ConfigurationTimeout time.Duration

	// RCARuleTimeout bounds one rca_rule row.
	RCARuleTimeout time.Duration
}

// loadConfig reads and validates the environment.
//
// getenv is injected so the whole contract is testable without touching the
// process environment, which two tests running in parallel would otherwise
// share.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		DSN:      getenv(envDBDSN),
		GRPCAddr: getenv(envGRPCAddr),
	}

	if cfg.DSN == "" {
		return config{}, fmt.Errorf("%s is required", envDBDSN)
	}
	if cfg.GRPCAddr == "" {
		return config{}, fmt.Errorf("%s is required", envGRPCAddr)
	}

	var err error
	if cfg.ConfigurationTimeout, err = duration(getenv, envConfigurationTimeout, configuration.DefaultTimeout); err != nil {
		return config{}, err
	}
	if cfg.RCARuleTimeout, err = duration(getenv, envRCARuleTimeout, ruleengine.DefaultRuleTimeout); err != nil {
		return config{}, err
	}

	return cfg, nil
}

// duration parses an optional time.Duration setting.
//
// A malformed or non-positive value fails startup rather than falling back to
// the default. Both mean the operator tried to set something and got it wrong,
// and an engine that silently ran with a different timeout than the one
// configured would be debugged by reading the wrong number.
func duration(getenv func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	raw := getenv(name)
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration: %w", name, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s: %v must be greater than zero", name, value)
	}
	return value, nil
}
