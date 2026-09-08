//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the cleanup process in its own session so a terminal hangup
// that ends the agent's shell does not take the timer with it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
