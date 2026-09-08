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
  charonctl await [--timeout 24h] [--env] [--cleanup-after 10m] --retrieve-handle RETRIEVE_HANDLE
  charonctl get [--to PATH] [--mode 0600] --retrieve-handle RETRIEVE_HANDLE --name NAME
  charonctl cleanup [--after 10m] --retrieve-handle RETRIEVE_HANDLE
  charonctl exec-env --retrieve-handle RETRIEVE_HANDLE [--name NAME ...] -- COMMAND [ARG ...]
  charonctl send < spec.json
  charonctl status --manage-handle MANAGE_HANDLE
  charonctl status --retrieve-handle RETRIEVE_HANDLE
  charonctl destroy --manage-handle MANAGE_HANDLE

request and send read JSON from stdin. Other operands are named flags.

--retrieve-handle is the collect token (await, get, cleanup, exec-env).
--manage-handle is the owner token (destroy, status). status accepts either.

destroy deletes the exchange on the server.
cleanup deletes the local receipt.

Server: CHARON_API overrides the optional JSON config's "api" field.
Config: $XDG_CONFIG_HOME/charonctl/config.json (default ~/.config/charonctl/config.json).
Without either, the server is http://localhost:1337.
CHARON_SCRATCH_DIR is where collected values are kept (default the user cache).`

var commandUsage = map[string]string{
	"request": `usage: charonctl request [--await] [--timeout 24h] [--cleanup-after 10m] < spec.json

Create a request. Stdin is the same JSON as POST /api/requests.
Names must be shell identifiers. Default ttl is 1d. Default linger is 0
(gone on first collect); set "linger":"60s" when a person will copy in a
browser or Mini App.

Prints TITLE, LINK (hand this out), EXPIRES, RETRIEVE_HANDLE, MANAGE_HANDLE.
--await continues into await. --timeout and --cleanup-after require --await.`,
	"await": `usage: charonctl await [--timeout 24h] [--env] [--cleanup-after 10m] --retrieve-handle RETRIEVE_HANDLE

Wait until the request is filled, collect once, keep a local receipt.
Later calls reuse the receipt. Values go to stderr never stdout, unless --env.`,
	"get": `usage: charonctl get [--to PATH] [--mode 0600] --retrieve-handle RETRIEVE_HANDLE --name NAME

Read one collected secret. Exit 3 if the user left it blank.`,
	"cleanup": `usage: charonctl cleanup [--after 10m] --retrieve-handle RETRIEVE_HANDLE

Delete the local receipt. Does not destroy the server entry.`,
	"exec-env": `usage: charonctl exec-env --retrieve-handle RETRIEVE_HANDLE [--name NAME ...] -- COMMAND [ARG ...]

Run COMMAND with collected secrets in its environment. Repeat --name to
allowlist; omit it to select all. Files become NAME_FILE.`,
	"send": `usage: charonctl send < spec.json

Create, fill, and submit a send.

{
  "title": "Generated deploy key",
  "description": "optional markdown",
  "ttl": "1d",
  "linger": "0s",
  "secrets": [
    {"name": "TOKEN", "text": "inline-value"},
    {"name": "TOKEN", "env": "API_TOKEN"},
    {"name": "KEY", "file": "/path/to/key.pem"},
    {"name": "CERTS", "files": ["/a.pem", "/b.pem"]},
    {"name": "BLOB", "text_file": "/path/to/blob"}
  ]
}

Each secret needs exactly one of:
  text        inline string
  env         environment variable name
  file        one path, uploaded as a file
  files       list of paths, uploaded onto one secret
  text_file   path whose contents become text, verbatim

Optional "type" ("text" or "file") must match the source; it is not a source.
Default ttl is 1d. Default linger is 0 (gone on first read); set "60s" when
a person will copy from a browser or Mini App.

Prints TITLE, LINK (hand this out), EXPIRES, MANAGE_HANDLE.
A fill or submit failure destroys the draft.`,
	"status": `usage: charonctl status --manage-handle MANAGE_HANDLE
       charonctl status --retrieve-handle RETRIEVE_HANDLE

Print TITLE, KIND, expiry, fulfilled, retrieved.
A manage handle also reprints LINK and RETRIEVE_HANDLE.`,
	"destroy": `usage: charonctl destroy --manage-handle MANAGE_HANDLE

