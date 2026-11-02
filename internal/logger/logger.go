// Package logger configures structured logging using Go's standard log/slog.
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// New initializes and returns a structured logger based on level and format preferences.
func New(levelStr, format string) *slog.Logger {
	return NewWithOutput(os.Stderr, levelStr, format)
}

// NewWithOutput initializes a structured logger with a custom io.Writer (useful for testing).
func NewWithOutput(w io.Writer, levelStr, format string) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if strings.ToLower(strings.TrimSpace(format)) == "json" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}

// LogAuthEvent logs authentication successes or failures in a consistent schema.
func LogAuthEvent(log *slog.Logger, success bool, user, method, remote, reason string) {
	if success {
		log.Info("authentication succeeded",
			slog.String("user", user),
			slog.String("method", method),
			slog.String("remote", remote),
		)
	} else {
		log.Warn("authentication failed",
			slog.String("user", user),
			slog.String("method", method),
			slog.String("remote", remote),
			slog.String("reason", reason),
		)
	}
}

// LogGitCommand logs the execution of a git transport verb (upload-pack or receive-pack).
func LogGitCommand(log *slog.Logger, user, repo, verb string, exitCode int, duration time.Duration, err error) {
	attrs := []any{
		slog.String("user", user),
		slog.String("repo", repo),
		slog.String("verb", verb),
		slog.Int("exit_code", exitCode),
		slog.Duration("duration", duration),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
		log.Error("git command completed with error", attrs...)
	} else {
		log.Info("git command completed", attrs...)
	}
}
