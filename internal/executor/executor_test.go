package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestExecute_RunsCommandNormally(t *testing.T) {
	e := New(zap.NewNop(), false)
	stdout, _, err := e.Execute(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello\n")
	}
}

func TestExecute_DryRunSkipsExecution(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	// A command that, if actually run, would create the marker file.
	e := New(zap.NewNop(), true)
	stdout, stderr, err := e.Execute(context.Background(), "touch", marker)
	if err != nil {
		t.Fatalf("dry run should never return an error, got: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if want := "[DRY RUN] touch " + marker; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("dry run must not actually execute the command — marker file was created")
	}
}

func TestExecute_DryRunWithNoArgs(t *testing.T) {
	e := New(zap.NewNop(), true)
	stdout, _, err := e.Execute(context.Background(), "somecommand")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stdout != "[DRY RUN] somecommand" {
		t.Errorf("stdout = %q, want %q", stdout, "[DRY RUN] somecommand")
	}
}

func TestExecuteTimeout_EnforcedEvenWhenChildHoldsPipe(t *testing.T) {
	e := NewWithTimeout(zap.NewNop(), 200*time.Millisecond, false)
	start := time.Now()
	// The backgrounded sleep inherits stdout/stderr and outlives the shell.
	_, _, err := e.Execute(context.Background(), "sh", "-c", "sleep 20 & wait")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timed out error, got %v", err)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("timeout not enforced: returned after %v (child kept the pipes open)", d)
	}
}

func TestExecuteTimeout_OverridesDefault(t *testing.T) {
	// Default timeout of 100ms would kill a 1s sleep; an explicit longer one must not.
	e := NewWithTimeout(zap.NewNop(), 100*time.Millisecond, false)
	if _, _, err := e.Execute(context.Background(), "sleep", "1"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("default timeout: want timed out error, got %v", err)
	}
	if _, _, err := e.ExecuteTimeout(context.Background(), 5*time.Second, "sleep", "1"); err != nil {
		t.Fatalf("explicit longer timeout must succeed, got %v", err)
	}
	if _, _, err := e.ExecuteTimeout(context.Background(), 100*time.Millisecond, "sleep", "1"); err == nil || !strings.Contains(err.Error(), "timed out after 100ms") {
		t.Fatalf("explicit short timeout: want 'timed out after 100ms', got %v", err)
	}
}
