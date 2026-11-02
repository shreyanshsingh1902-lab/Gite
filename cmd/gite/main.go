// Command gite is the forge server and its management CLI.
//
//	gite serve      run the git transport (SSH now; web UI arrives in phase 1)
//	gite migrate    create or upgrade the database
//	gite admin      user management
//	gite keys       ssh key management
//	gite repos      repository management
//	gite access     per-repository permission management
package main

import (
	"fmt"
	"os"

	"gite/internal/cli"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "gite: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "help", "-h", "--help":
		usage()
		return nil
	case "version", "-v", "--version":
		fmt.Println("gite 0.1.0 (phase 0: forge floor)")
		return nil
	case "migrate":
		return cli.Migrate(rest)
	case "serve":
		return cli.Serve(rest)
	case "admin":
		return cli.Admin(rest)
	case "keys":
		return cli.Keys(rest)
	case "repos":
		return cli.Repos(rest)
	case "access":
		return cli.Access(rest)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: gite <command>

commands:
  serve             run the forge (git over ssh; web ui arrives in phase 1)
  migrate           create or upgrade the sqlite schema
  admin             manage users        gite admin create-user|list-users
  keys              manage ssh keys     gite keys add|list|remove
  repos             manage repos        gite repos create|list|delete
  access            manage repo roles   gite access grant|revoke|list
  help, version

every command accepts -home <dir> (defaults to $GITE_HOME or ~/.gite)
`)
}
