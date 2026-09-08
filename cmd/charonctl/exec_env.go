package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/kamaln7/charon/internal/api"
)

func cmdExecEnv(handle string, names, args []string) error {
	r, err := loadReceipt(handle)
	if errors.Is(err, os.ErrNotExist) {
		return fail(exitTimeout, "nothing collected for %s; run await first", handle)
	}
	if err != nil {
		return err
	}
	values, err := r.execEnvironment(names)
	if err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	if cmd.Err != nil {
		return cmd.Err
	}
	cmd.Env = os.Environ()
	for name, value := range values {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return execEnvProcess(cmd)
}

// The allowlist names receipt fields, before the _FILE suffix is added.
// Resolve everything before starting the command; never run with partial credentials.
func (r *receipt) execEnvironment(names []string) (map[string]string, error) {
	selected := make(map[string]bool, len(names))
	for _, name := range names {
		selected[name] = false
	}
	values := make(map[string]string)
	for i, sec := range r.Secrets {
		if _, ok := selected[sec.Name]; len(names) > 0 && !ok {
			continue
		}
		selected[sec.Name] = true
		if !identifier.MatchString(sec.Name) {
			return nil, fmt.Errorf("secret name %q is not a shell identifier", sec.Name)
		}
		if sec.Blank {
			return nil, fail(exitBlank, "%s was left blank", sec.Name)
		}
		name := sec.Name
		var value string
		if sec.Type == api.TypeFile {
			name += "_FILE"
			value = r.valuePath(i)
			if _, err := os.Stat(value); err != nil {
				return nil, err
			}
		} else {
			b, err := os.ReadFile(r.valuePath(i))
			if err != nil {
				return nil, err
			}
			value = string(b)
		}
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("secret %q contains NUL, which environment variables cannot represent; use get --to", sec.Name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("multiple secrets map to environment variable %q", name)
		}
		values[name] = value
	}
	for _, name := range names {
		if !selected[name] {
			return nil, fmt.Errorf("no secret named %q", name)
		}
	}
	return values, nil
}
