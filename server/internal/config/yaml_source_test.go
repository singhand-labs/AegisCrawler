package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", path)
	return path
}

// The file layer must supply values the environment does not set, while the
// environment keeps priority over the file for the same key.
func TestConfigFileSuppliesValuesAndEnvironmentWins(t *testing.T) {
	path := writeConfigFile(t, `
LISTEN_ADDR: 127.0.0.1:9999
DATABASE_PATH: /tmp/from-yml.db
MAX_RETRIES: 7
LEASE_DURATION: 90s
RATE_LIMIT_PER_SECOND: 42.5
FEATURE_WORKFLOW_V2: true
`)
	t.Setenv("LISTEN_ADDR", "127.0.0.1:7777")

	cfg := Load()

	if cfg.ConfigFile != path {
		t.Fatalf("ConfigFile = %q, want %q", cfg.ConfigFile, path)
	}
	if cfg.ConfigFileErr != nil {
		t.Fatalf("ConfigFileErr = %v, want nil", cfg.ConfigFileErr)
	}
	if cfg.ListenAddr != "127.0.0.1:7777" {
		t.Fatalf("ListenAddr = %q, environment must win over config.yml", cfg.ListenAddr)
	}
	if cfg.DatabasePath != "/tmp/from-yml.db" {
		t.Fatalf("DatabasePath = %q, want the config.yml value", cfg.DatabasePath)
	}
	if cfg.MaxRetries != 7 {
		t.Fatalf("MaxRetries = %d, want 7 from config.yml", cfg.MaxRetries)
	}
	if cfg.LeaseDuration != 90*time.Second {
		t.Fatalf("LeaseDuration = %v, want 90s from config.yml", cfg.LeaseDuration)
	}
	if cfg.RateLimitPerSecond != 42.5 {
		t.Fatalf("RateLimitPerSecond = %v, want 42.5 from config.yml", cfg.RateLimitPerSecond)
	}
	if !cfg.WorkflowV2Enabled {
		t.Fatal("WorkflowV2Enabled = false, want true from config.yml")
	}
}

// A missing file must be indistinguishable from the historical behavior:
// environment plus defaults only, no error.
func TestMissingConfigFileFallsBackToEnvironmentOnly(t *testing.T) {
	t.Setenv("CONFIG_PATH", filepath.Join(t.TempDir(), "does-not-exist.yml"))
	t.Setenv("DATABASE_PATH", "/tmp/env-only.db")

	cfg := Load()

	if cfg.ConfigFile != "" {
		t.Fatalf("ConfigFile = %q, want empty for a missing file", cfg.ConfigFile)
	}
	if cfg.ConfigFileErr != nil {
		t.Fatalf("ConfigFileErr = %v, want nil for a missing file", cfg.ConfigFileErr)
	}
	if cfg.DatabasePath != "/tmp/env-only.db" {
		t.Fatalf("DatabasePath = %q, want the environment value", cfg.DatabasePath)
	}
	if cfg.MaxRetries != 3 {
		t.Fatalf("MaxRetries = %d, want the built-in default", cfg.MaxRetries)
	}
}

// A file that exists but cannot be parsed must fail closed: the error is
// reported on the Config so startup can refuse to run on guesses.
func TestMalformedConfigFileReportsError(t *testing.T) {
	writeConfigFile(t, "\tLISTEN_ADDR: [unclosed")

	cfg := Load()

	if cfg.ConfigFileErr == nil {
		t.Fatal("ConfigFileErr = nil, want a parse error for malformed YAML")
	}
}

// The default path is config.yml in the working directory.
func TestDefaultConfigPathIsWorkingDirectoryFile(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.yml", []byte("LISTEN_ADDR: 127.0.0.1:6666\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Load()

	if cfg.ListenAddr != "127.0.0.1:6666" {
		t.Fatalf("ListenAddr = %q, want the config.yml value from the working directory", cfg.ListenAddr)
	}
	if cfg.ConfigFile != "config.yml" {
		t.Fatalf("ConfigFile = %q, want the relative default path", cfg.ConfigFile)
	}
}

// YAML scalars render to the exact string forms the typed helpers parse,
// structured values encode as JSON, and null keys are ignored.
func TestYAMLValueToStringCoercion(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "60s", "60s"},
		{"bool", true, "true"},
		{"int", 8080, "8080"},
		{"whole float", 8080.0, "8080"},
		{"fractional float", 0.2, "0.2"},
		{"array", []any{"a", "b"}, `["a","b"]`},
		{"map", map[string]any{"k": "v"}, `{"k":"v"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := yamlValueToString(tc.in)
			if err != nil {
				t.Fatalf("yamlValueToString(%v) error = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("yamlValueToString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A null key in the file must not shadow anything and must not error.
func TestNullYAMLKeysAreIgnored(t *testing.T) {
	writeConfigFile(t, "DATABASE_PATH: /tmp/null-test.db\nEMPTY_KEY: null\n")

	cfg := Load()

	if cfg.ConfigFileErr != nil {
		t.Fatalf("ConfigFileErr = %v, want nil", cfg.ConfigFileErr)
	}
	if cfg.DatabasePath != "/tmp/null-test.db" {
		t.Fatalf("DatabasePath = %q, want the file value", cfg.DatabasePath)
	}
	if v, ok := lookupSetting("EMPTY_KEY"); ok || v != "" {
		t.Fatalf("lookupSetting(EMPTY_KEY) = (%q, %v), want unset", v, ok)
	}
}

// The LLM policy loader must also see the file layer so enforced-mode
// settings can live in config.yml.
func TestLLMPolicyModeReadableFromConfigFile(t *testing.T) {
	writeConfigFile(t, "LLM_POLICY_MODE: enforced\n")
	cfg := Load()
	if cfg.llmPolicyErr != nil {
		t.Fatalf("llmPolicyErr = %v, want nil (mode alone is valid)", cfg.llmPolicyErr)
	}
	if cfg.llmPolicy == nil || cfg.llmPolicy.Mode != LLMPolicyModeEnforced {
		t.Fatalf("llm policy mode not read from config.yml: %+v", cfg.llmPolicy)
	}
}
