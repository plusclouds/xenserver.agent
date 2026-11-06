// Package executor provides a safe, audited command execution utility.
// All commands are executed directly via os/exec (never through a shell),
// with full stdout/stderr capture and structured audit logging via zap.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"
)

const defaultTimeout = 30 * time.Second

// Executor runs external commands safely.
type Executor struct {
	logger  *zap.Logger
	timeout time.Duration
	dryRun  bool
}

// New creates an Executor with the default 30-second timeout.
func New(logger *zap.Logger, dryRun bool) *Executor {
	return &Executor{
		logger:  logger,
		timeout: defaultTimeout,
		dryRun:  dryRun,
	}
}

// NewWithTimeout creates an Executor with the specified timeout.
func NewWithTimeout(logger *zap.Logger, timeout time.Duration, dryRun bool) *Executor {
	return &Executor{
		logger:  logger,
		timeout: timeout,
		dryRun:  dryRun,
	}
}

// Execute runs the given command with the provided arguments.
// It applies a deadline to the provided context (the shorter of the context
// deadline and the executor timeout), captures stdout and stderr separately,
// and emits a structured audit log entry on every execution.
//
// The command is always invoked directly — never via sh -c — to prevent
// shell injection.
//
// When the Executor is in dry-run mode, no subprocess is started at all:
// the command is logged and a "[DRY RUN] ..." marker is returned as stdout
// so it's also visible in the operation's result output, not just the logs.
func (e *Executor) Execute(ctx context.Context, command string, args ...string) (stdout string, stderr string, err error) {
	return e.ExecuteTimeout(ctx, e.timeout, command, args...)
}

// ExecuteTimeout is Execute with an explicit timeout in place of the
// executor's default. Use it for operations that legitimately run long, such
// as VM exports and imports.
func (e *Executor) ExecuteTimeout(ctx context.Context, timeout time.Duration, command string, args ...string) (stdout string, stderr string, err error) {
	if e.dryRun {
		e.logger.Info("dry run: skipping command execution",
			zap.String("command", command),
			zap.Strings("args", RedactArgs(args)),
		)
		cmdLine := command
		if len(args) > 0 {
			cmdLine += " " + strings.Join(RedactArgs(args), " ")
		}
		return "[DRY RUN] " + cmdLine, "", nil
	}

	// Apply timeout: if the context already has a shorter deadline, that wins.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	// If the context expires, kill the command and give up on its pipes after a
	// few seconds. Without this, a child that outlives the killed process (e.g.
	// mount.nfs retrying an unreachable server) keeps the pipes open and Run
	// blocks long past the timeout.
	cmd.WaitDelay = 5 * time.Second

	release := configureChild(cmd)
	defer release()

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	// Structured audit log — emitted for every execution regardless of outcome.
	e.logger.Info("command executed",
		zap.String("command", command),
		zap.Strings("args", RedactArgs(args)),
		zap.Duration("duration", duration),
		zap.Int("exit_code", exitCode),
		zap.Bool("success", runErr == nil),
	)

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout, stderr, fmt.Errorf("command timed out after %s: %s", timeout, command)
		}
		return stdout, stderr, fmt.Errorf("command failed (exit %d): %w", exitCode, runErr)
	}

	return stdout, stderr, nil
}
