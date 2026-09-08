//go:build !unix

package main

import (
	"errors"
	"os/exec"
)

func execEnvProcess(cmd *exec.Cmd) error {
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fail(ee.ExitCode(), "%v", err)
	}
	return err
}
