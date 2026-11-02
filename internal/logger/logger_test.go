package logger

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLoggerLevelsAndFormat(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithOutput(&buf, "debug", "json")

	log.Debug("test debug message", "key", "val")
	output := buf.String()

	if !strings.Contains(output, `"level":"DEBUG"`) {
		t.Errorf("expected DEBUG level in JSON output, got: %s", output)
	}
	if !strings.Contains(output, `"msg":"test debug message"`) {
		t.Errorf("expected message in JSON output, got: %s", output)
	}
}

func TestLogAuthEvent(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithOutput(&buf, "info", "text")

	LogAuthEvent(log, true, "alice", "ssh-key", "127.0.0.1:54321", "")
	LogAuthEvent(log, false, "", "ssh-key", "192.168.1.100:12345", "unknown key fingerprint")

	output := buf.String()
	if !strings.Contains(output, "authentication succeeded") {
		t.Errorf("missing success log in: %s", output)
	}
	if !strings.Contains(output, "authentication failed") {
		t.Errorf("missing failure log in: %s", output)
	}
	if !strings.Contains(output, "unknown key fingerprint") {
		t.Errorf("missing reason in failure log: %s", output)
	}
}

func TestLogGitCommand(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithOutput(&buf, "info", "text")

	LogGitCommand(log, "alice", "alice/widget", "git-receive-pack", 0, 150*time.Millisecond, nil)

	output := buf.String()
	if !strings.Contains(output, "git command completed") {
		t.Errorf("missing git completion log in: %s", output)
	}
	if !strings.Contains(output, "user=alice") {
		t.Errorf("missing user attr in: %s", output)
	}
}
