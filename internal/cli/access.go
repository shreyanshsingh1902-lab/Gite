package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"gite/internal/auth"
	"gite/internal/repository"
)

const accessHelp = `usage: gite access grant <owner>/<name> <user> <read|write|admin>
       gite access revoke <owner>/<name> <user>
       gite access list <owner>/<name>
`

// Access implements `gite access`.
func Access(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, accessHelp)
		return nil
	}
	switch args[0] {
	case "grant":
		return accessGrant(args[1:])
	case "revoke":
		return accessRevoke(args[1:])
	case "list":
		return accessList(args[1:])
	default:
		return fmt.Errorf("unknown access command %q\n\n%s", args[0], accessHelp)
	}
}

// loadRepo loads the repository named "<owner>/<name>".
func loadRepo(c *core, slug string) (*repository.Repo, error) {
	ownerName, repoName, err := splitRepoPath(slug)
	if err != nil {
		return nil, err
	}
	store := repository.NewStore(c.store, c.cfg.RepoRoot)
	return store.GetByOwnerName(context.Background(), ownerName, repoName)
}

func accessGrant(args []string) error {
	fs := flag.NewFlagSet("gite access grant", flag.ExitOnError)
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	rest := fs.Args()
	if len(rest) != 3 {
		return fmt.Errorf("three arguments required: <owner>/<name> <user> <read|write|admin>\n\n%s", accessHelp)
	}
	repo, err := loadRepo(c, rest[0])
	if err != nil {
		return err
	}
	user, err := auth.GetUserByUsername(context.Background(), c.store, rest[1])
	if err != nil {
		return err
	}
	role := rest[2]
	if _, err := auth.ParseRole(role); err != nil {
		return err
	}
	if err := auth.Grant(context.Background(), c.store, repo.ID, user.ID, role); err != nil {
		return err
	}
	fmt.Printf("granted %s role %s on %s\n", user.Username, role, repo.FullName())
	fmt.Printf("(owner %s keeps full access regardless)\n", repo.OwnerUsername)
	return nil
}

func accessRevoke(args []string) error {
	fs := flag.NewFlagSet("gite access revoke", flag.ExitOnError)
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("two arguments required: <owner>/<name> <user>\n\n%s", accessHelp)
	}
	repo, err := loadRepo(c, rest[0])
	if err != nil {
		return err
	}
	user, err := auth.GetUserByUsername(context.Background(), c.store, rest[1])
	if err != nil {
		return err
	}
	if err := auth.Revoke(context.Background(), c.store, repo.ID, user.ID); err != nil {
		return err
	}
	fmt.Printf("revoked %s's access on %s (owner keeps full access)\n", user.Username, repo.FullName())
	return nil
}

func accessList(args []string) error {
	fs := flag.NewFlagSet("gite access list", flag.ExitOnError)
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("one argument required: <owner>/<name>\n\n%s", accessHelp)
	}
	repo, err := loadRepo(c, rest[0])
	if err != nil {
		return err
	}
	collabs, err := auth.ListCollabs(context.Background(), c.store, repo.ID)
	if err != nil {
		return err
	}
	fmt.Printf("%s: owner %s (admin)\n", repo.FullName(), repo.OwnerUsername)
	for _, col := range collabs {
		fmt.Printf("%-20s %s\n", col.Username, col.Role)
	}
	return nil
}
