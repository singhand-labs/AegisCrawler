package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/config"
	"github.com/singhand-labs/AegisCrawler/internal/store"
)

// version is overridden at build time with
//
//	go build -ldflags "-X main.version=0.1.0" ./cmd/server
var version = "dev"

const cliUsage = `AegisCrawler Page Agent Service

Usage:
  aegiscrawler [command] [flags]

Commands:
  serve            Start the HTTP server (default when no command is given)
  version          Print the build version and exit
  validate-config  Load and validate configuration, then exit
  migrate          Run database migrations and exit

Flags:
  -c, --config <path>   Config file path (overrides the CONFIG_PATH
                        environment variable; defaults to config.yml in the
                        working directory)

Configuration resolves per key as: environment variable > config.yml >
built-in default. See server/config.example.yml.
`

// cliCommand is one parsed invocation.
type cliCommand struct {
	name       string
	configPath string
}

// parseCommand maps raw arguments to a command. An empty argument list means
// "serve" so historical no-argument invocations keep working.
func parseCommand(args []string) (*cliCommand, error) {
	if len(args) == 0 {
		return &cliCommand{name: "serve"}, nil
	}
	switch args[0] {
	case "-h", "--help", "help":
		return &cliCommand{name: "help"}, nil
	case "-v", "--version", "version":
		return &cliCommand{name: "version"}, nil
	}

	name := args[0]
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("c", "", "config file path (overrides CONFIG_PATH)")
	flags.StringVar(configPath, "config", "", "config file path (overrides CONFIG_PATH)")
	if err := flags.Parse(args[1:]); err != nil {
		return nil, err
	}
	if flags.NArg() > 0 {
		return nil, fmt.Errorf("command %q takes no positional arguments: %s", name, strings.Join(flags.Args(), " "))
	}
	switch name {
	case "serve", "validate-config", "migrate":
		return &cliCommand{name: name, configPath: *configPath}, nil
	default:
		return nil, fmt.Errorf("unknown command %q — run with --help for usage", name)
	}
}

// applyConfigPath lets the -c flag override CONFIG_PATH before config.Load
// runs. Flag beats environment for the file location because it is the most
// explicit operator intent; the value layering inside the file stays
// environment-first.
func applyConfigPath(path string) {
	if strings.TrimSpace(path) != "" {
		_ = os.Setenv("CONFIG_PATH", path)
	}
}

// runCLI executes one command and returns the process exit code.
func runCLI(args []string) int {
	cmd, err := parseCommand(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aegiscrawler:", err)
		return 2
	}
	switch cmd.name {
	case "help":
		fmt.Print(cliUsage)
		return 0
	case "version":
		fmt.Println("aegiscrawler", version)
		return 0
	case "validate-config":
		return runValidateConfig(cmd.configPath)
	case "migrate":
		return runMigrate(cmd.configPath)
	default:
		return runServe(cmd.configPath)
	}
}

// runValidateConfig loads the full configuration exactly as serve would and
// reports every startup-blocking problem. It exists so operators and CI can
// check env + config.yml without binding a port.
func runValidateConfig(configPath string) int {
	applyConfigPath(configPath)
	cfg := config.Load()
	if cfg.ConfigFileErr != nil {
		fmt.Fprintf(os.Stderr, "config file error: %v\n", cfg.ConfigFileErr)
		return 1
	}
	if cfg.ConfigFile != "" {
		fmt.Printf("config file: %s\n", cfg.ConfigFile)
	} else {
		fmt.Println("config file: none (environment-only)")
	}
	if err := validateStartupConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "startup config invalid: %v\n", err)
		return 1
	}
	if missing := missingSecurityKeys(cfg); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing security keys: %s\n", strings.Join(missing, ", "))
		return 1
	}
	fmt.Println("configuration valid")
	return 0
}

// runMigrate opens the store (which applies versioned migrations) and exits.
func runMigrate(configPath string) int {
	applyConfigPath(configPath)
	cfg := config.Load()
	if cfg.ConfigFileErr != nil {
		fmt.Fprintf(os.Stderr, "config file error: %v\n", cfg.ConfigFileErr)
		return 1
	}
	if err := validateStartupConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "startup config invalid: %v\n", err)
		return 1
	}
	if missing := missingSecurityKeys(cfg); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing security keys: %s\n", strings.Join(missing, ", "))
		return 1
	}
	logger, err := newLogger(cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		return 1
	}
	defer func() { _ = logger.Sync() }()
	s, err := store.NewWithConfig(cfg, cfg.DatabasePath, cfg.VariableEncryptionKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		return 1
	}
	if err := s.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: close: %v\n", err)
		return 1
	}
	fmt.Printf("migrations applied to %s\n", cfg.DatabasePath)
	return 0
}
