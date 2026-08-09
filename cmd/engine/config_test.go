package main

import (
	"strings"
	"testing"
	"time"

	"re/internal/contextbuilder/configuration"
	"re/internal/ruleengine"
)

// env builds a getenv function over a fixed map. The whole configuration
// contract is tested this way rather than through t.Setenv, because the
// process environment is shared and two parallel tests editing it would see
// each other's values.
func env(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

func minimalEnv() map[string]string {
	return map[string]string{
		envDBDSN:    "postgres://user:pass@localhost:5432/re?sslmode=disable",
		envGRPCAddr: ":30051",
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(minimalEnv()))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if cfg.DSN == "" || cfg.GRPCAddr != ":30051" {
		t.Errorf("config = %+v", cfg)
	}
	// The defaults are the modules' own, not a second set of numbers that
	// could drift from them.
	if cfg.ConfigurationTimeout != configuration.DefaultTimeout {
		t.Errorf("configuration timeout = %v, want %v", cfg.ConfigurationTimeout, configuration.DefaultTimeout)
	}
	if cfg.RCARuleTimeout != ruleengine.DefaultRuleTimeout {
		t.Errorf("rca rule timeout = %v, want %v", cfg.RCARuleTimeout, ruleengine.DefaultRuleTimeout)
	}
}

func TestLoadConfigRequiredVariables(t *testing.T) {
	tests := []struct {
		name    string
		remove  string
		wantErr string
	}{
		{name: "no dsn", remove: envDBDSN, wantErr: envDBDSN},
		{name: "no address", remove: envGRPCAddr, wantErr: envGRPCAddr},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := minimalEnv()
			delete(values, tc.remove)

			_, err := loadConfig(env(values))
			if err == nil {
				t.Fatalf("loadConfig = nil, want an error naming %s", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to name %s", err, tc.wantErr)
			}
		})
	}
}

func TestLoadConfigDurations(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		want    time.Duration
		wantErr string
	}{
		{name: "configuration timeout set", key: envConfigurationTimeout, value: "5s", want: 5 * time.Second},
		{name: "rca timeout set", key: envRCARuleTimeout, value: "1500ms", want: 1500 * time.Millisecond},
		{name: "empty falls back", key: envConfigurationTimeout, value: "", want: configuration.DefaultTimeout},
		{
			// An operator who tried to set something and got it wrong must not
			// end up debugging a timeout that is silently the default.
			name: "not a duration", key: envConfigurationTimeout, value: "5",
			wantErr: "is not a duration",
		},
		{name: "nonsense", key: envRCARuleTimeout, value: "soon", wantErr: "is not a duration"},
		{name: "zero", key: envRCARuleTimeout, value: "0s", wantErr: "greater than zero"},
		{name: "negative", key: envConfigurationTimeout, value: "-2s", wantErr: "greater than zero"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			values := minimalEnv()
			values[tc.key] = tc.value

			cfg, err := loadConfig(env(values))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("loadConfig = nil, want %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.key) {
					t.Errorf("error = %q, want it to name %s", err, tc.key)
				}
				return
			}

			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			got := cfg.ConfigurationTimeout
			if tc.key == envRCARuleTimeout {
				got = cfg.RCARuleTimeout
			}
			if got != tc.want {
				t.Errorf("%s = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestLoadConfigHasNoBuiltInDSN(t *testing.T) {
	// A binary that would connect somewhere on its own is a binary that can
	// connect to the wrong place silently.
	if _, err := loadConfig(func(string) string { return "" }); err == nil {
		t.Fatal("loadConfig on an empty environment = nil, want an error")
	}
}
