package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"gite/internal/auth"
)

const adminHelp = `usage: gite admin create-user --username U --email E --password P [--admin]
       gite admin list-users
`

// Admin implements `gite admin`.
func Admin(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, adminHelp)
		return nil
	}
	switch args[0] {
	case "create-user":
		return adminCreateUser(args[1:])
	case "list-users":
		return adminListUsers(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q\n\n%s", args[0], adminHelp)
	}
}

func adminCreateUser(args []string) error {
	fs := flag.NewFlagSet("gite admin create-user", flag.ExitOnError)
	username := fs.String("username", "", "login name (required)")
	email := fs.String("email", "", "email address (required)")
	password := fs.String("password", "", "password, 8+ characters (required)")
	admin := fs.Bool("admin", false, "grant site administrator powers")

	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	if *username == "" || *email == "" || *password == "" {
		return fmt.Errorf("--username, --email and --password are required\n\n%s", adminHelp)
	}
	u, err := auth.CreateUser(context.Background(), c.store, *username, *email, *password, *admin)
	if err != nil {
		return err
	}
	fmt.Printf("created user %s (id=%d, email=%s, admin=%v)\n", u.Username, u.ID, u.Email, u.IsAdmin)
	return nil
}

func adminListUsers(args []string) error {
	fs := flag.NewFlagSet("gite admin list-users", flag.ExitOnError)
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	users, err := auth.ListUsers(context.Background(), c.store)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("no users yet")
		return nil
	}
	for _, u := range users {
		fmt.Printf("%-20s %-32s admin=%-5v created=%s\n", u.Username, u.Email, u.IsAdmin, u.CreatedAt)
	}
	return nil
}
