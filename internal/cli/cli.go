// Package cli implements the management commands of the gite binary.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"gite/internal/config"
	"gite/internal/db"
	"gite/internal/gateway"
	"gite/internal/logger"
)

// core bundles config and database access shared by management commands.
type core struct {
	cfg   config.Config
	store *db.DB
}

// loadEnv registers the shared -home flag, parses args, resolves config
// and opens (and optionally migrates) the database.
func loadEnv(fs *flag.FlagSet, args []string, migrate bool) (*core, error) {
	fs.String("home", config.EnvHome(), "gite data directory (defaults to $GITE_HOME or ~/.gite)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	home := fs.Lookup("home").Value.String()
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	if err := cfg.Ensure(); err != nil {
		return nil, err
	}
	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if migrate {
		if err := conn.Migrate(); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return &core{cfg: *cfg, store: conn}, nil
}

// Migrate implements `gite migrate`.
func Migrate(args []string) error {
	fs := flag.NewFlagSet("gite migrate", flag.ExitOnError)
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	fmt.Printf("database ready: %s\n", c.cfg.DBPath)
	return nil
}

// Serve implements `gite serve` with graceful shutdown and structured logging.
func Serve(args []string) error {
	fs := flag.NewFlagSet("gite serve", flag.ExitOnError)
	homeFlag := fs.String("home", config.EnvHome(), "gite data directory (defaults to $GITE_HOME or ~/.gite)")
	addrFlag := fs.String("addr", "", "SSH listen address (defaults to 127.0.0.1:2222 or $GITE_SSH_ADDR)")
	logLevelFlag := fs.String("log-level", "", "logging level: debug, info, warn, error")
	logFmtFlag := fs.String("log-format", "", "logging format: text or json")

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*homeFlag)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if *addrFlag != "" {
		cfg.SSHAddr = *addrFlag
	}
	if *logLevelFlag != "" {
		cfg.LogLevel = *logLevelFlag
	}
	if *logFmtFlag != "" {
		cfg.LogFormat = *logFmtFlag
	}

	if err := cfg.Ensure(); err != nil {
		return fmt.Errorf("ensure paths: %w", err)
	}

	log := logger.New(cfg.LogLevel, cfg.LogFormat)
	log.Info("initializing gite forge",
		"home", cfg.Home,
		"ssh_addr", cfg.SSHAddr,
		"db", cfg.DBPath,
		"log_level", cfg.LogLevel,
	)

	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer conn.Close()

	if err := conn.Migrate(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	srv, err := gateway.NewSSHServer(*cfg, conn, log)
	if err != nil {
		return fmt.Errorf("init ssh server: %w", err)
	}

	// Trap SIGINT and SIGTERM for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.Info("gite server online", "ssh_listen", cfg.SSHAddr)
	if err := srv.ListenAndServe(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("ssh server error: %w", err)
	}

	log.Info("gite server shutdown complete")
	return nil
}
