package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The new knob must be readable from config.yml and usable in enforced mode.
func TestLLMCallMaxRetriesFromConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("LLM_CALL_MAX_RETRIES: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_PATH", path)
	cfg := Load()
	if cfg.LLMCallMaxRetries != 5 {
		t.Fatalf("LLMCallMaxRetries = %d, want 5 from config.yml", cfg.LLMCallMaxRetries)
	}
}

// Legacy deployments that only set LLM_MAX_RETRIES keep that value as the
// call-retry default; the new knob wins when both are set.
func TestLLMCallMaxRetriesLegacyMigration(t *testing.T) {
	t.Setenv("LLM_MAX_RETRIES", "7")
	if cfg := Load(); cfg.LLMCallMaxRetries != 7 {
		t.Fatalf("LLMCallMaxRetries = %d, want the legacy LLM_MAX_RETRIES value 7", cfg.LLMCallMaxRetries)
	}
	t.Setenv("LLM_CALL_MAX_RETRIES", "3")
	if cfg := Load(); cfg.LLMCallMaxRetries != 3 {
		t.Fatalf("LLMCallMaxRetries = %d, want the explicit new knob value 3", cfg.LLMCallMaxRetries)
	}
}

// Values above the fail-closed ceiling fall back to the default instead of
// silently allowing an unbounded retry storm.
func TestLLMCallMaxRetriesCeiling(t *testing.T) {
	t.Setenv("LLM_CALL_MAX_RETRIES", "11")
	if cfg := Load(); cfg.LLMCallMaxRetries != 2 {
		t.Fatalf("LLMCallMaxRetries = %d, want the default 2 for an out-of-range value", cfg.LLMCallMaxRetries)
	}
}
