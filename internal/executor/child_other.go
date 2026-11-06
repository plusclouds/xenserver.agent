//go:build !linux

package executor

import "os/exec"

// configureChild is a no-op where parent-death signals are unavailable.
func configureChild(*exec.Cmd) (release func()) { return func() {} }
