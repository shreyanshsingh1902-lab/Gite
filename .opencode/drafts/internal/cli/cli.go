// Package cli implements the management commands of the gite binary.
package cli

import (
	"flag"
	"fmt"

	"gite/internal/config"
	"gite/internal/db"
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
