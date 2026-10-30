package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"gite/internal/auth"
	"gite/internal/repository"
)

const reposHelp = `usage: gite repos create <owner>/<name> [--private] [--description D] [--branch main]
       gite repos list [--owner U]
       gite repos delete <owner>/<name> --yes
`

// Repos implements `gite repos`.
func Repos(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, reposHelp)
		return nil
	}
	switch args[0] {
	case "create":
		return reposCreate(args[1:])
	case "list":
		return reposList(args[1:])
	case "delete":
		return reposDelete(args[1:])
	default:
		return fmt.Errorf("unknown repos command %q\n\n%s", args[0], reposHelp)
	}
}

// splitRepoPath splits "owner/name" and validates the shape.
func splitRepoPath(arg string) (owner, name string, err error) {
	i := strings.IndexByte(arg, '/')
	if i <= 0 || i == len(arg)-1 {
		return "", "", fmt.Errorf("repository must be <owner>/<name>, e.g. alice/widget\n\n%s", reposHelp)
	}
	if strings.Contains(arg[i+1:], "/") {
		return "", "", fmt.Errorf("repository must be <owner>/<name>\n\n%s", reposHelp)
	}
	return arg[:i], arg[i+1:], nil
}

func reposCreate(args []string) error {
	fs := flag.NewFlagSet("gite repos create", flag.ExitOnError)
	private := fs.Bool("private", false, "only collaborators may see or clone it")
	description := fs.String("description", "", "short description")
	branch := fs.String("branch", "main", "default branch")

	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	if len(fs.Args()) != 1 {
		return fmt.Errorf("exactly one <owner>/<name> argument is required\n\n%s", reposHelp)
	}
	ownerName, repoName, err := splitRepoPath(fs.Args()[0])
	if err != nil {
		return err
	}
	ctx := context.Background()
	owner, err := auth.GetUserByUsername(ctx, c.store, ownerName)
	if err != nil {
		return err
	}
	store := repository.NewStore(c.store, c.cfg.RepoRoot)
	repo, err := store.Create(ctx, owner, repoName, *description, *private, *branch)
	if err != nil {
		return err
	}
	fmt.Printf("created %s (%s), default branch %s, private=%v\n",
		repo.FullName(), store.Path(repo), repo.DefaultBranch, repo.Private)
	return nil
}

func reposList(args []string) error {
	fs := flag.NewFlagSet("gite repos list", flag.ExitOnError)
	ownerName := fs.String("owner", "", "restrict to one account")
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	store := repository.NewStore(c.store, c.cfg.RepoRoot)
	repos, err := store.List(context.Background(), *ownerName)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return fmt.Errorf("no repositories yet")
	}
	for _, r := range repos {
		vis := "public"
		if r.Private {
			vis = "private"
		}
		desc := r.Description
		if desc != "" {
			desc = "  " + desc
		}
		fmt.Printf("%-30s %-8s %-8s %s%s\n", r.FullName(), vis, r.Backend, r.CreatedAt, desc)
	}
	return nil
}

func reposDelete(args []string) error {
	fs := flag.NewFlagSet("gite repos delete", flag.ExitOnError)
	yes := fs.Bool("yes", false, "confirm deletion, this destroys git history")
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	if len(fs.Args()) != 1 {
		return fmt.Errorf("exactly one <owner>/<name> argument is required\n\n%s", reposHelp)
	}
	if !*yes {
		return fmt.Errorf("this destroys the git history. pass --yes to confirm")
	}
	ownerName, repoName, err := splitRepoPath(fs.Args()[0])
	if err != nil {
		return err
	}
	store := repository.NewStore(c.store, c.cfg.RepoRoot)
	repo, err := store.GetByOwnerName(context.Background(), ownerName, repoName)
	if err != nil {
		return err
	}
	if err := store.Delete(context.Background(), repo); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", repo.FullName())
	return nil
}
