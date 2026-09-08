// Command charonctl drives the charon API so a calling agent does not have to.
//
// Named charonctl, not charon: charon is the service this talks to, and having
// both answer to one name made it impossible to say which one wrote a file.
//
//	charonctl request [--await] [--timeout 24h]      create a request for the user to fill in
//	charonctl await [--timeout 24h] [--env] HANDLE   wait for it, collect it
//	charonctl get [--to PATH] HANDLE NAME            read one collected value
//	charonctl cleanup [--after 10m] HANDLE           remove the collected values
//	charonctl send                                   hand over a secret you already have
//
// Secret values never reach stdout unless asked for with --env or get. Human
// readable progress goes to stderr, so `eval "$(charonctl await H --env)"` is
// safe and everything else stays legible in a transcript.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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
  charonctl await [--timeout 24h] [--env] [--cleanup-after 10m] HANDLE
  charonctl get [--to PATH] [--mode 0600] HANDLE NAME
  charonctl cleanup [--after 10m] HANDLE
  charonctl send < spec.json

Flags go before positional arguments.

CHARON_API is the server (default http://localhost:1337).
CHARON_SCRATCH is where collected values are kept (default the temp dir).`

// parse accepts flags before or after positional arguments. Go's flag package
// stops at the first positional, which would make `await HANDLE --env` silently
// ignore --env; leading positionals are moved after the flags instead.
func parse(fs *flag.FlagSet, args []string) error {
	var pos, flags []string
	for i, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = args[i:]
			break
		}
		pos = append(pos, a)
	}
	return fs.Parse(append(flags, pos...))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fail(exitError, "%s", usage)
	}
	c := newClient(os.Getenv("CHARON_API"))
	verb, rest := args[0], args[1:]
	switch verb {
	case "request":
		fs := flag.NewFlagSet("request", flag.ContinueOnError)
		fs.SetOutput(stderr)
		wait := fs.Bool("await", false, "wait for the answer after printing the link")
		timeout := fs.Duration("timeout", 24*time.Hour, "how long --await waits")
		cleanup := fs.Duration("cleanup-after", 10*time.Minute, "when --await removes collected values")
		if err := parse(fs, rest); err != nil {
			return err
		}
		return cmdRequest(c, stdin, stdout, stderr, *wait, *timeout, *cleanup)
	case "await":
		fs := flag.NewFlagSet("await", flag.ContinueOnError)
		fs.SetOutput(stderr)
		timeout := fs.Duration("timeout", 24*time.Hour, "give up after this long")
		env := fs.Bool("env", false, "print export lines for eval")
		cleanup := fs.Duration("cleanup-after", 10*time.Minute, "remove collected values after this long")
		if err := parse(fs, rest); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fail(exitError, "await needs exactly one HANDLE")
		}
		return cmdAwait(c, stdout, stderr, fs.Arg(0), *timeout, *env, *cleanup)
	case "get":
		fs := flag.NewFlagSet("get", flag.ContinueOnError)
		fs.SetOutput(stderr)
		to := fs.String("to", "", "write the value to this path instead of stdout")
		mode := fs.String("mode", "0600", "file mode for --to")
		if err := parse(fs, rest); err != nil {
			return err
		}
		if fs.NArg() != 2 {
			return fail(exitError, "get needs HANDLE and NAME")
		}
		return cmdGet(stdout, fs.Arg(0), fs.Arg(1), *to, *mode)
	case "cleanup":
		fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
		fs.SetOutput(stderr)
		after := fs.Duration("after", 0, "wait this long first")
		if err := parse(fs, rest); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fail(exitError, "cleanup needs exactly one HANDLE")
		}
		return cmdCleanup(fs.Arg(0), *after)
	case "send":
		return cmdSend(c, stdin, stdout)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		return fail(exitError, "unknown command %q\n%s", verb, usage)
	}
}
