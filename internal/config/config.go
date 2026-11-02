// Package config resolves the gite data directory and its runtime settings.
//
// One home directory = one forge instance. It holds the sqlite database,
// the bare repositories, and the ssh host key.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config contains resolved paths and runtime settings for one gite instance.
type Config struct {
	Home      string // Root data directory ($GITE_HOME or ~/.gite)
	DBPath    string // SQLite database file (<Home>/gite.db)
	RepoRoot  string // Bare repos root (<Home>/repos/<owner>/<name>.git)
	SSHAddr   string // Listen address for git-over-ssh (default: 127.0.0.1:2222)
	HTTPAddr  string // Listen address for web and smart HTTP (default: 127.0.0.1:3000)
	LogLevel  string // Structured logging level: debug, info, warn, error (default: info)
	LogFormat string // Structured logging format: text or json (default: text)
}

// EnvHome returns the default data directory: $GITE_HOME or ~/.gite.
func EnvHome() string {
	if h := strings.TrimSpace(os.Getenv("GITE_HOME")); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gite"
	}
	return filepath.Join(home, ".gite")
}

// Load resolves all paths from a home directory value, applying environment variables.
func Load(home string) (*Config, error) {
	if home == "" {
		home = EnvHome()
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve home %q: %w", home, err)
	}

	c := &Config{
		Home:      abs,
		DBPath:    filepath.Join(abs, "gite.db"),
		RepoRoot:  filepath.Join(abs, "repos"),
		SSHAddr:   "127.0.0.1:2222",
		HTTPAddr:  "127.0.0.1:3000",
		LogLevel:  "info",
		LogFormat: "text",
	}

	// Environment variable overrides
	if a := strings.TrimSpace(os.Getenv("GITE_SSH_ADDR")); a != "" {
		c.SSHAddr = a
	}
	if a := strings.TrimSpace(os.Getenv("GITE_HTTP_ADDR")); a != "" {
		c.HTTPAddr = a
	}
	if l := strings.TrimSpace(os.Getenv("GITE_LOG_LEVEL")); l != "" {
		c.LogLevel = strings.ToLower(l)
	}
	if f := strings.TrimSpace(os.Getenv("GITE_LOG_FORMAT")); f != "" {
		c.LogFormat = strings.ToLower(f)
	}

	return c, nil
}

// ParseFlags parses standard global configuration flags (-home, -addr, -log-level).
func ParseFlags(args []string) (*Config, []string, error) {
	fs := flag.NewFlagSet("gite", flag.ContinueOnError)
	var (
		homeFlag     = fs.String("home", "", "data directory (default: $GITE_HOME or ~/.gite)")
		sshAddrFlag  = fs.String("addr", "", "SSH listen address (default: 127.0.0.1:2222)")
		httpAddrFlag = fs.String("http", "", "HTTP listen address (default: 127.0.0.1:3000)")
		logLevelFlag = fs.String("log-level", "", "logging level: debug, info, warn, error")
		logFmtFlag   = fs.String("log-format", "", "logging format: text or json")
	)

	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}

	cfg, err := Load(*homeFlag)
	if err != nil {
		return nil, nil, err
	}

	if *sshAddrFlag != "" {
		cfg.SSHAddr = *sshAddrFlag
	}
	if *httpAddrFlag != "" {
		cfg.HTTPAddr = *httpAddrFlag
	}
	if *logLevelFlag != "" {
		cfg.LogLevel = strings.ToLower(*logLevelFlag)
	}
	if *logFmtFlag != "" {
		cfg.LogFormat = strings.ToLower(*logFmtFlag)
	}

	return cfg, fs.Args(), nil
}

// Ensure creates the home directory and the repository root.
func (c *Config) Ensure() error {
	for _, dir := range []string{c.Home, c.RepoRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}

// RepoPath returns the canonical bare repository path for a given owner and repository name.
func (c *Config) RepoPath(owner, name string) string {
	return filepath.Join(c.RepoRoot, strings.ToLower(owner), strings.ToLower(name)+".git")
}

// HostKeyPath returns the path to the SSH host Ed25519 private key.
func (c *Config) HostKeyPath() string {
	return filepath.Join(c.Home, "ssh_host_ed25519_key")
}
