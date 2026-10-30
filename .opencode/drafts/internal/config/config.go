// Package config resolves the gite data directory and its paths.
//
// One home directory = one forge instance. It holds the sqlite database,
// the bare repositories and the ssh host key.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// Config contains resolved paths and listen settings for one data directory.
type Config struct {
	Home     string // data directory
	DBPath   string // sqlite database file
	RepoRoot string // bare repos: <repo-root>/<owner>/<name>.git
	SSHAddr  string // listen address for git-over-ssh
}

// EnvHome returns the default data directory: $GITE_HOME or ~/.gite.
func EnvHome() string {
	if h := os.Getenv("GITE_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gite"
	}
	return filepath.Join(home, ".gite")
}

// Load resolves all paths from a home directory value.
func Load(home string) (*Config, error) {
	if home == "" {
		home = EnvHome()
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve home %q: %w", home, err)
	}
	c := &Config{
		Home:     abs,
		DBPath:   filepath.Join(abs, "gite.db"),
		RepoRoot: filepath.Join(abs, "repos"),
		SSHAddr:  "127.0.0.1:2222",
	}
	if a := os.Getenv("GITE_SSH_ADDR"); a != "" {
		c.SSHAddr = a
	}
	return c, nil
}

// Ensure creates the home directory and the repository root.
func (c *Config) Ensure() error {
	for _, dir := range []string{c.Home, c.RepoRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}
