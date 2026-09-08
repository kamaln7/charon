// Command charonctl drives the charon API so a calling agent does not have to.
//
// Named charonctl, not charon: charon is the service this talks to, and having
// both answer to one name made it impossible to say which one wrote a file.
//
// Secret values reach stdout only through await --env or get without --to.
// Progress goes to stderr. Capture --env output before evaluating it in a shell.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// Exit codes. Scripts branch on these; keep them stable.
const (
	exitOK      = 0
	exitError   = 1
	exitTimeout = 2 // the request expired, timed out, or was already collected
	exitBlank   = 3 // get: the user left that field empty
)

type exitCode struct {
	code int
	msg  string
}

func (e exitCode) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return exitCode{code: code, msg: fmt.Sprintf(format, args...)}
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "charonctl:", err)
		var ee exitCode
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(exitError)
	}
}

const usage = `usage:
  charonctl request [--await] [--timeout 24h] [--cleanup-after 10m] < spec.json
  charonctl await [--timeout 24h] [--env] [--cleanup-after 10m] --handle HANDLE
  charonctl get [--to PATH] [--mode 0600] --handle HANDLE --name NAME
  charonctl cleanup [--after 10m] --handle HANDLE
  charonctl exec-env --handle HANDLE [--name NAME ...] -- COMMAND [ARG ...]
  charonctl send < spec.json

All operands use named flags; request and send read JSON from stdin.

Server: CHARON_API overrides the optional JSON config's "api" field.
Config: $XDG_CONFIG_HOME/charonctl/config.json (default ~/.config/charonctl/config.json).
Without either, the server is http://localhost:1337.
CHARON_SCRATCH is where collected values are kept (default the user cache).`

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fail(exitError, "%s", usage)
	}
	verb, rest := args[0], args[1:]
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var handle, name, to, mode string
	var names []string
	var wait, env bool
	var timeout, cleanup, after time.Duration
	switch verb {
	case "request":
		fs.BoolVar(&wait, "await", false, "wait for the answer after printing the link")
		fs.DurationVar(&timeout, "timeout", 24*time.Hour, "how long --await waits")
		fs.DurationVar(&cleanup, "cleanup-after", 10*time.Minute, "when --await removes collected values")
	case "await":
		fs.StringVar(&handle, "handle", "", "request handle (required)")
		fs.DurationVar(&timeout, "timeout", 24*time.Hour, "give up after this long")
		fs.BoolVar(&env, "env", false, "print shell-quoted export statements containing secrets; capture stdout for eval")
		fs.DurationVar(&cleanup, "cleanup-after", 10*time.Minute, "remove collected values after this long")
	case "get":
		fs.StringVar(&handle, "handle", "", "request handle (required)")
		fs.StringVar(&name, "name", "", "secret name (required)")
		fs.StringVar(&to, "to", "", "write the value to this path instead of stdout")
		fs.StringVar(&mode, "mode", "0600", "file mode for --to")
	case "cleanup":
		fs.StringVar(&handle, "handle", "", "request handle (required)")
		fs.DurationVar(&after, "after", 0, "wait this long first")
	case "exec-env":
		fs.StringVar(&handle, "handle", "", "request handle (required)")
		fs.Func("name", "allow this secret (repeatable; default all)", func(value string) error {
			if value == "" {
				return fmt.Errorf("--name must not be empty")
			}
			names = append(names, value)
			return nil
		})
	case "send":
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		return fail(exitError, "unknown command %q\n%s", verb, usage)
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if verb != "exec-env" && fs.NArg() != 0 {
		return fail(exitError, "%s accepts named flags only, no positional arguments", verb)
	}
	if verb == "await" || verb == "get" || verb == "cleanup" || verb == "exec-env" {
		if handle == "" {
			return fail(exitError, "%s requires --handle", verb)
		}
	}
	if verb == "get" && name == "" {
		return fail(exitError, "get requires --name")
	}
	if verb == "exec-env" && fs.NArg() == 0 {
		return fail(exitError, "exec-env requires a command after --")
	}
	var c *client
	if verb == "request" || verb == "await" || verb == "send" {
		base, err := serverConfig()
		if err != nil {
			return err
		}
		c = newClient(base)
	}
	switch verb {
	case "request":
		return cmdRequest(c, stdin, stdout, stderr, wait, timeout, cleanup)
	case "await":
		return cmdAwait(c, stdout, stderr, handle, timeout, env, cleanup)
	case "get":
		return cmdGet(stdout, handle, name, to, mode)
	case "cleanup":
		return cmdCleanup(handle, after)
	case "exec-env":
		return cmdExecEnv(handle, names, fs.Args())
	case "send":
		return cmdSend(c, stdin, stdout)
	}
	return nil
}
