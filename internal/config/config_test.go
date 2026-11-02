package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvHome(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("GITE_HOME", temp)
	if got := EnvHome(); got != temp {
		t.Fatalf("EnvHome() = %q, want %q", got, temp)
	}
}

func TestLoadDefaults(t *testing.T) {
	temp := t.TempDir()
	cfg, err := Load(temp)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Home != temp {
		t.Errorf("cfg.Home = %q, want %q", cfg.Home, temp)
	}
	if want := filepath.Join(temp, "gite.db"); cfg.DBPath != want {
		t.Errorf("cfg.DBPath = %q, want %q", cfg.DBPath, want)
	}
	if want := filepath.Join(temp, "repos"); cfg.RepoRoot != want {
		t.Errorf("cfg.RepoRoot = %q, want %q", cfg.RepoRoot, want)
	}
	if cfg.SSHAddr != "127.0.0.1:2222" {
		t.Errorf("cfg.SSHAddr = %q, want default 127.0.0.1:2222", cfg.SSHAddr)
	}
	if cfg.HTTPAddr != "127.0.0.1:3000" {
		t.Errorf("cfg.HTTPAddr = %q, want default 127.0.0.1:3000", cfg.HTTPAddr)
	}
}

func TestParseFlags(t *testing.T) {
	temp := t.TempDir()
	args := []string{"-home", temp, "-addr", "0.0.0.0:2229", "-log-level", "debug", "serve", "extra"}

	cfg, rest, err := ParseFlags(args)
	if err != nil {
		t.Fatalf("ParseFlags() error: %v", err)
	}

	if cfg.Home != temp {
		t.Errorf("cfg.Home = %q, want %q", cfg.Home, temp)
	}
	if cfg.SSHAddr != "0.0.0.0:2229" {
		t.Errorf("cfg.SSHAddr = %q, want 0.0.0.0:2229", cfg.SSHAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("cfg.LogLevel = %q, want debug", cfg.LogLevel)
	}
	if len(rest) != 2 || rest[0] != "serve" || rest[1] != "extra" {
		t.Errorf("remaining args = %v, want [serve extra]", rest)
	}
}

func TestEnsure(t *testing.T) {
	temp := filepath.Join(t.TempDir(), "sub", "dir")
	cfg, err := Load(temp)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if err := cfg.Ensure(); err != nil {
		t.Fatalf("Ensure() error: %v", err)
	}

	if info, err := os.Stat(cfg.RepoRoot); err != nil || !info.IsDir() {
		t.Errorf("RepoRoot %q was not created", cfg.RepoRoot)
	}
}

func TestRepoPath(t *testing.T) {
	temp := t.TempDir()
	cfg, _ := Load(temp)
	path := cfg.RepoPath("Alice", "MyRepo")
	want := filepath.Join(temp, "repos", "alice", "myrepo.git")
	if path != want {
		t.Errorf("RepoPath = %q, want %q", path, want)
	}
}
