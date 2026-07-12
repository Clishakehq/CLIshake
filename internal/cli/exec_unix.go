//go:build unix

package cli

import "syscall"

// sysExec replaces the current process image (execve). Used for tmux attach
// so clishake gets out of the way of the interactive session.
func sysExec(bin string, argv []string, env []string) error {
	return syscall.Exec(bin, argv, env)
}
