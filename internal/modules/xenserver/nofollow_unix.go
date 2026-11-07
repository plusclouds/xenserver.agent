//go:build !windows

package xenserver

import "syscall"

// oNoFollow makes open fail instead of following a symlink at the final path
// component.
const oNoFollow = syscall.O_NOFOLLOW
