//go:build linux

package executor

import (
	"os/exec"
	"runtime"
	"syscall"
)

// configureChild makes the child die with the agent. Without this, a crash or
// SIGKILL of the agent leaves the xe process it started running, and a long
// export keeps writing a file nobody is tracking any more. Pdeathsig is tied to
// the OS thread that starts the child, so the thread is pinned for the call.
// The returned func releases the pin.
func configureChild(cmd *exec.Cmd) (release func()) {
	runtime.LockOSThread()
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return runtime.UnlockOSThread
}
