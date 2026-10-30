package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gite/internal/auth"
)

const keysHelp = `usage: gite keys add --user U --title T [--key <path-or-material>]
                      (omit --key or pass - to read the key from stdin)
       gite keys list [--user U]
       gite keys remove --fingerprint SHA256:... (gite keys list shows it)
`

// Keys implements `gite keys`.
func Keys(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, keysHelp)
		return nil
	}
	switch args[0] {
	case "add":
		return keysAdd(args[1:])
	case "list":
		return keysList(args[1:])
	case "remove":
		return keysRemove(args[1:])
	default:
		return fmt.Errorf("unknown keys command %q\n\n%s", args[0], keysHelp)
	}
}

// looksLikeKeyMaterial guesses whether the --key value is a key line or a path.
func looksLikeKeyMaterial(s string) bool {
	for _, prefix := range []string{"ssh-", "ecdsa-", "sk-ssh-", "sk-ecdsa-", "@cert-authority "} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func keysAdd(args []string) error {
	fs := flag.NewFlagSet("gite keys add", flag.ExitOnError)
	userName := fs.String("user", "", "account username (required)")
	title := fs.String("title", "", "short label, e.g. laptop (required)")
	keyArg := fs.String("key", "", "public key material, or a path to a .pub file (omit or - for stdin)")

	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	if *userName == "" || *title == "" {
		return fmt.Errorf("--user and --title are required\n\n%s", keysHelp)
	}
	var data []byte
	switch {
	case *keyArg == "" || *keyArg == "-":
		if data, err = io.ReadAll(os.Stdin); err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	case looksLikeKeyMaterial(*keyArg):
		data = []byte(*keyArg)
	default:
		if data, err = os.ReadFile(*keyArg); err != nil {
			return fmt.Errorf("read key file: %w", err)
		}
	}

	user, err := auth.GetUserByUsername(context.Background(), c.store, *userName)
	if err != nil {
		return err
	}
	k, err := auth.AddKey(context.Background(), c.store, user, *title, data)
	if err != nil {
		return err
	}
	fmt.Printf("added key id=%d fingerprint=%s title=%q for %s\n", k.ID, k.Fingerprint, k.Title, user.Username)
	return nil
}

func keysList(args []string) error {
	fs := flag.NewFlagSet("gite keys list", flag.ExitOnError)
	userName := fs.String("user", "", "restrict to one account")
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	ctx := context.Background()
	if *userName != "" {
		user, err := auth.GetUserByUsername(ctx, c.store, *userName)
		if err != nil {
			return err
		}
		keys, err := auth.ListKeys(ctx, c.store, user)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return fmt.Errorf("no keys for %s", user.Username)
		}
		for _, k := range keys {
			printKey(k)
		}
		return nil
	}
	users, err := auth.ListUsers(ctx, c.store)
	if err != nil {
		return err
	}
	found := false
	for _, user := range users {
		keys, err := auth.ListKeys(ctx, c.store, user)
		if err != nil {
			return err
		}
		for _, k := range keys {
			found = true
			printKey(k)
		}
	}
	if !found {
		return fmt.Errorf("no ssh keys registered yet")
	}
	return nil
}

func printKey(k *auth.SSHKey) {
	fmt.Printf("%-16s %-20s %s %q\n", k.Username, k.Title, k.Fingerprint, k.Comment)
}

func keysRemove(args []string) error {
	fs := flag.NewFlagSet("gite keys remove", flag.ExitOnError)
	fingerprint := fs.String("fingerprint", "", "fingerprint of the key to remove (required)")
	c, err := loadEnv(fs, args, true)
	if err != nil {
		return err
	}
	defer c.store.Close()
	if *fingerprint == "" {
		return fmt.Errorf("--fingerprint is required\n\n%s", keysHelp)
	}
	if err := auth.RemoveKey(context.Background(), c.store, *fingerprint); err != nil {
		return err
	}
	fmt.Printf("removed key %s\n", *fingerprint)
	return nil
}