Delete the exchange on the server. Existing share links stop working.
Does not delete a local receipt (use cleanup for that).`,
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fail(exitError, "%s", usage)
	}
	verb, rest := args[0], args[1:]
	if verb == "-h" || verb == "--help" || verb == "help" {
		fmt.Fprintln(stdout, usage)
		return nil
	}
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	if u, ok := commandUsage[verb]; ok {
		fs.Usage = func() {
			fmt.Fprintln(stderr, u)
			fs.PrintDefaults()
		}
	}
	var retrieveHandle, manageHandle, name, to, mode, from string
	var names []string
	var wait, env bool
	var timeout, cleanup, after time.Duration
	switch verb {
	case "request":
		fs.BoolVar(&wait, "await", false, "wait for the answer after printing the link")
		fs.DurationVar(&timeout, "timeout", 24*time.Hour, "how long --await waits")
		fs.DurationVar(&cleanup, "cleanup-after", 10*time.Minute, "when --await removes collected values")
	case "await":
		fs.StringVar(&retrieveHandle, "retrieve-handle", "", "retrieve handle (required)")
		fs.DurationVar(&timeout, "timeout", 24*time.Hour, "give up after this long")
		fs.BoolVar(&env, "env", false, "print shell-quoted export statements containing secrets; capture stdout for eval")
		fs.DurationVar(&cleanup, "cleanup-after", 10*time.Minute, "remove collected values after this long")
	case "get":
		fs.StringVar(&retrieveHandle, "retrieve-handle", "", "retrieve handle (required)")
		fs.StringVar(&name, "name", "", "secret name (required)")
		fs.StringVar(&to, "to", "", "write the value to this path instead of stdout")
		fs.StringVar(&mode, "mode", "0600", "file mode for --to")
	case "cleanup":
		fs.StringVar(&retrieveHandle, "retrieve-handle", "", "retrieve handle")
		fs.StringVar(&from, "from", "", "file holding the handle, used by scheduled cleanup")
		fs.DurationVar(&after, "after", 0, "wait this long first")
	case "exec-env":
		fs.StringVar(&retrieveHandle, "retrieve-handle", "", "retrieve handle (required)")
		fs.Func("name", "allow this secret (repeatable; default all)", func(value string) error {
			if value == "" {
				return fmt.Errorf("--name must not be empty")
			}
			names = append(names, value)
			return nil
		})
	case "send":
	case "status":
		fs.StringVar(&retrieveHandle, "retrieve-handle", "", "retrieve handle")
		fs.StringVar(&manageHandle, "manage-handle", "", "manage handle")
	case "destroy":
		fs.StringVar(&manageHandle, "manage-handle", "", "manage handle (required)")
	default:
		return fail(exitError, "unknown command %q\n%s", verb, usage)
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		if looksLikeOldHandleFlag(err) {
			return fail(exitError, "--handle was renamed to --retrieve-handle or --manage-handle")
		}
		return err
	}
	if verb != "exec-env" && fs.NArg() != 0 {
		return fail(exitError, "%s accepts named flags only, no positional arguments", verb)
	}
	if verb == "request" && !wait {
		for _, name := range []string{"timeout", "cleanup-after"} {
			if flagPassed(fs, name) {
				return fail(exitError, "--%s requires --await", name)
			}
		}
	}
	switch verb {
	case "cleanup":
		if retrieveHandle == "" && from == "" {
			return fail(exitError, "cleanup requires --retrieve-handle")
		}
	case "await", "get", "exec-env":
		if retrieveHandle == "" {
			return fail(exitError, "%s requires --retrieve-handle", verb)
		}
	case "destroy":
		if manageHandle == "" {
			return fail(exitError, "destroy requires --manage-handle")
		}
	case "status":
		if (retrieveHandle == "") == (manageHandle == "") {
			return fail(exitError, "status requires exactly one of --retrieve-handle or --manage-handle")
		}
	}
	if verb == "get" && name == "" {
		return fail(exitError, "get requires --name")
	}
	if verb == "exec-env" && fs.NArg() == 0 {
		return fail(exitError, "exec-env requires a command after --")
	}
	var c *client
	if verb == "request" || verb == "await" || verb == "send" || verb == "destroy" || verb == "status" {
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
		return cmdAwait(c, stdout, stderr, retrieveHandle, timeout, env, cleanup)
	case "get":
		return cmdGet(stdout, retrieveHandle, name, to, mode)
	case "cleanup":
		return cmdCleanup(retrieveHandle, from, after)
	case "exec-env":
		return cmdExecEnv(retrieveHandle, names, fs.Args())
	case "send":
		return cmdSend(c, stdin, stdout)
	case "status":
		handle, want := retrieveHandle, "retrieve"
		if manageHandle != "" {
			handle, want = manageHandle, "manage"
		}
		return cmdStatus(c, stdout, handle, want)
	case "destroy":
		return cmdDestroy(c, manageHandle)
	}
	return nil
}

func flagPassed(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func looksLikeOldHandleFlag(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "-handle") &&
		!strings.Contains(msg, "retrieve-handle") &&
		!strings.Contains(msg, "manage-handle")
}
