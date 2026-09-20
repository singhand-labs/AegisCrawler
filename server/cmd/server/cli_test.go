package main

import (
	"os"
	"testing"
)

func TestParseCommandDefaultsToServe(t *testing.T) {
	cmd, err := parseCommand(nil)
	if err != nil {
		t.Fatalf("parseCommand(nil) error = %v", err)
	}
	if cmd.name != "serve" {
		t.Fatalf("no arguments must default to serve, got %q", cmd.name)
	}
}

func TestParseCommandHelpAndVersionAliases(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		cmd, err := parseCommand([]string{arg})
		if err != nil || cmd.name != "help" {
			t.Fatalf("parseCommand(%q) = (%q, %v), want help", arg, cmdName(cmd), err)
		}
	}
	for _, arg := range []string{"-v", "--version", "version"} {
		cmd, err := parseCommand([]string{arg})
		if err != nil || cmd.name != "version" {
			t.Fatalf("parseCommand(%q) = (%q, %v), want version", arg, cmdName(cmd), err)
		}
	}
}

func TestParseCommandConfigFlagForms(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"serve", "-c", "/etc/aegis.yml"}, "/etc/aegis.yml"},
		{[]string{"serve", "--config", "/etc/aegis.yml"}, "/etc/aegis.yml"},
		{[]string{"validate-config", "-c", "local.yml"}, "local.yml"},
		{[]string{"migrate", "--config=x.yml"}, "x.yml"},
		{[]string{"serve"}, ""},
	}
	for _, tc := range cases {
		cmd, err := parseCommand(tc.args)
		if err != nil {
			t.Fatalf("parseCommand(%v) error = %v", tc.args, err)
		}
		if cmd.configPath != tc.want {
			t.Fatalf("parseCommand(%v).configPath = %q, want %q", tc.args, cmd.configPath, tc.want)
		}
	}
}

func TestParseCommandRejectsUnknownAndPositional(t *testing.T) {
	if _, err := parseCommand([]string{"frobnicate"}); err == nil {
		t.Fatal("unknown command must be rejected")
	}
	if _, err := parseCommand([]string{"serve", "extra"}); err == nil {
		t.Fatal("positional arguments must be rejected")
	}
}

// The flag path must be applied to CONFIG_PATH so config.Load honors it
// without any further plumbing.
func TestApplyConfigPathOverridesEnv(t *testing.T) {
	t.Setenv("CONFIG_PATH", "/from/env.yml")
	applyConfigPath("/from/flag.yml")
	if got := os.Getenv("CONFIG_PATH"); got != "/from/flag.yml" {
		t.Fatalf("CONFIG_PATH = %q, want the -c flag value", got)
	}
	t.Setenv("CONFIG_PATH", "/from/env.yml")
	applyConfigPath("  ")
	if got := os.Getenv("CONFIG_PATH"); got != "/from/env.yml" {
		t.Fatalf("blank -c must keep the environment value, got %q", got)
	}
}

func cmdName(cmd *cliCommand) string {
	if cmd == nil {
		return ""
	}
	return cmd.name
}
