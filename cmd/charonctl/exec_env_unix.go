//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// Replace the wrapper so terminal I/O, signals, and exit status belong to the command.
func execEnvProcess(cmd *exec.Cmd) error {
	return syscall.Exec(cmd.Path, cmd.Args, cmd.Environ())
}
