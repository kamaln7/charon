---
name: charon-secrets
description: Request a secret from the user, or hand one over, via charon instead of pasting it in chat.
---

# Charon secret exchange

Use `charonctl` directly; do not call the HTTP API or write a replacement script.
Commands use named flags; `request` and `send` read JSON from stdin.

## 1. Choose the flow

- **Need a secret from the user:** follow steps **2 → 3 → 4**.
- **Have a secret to give the user:** go to **step 5**.
- **Need long-term storage:** use your configured secret manager. Charon is
  only for short-lived exchange.

Never expose secret values in chat, tool arguments, logs, or commits. Do not
use shell tracing (`set -x`) when consuming secrets.

## 2. Create the request

Create the request as soon as the task needs it; never ask for secrets in chat.
Names must be shell identifiers. Use the default exchange TTL of `1d`.
Leave `linger` unset (default `0`); charonctl collects once into a receipt.

```bash
echo '{
  "title": "Deploy credentials",
  "secrets": [
    {"name": "API_TOKEN", "description": "token for the deployment"},
    {"name": "DEPLOY_KEY", "description": "private key", "type": "file"}
  ]
}' | charonctl request --await --timeout 24h
```

Start the command using the applicable harness row in step 3.

## 3. Share the link and await collection

Output starts with `TITLE`, `LINK`, `EXPIRES`, `RETRIEVE_HANDLE`, and
`MANAGE_HANDLE`. Give the user `LINK` as plain text immediately, mention
`TITLE` so they can confirm the page, retain `RETRIEVE_HANDLE` for
retrieval, and continue independent work. Let `charonctl` handle long-polling.
If the user cancels, `charonctl destroy --manage-handle MANAGE_HANDLE`.

| Harness | Execution and completion |
| --- | --- |
| Claude Code | Bash with `run_in_background: true` and `timeout: 600000` (10 minutes). Read initial output for the link, then use the task completion notification. |
| Codex (`exec_command`) | Set `yield_time_ms: 1000`; retain `session_id`. Read output/completion with `write_stdin`, `chars: ""`, and `yield_time_ms: 1000` when resuming work that needs the secret. Do not assume completion notifications. |
| No persistent sessions | Run `request` without `--await`, share the link, then run `charonctl await --timeout 30s --retrieve-handle RETRIEVE_HANDLE` when resuming. If stderr says timed out waiting, reuse the handle on the next attempt; do not create a duplicate request. |

If the 10-minute Claude tool timeout ends the waiter before submission, or
any harness kills it, resume with `charonctl await --retrieve-handle RETRIEVE_HANDLE` using the
same harness settings. Reuse the existing handle; do not create another request.
Successful collection reports `set NAME`, `file NAME`, or `blank NAME` without
values. If a required field is blank, request it again; exclude optional blank
fields from the `exec-env` allowlist.

## 4. Consume the collected secrets

Choose one path:

- **Run a command with secrets:** use **4a — `exec-env`**.
- **Load secrets into the current shell:** use **4b — `--env`**.
- **Save a secret to a file:** use **4c — `get --to`**.

Both environment modes map text to `NAME` and files to `NAME_FILE`
containing the receipt path.

### 4a. Run a command

```bash
charonctl exec-env --retrieve-handle RETRIEVE_HANDLE --name API_TOKEN --name DEPLOY_KEY -- your-command --its-flags
```

Repeat `--name` to allowlist receipt field names; omit it to select all.
Selected values override inherited variables; other inherited variables remain.
Unknown or blank selected fields fail before the command starts.

### 4b. Load the current shell

`await --env` prints shell-quoted export statements **containing secrets**;
it does not modify the parent shell. Capture and evaluate them in the same
Bash/zsh invocation that consumes them. Never print the captured output.
Blank fields are omitted: unset requested variables first to avoid stale values.

```bash
unset API_TOKEN DEPLOY_KEY_FILE
charon_exports="$(charonctl await --env --retrieve-handle RETRIEVE_HANDLE)" || exit "$?"
eval "${charon_exports}"
unset charon_exports
# Run the consuming command here, in this same shell invocation.
```

### 4c. Save a file

```bash
charonctl get --retrieve-handle RETRIEVE_HANDLE --name DEPLOY_KEY --to ~/.ssh/deploy_key --mode 0600
```

Without `--to`, `get` writes the value to stdout: pipe it directly into a
consumer; never display it in a tool result.

## 5. Hand over a secret

Each secret takes exactly one source: `env` (variable name), `file` (one path
uploaded as a file), `files` (a list of paths on one secret), `text_file`
(path whose contents become text, verbatim), or `text` (inline). Optional
`type` must match the source; do not send `"type"` instead of a source.
Prefer `env` or a path so the value is not in the JSON. Do not embed
real values in agent tool arguments.

```bash
echo '{
  "title": "Generated deploy key",
  "ttl": "1d",
  "secrets": [
    {"name": "API_TOKEN", "env": "API_TOKEN"},
    {"name": "NOTE", "text_file": "/tmp/note.txt"},
    {"name": "private key", "file": "/path/to/gen.pem"},
    {"name": "certs", "files": ["/path/to/a.pem", "/path/to/b.pem"]}
  ]
}' | charonctl send
```

One call creates, fills, and finalises the exchange. Give the user `LINK`
and mention `TITLE`. Keep `MANAGE_HANDLE` to abort with
`charonctl destroy --manage-handle`. A fill failure destroys the draft itself.

## 6. Handle expiry or failure

Collection stores a private local receipt and schedules deletion after
10 minutes. Rereading does not extend this lifetime. `NAME_FILE` paths expire
with the receipt; use `get --to` when a file needs to last longer. If cleanup
scheduling warns, run `charonctl cleanup --retrieve-handle RETRIEVE_HANDLE` after consuming it.
If the user cancels a pending exchange, `charonctl destroy --manage-handle MANAGE_HANDLE`.
`destroy` is the server entry; `cleanup` is the local receipt.

A Charon restart loses pending exchanges; collected local receipts still work
until deleted. Create a new request when the exchange has expired or
been lost, or when the receipt is gone and Charon reports already collected.

### Other errors and limits

- Exit codes: `0` success, `1` error (reason on stderr), `2` timed out/expired
  or no receipt, `3` blank field selected by `get` or `exec-env`. Once started,
  `exec-env` returns the command's exit status.
- Unknown JSON fields are errors; `type` must be `text` or `file`.
- Default limits: 20 secrets, 16 MB per file; deployments may override them.
  TTLs: `15m`, `1h`, `6h`, `1d`, `3d`, `1w`.
