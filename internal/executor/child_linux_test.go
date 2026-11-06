//go:build linux

package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap"
)

// A helper process runs an Executor command and is then SIGKILLed; the command
// it started must die with it (no orphaned export writing to disk).
func TestChildDiesWhenAgentIsKilled(t *testing.T) {
	if os.Getenv("EXECUTOR_HELPER") == "1" {
		e := New(zap.NewNop(), false)
		_, _, _ = e.Execute(context.Background(), "sleep", "300")
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestChildDiesWhenAgentIsKilled")
	cmd.Env = append(os.Environ(), "EXECUTOR_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = dir
	var childPID int
	for i := 0; i < 50 && childPID == 0; i++ {
		time.Sleep(100 * time.Millisecond)
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(cmd.Process.Pid), "sleep").Output()
		if f := strings.Fields(string(out)); len(f) > 0 {
			childPID, _ = strconv.Atoi(f[0])
		}
	}
	if childPID == 0 {
		t.Fatal("helper never started its child")
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_, _ = cmd.Process.Wait()
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(childPID))); err != nil {
			return // child is gone
		}
		if b, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(childPID), "stat")); strings.Contains(string(b), ") Z ") {
			return // zombie awaiting reaping still counts as dead
		}
	}
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Fatal("child outlived the killed parent")
}
