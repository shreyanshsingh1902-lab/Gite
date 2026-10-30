// Package git shells out to the native git binary.
//
// gite never reimplements git internals. Every storage-level operation
// goes through this exec wrapper, which is the whole point: repositories
// here are ordinary bare repos, readable by any git tool.
package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Binary returns the path of the git executable.
func Binary() (string, error) {
	p, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git not found in PATH: %w", err)
	}
	return p, nil
}

// Run executes `git <args...>` and returns its (trimmed) combined output.
func Run(ctx context.Context, args ...string) (string, error) {
	return RunIn(ctx, "", args...)
}

// RunIn executes `git <args...>` with working directory dir.
func RunIn(ctx context.Context, dir string, args ...string) (string, error) {
	bin, err := Binary()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(string(out), "\n"), nil
}
