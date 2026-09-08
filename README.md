# charon

One-way, short-lived delivery for secrets. Self-hosted; there is no public instance.

Two modes, one mechanism:

- **Send** — you enter a title, a description and one or more named secrets, and
  get a link to hand to someone.
- **Request** — you describe what you need (an optional title plus a list of
  secrets), get a link to hand to someone, and they fill it in. Built for asking
  an agent's operator for credentials without those landing in a chat log.

Almost everything is optional. A request with no title gets a generated
two-word name; an unnamed secret becomes `secret-1`, `secret-2`, and so on;
descriptions exist only where they help. The one required field is the list of
secrets itself.

Every entry has three independent tokens:

| Token | Who holds it | What it can do |
|---|---|---|
| **submit** | whoever fills the form in | write the draft, submit it once |
| **retrieve** | whoever reads the payload | consume the secret, once |
| **manage** | the creator | read back the other two links, or destroy the entry |

Holding one never grants another. In request mode you hand out the submit link;
in send mode you hand out the retrieve link. Creating either redirects you to
`/manage?token=…`, which is bookmarkable — a refresh still shows your links
instead of losing them to page state.

## Server

The server can read every secret. The API is unauthenticated: possession of a
link grants access. Run it on a trusted network or behind an authenticating proxy.

### Behaviour worth knowing

- Text and draft state live in memory; uploaded files are encrypted in the
  scratch directory with `CHARON_SECRET_KEY`. Mount a tmpfs there. A server
  restart loses all exchanges.
- Draft text autosaves and files upload when selected. Unsubmitted drafts and
  their files expire with the entry TTL.
- Create-time `linger` defaults to `0`: the next lookup after retrieval misses,
  and the reaper deletes the entry and files on its next tick (every second).
  Download files immediately. For a browser copy window, request `"linger": "60s"`.
  `CHARON_LINGER` caps this window; the web UI requests that maximum.
- Tokens contain 120 random bits, encoded as lowercase base32.
- Markdown descriptions have raw HTML escaped.
- Callbacks are off until configured below.

### API

Create a request — this is the endpoint an agent calls:

```console
curl -X POST https://charon.example/api/requests -H 'Content-Type: application/json' -d '{
  "title": "Deploy credentials",
  "description": "Needed for the deployment.",
  "secrets": [
    {"name": "API_TOKEN", "description": "read+write, no expiry"},
    {"name": "deploy key",   "description": "the private key file", "type": "file"}
  ],
  "ttl": "1h",
  "linger": "0s"
}'
```

The smallest useful request is just `{"secrets": [{}]}`.

Request bodies are validated strictly. Unknown fields are rejected rather than
ignored, so a misspelled key is an error instead of a silently different
request, and `type` must be exactly `text` or `file`. Each secret accepts only
what its type declares: a file uploaded to a text secret is a `400`, and the
form renders one control per secret accordingly.

```jsonc
{
  "title":        "Deploy credentials",
  "submit_url":   "https://charon.example/e/fwlascooqxofa2ekujikwweq",  // hand this out
  "retrieve_url": "https://charon.example/e/po3nkmdgisdb5veespq6hfph",  // keep this
  "poll_url":     "https://charon.example/api/e/po3nkmdgisdb5veespq6hfph",
  "manage_url":   "https://charon.example/manage?token=kwwyvma5t53x4ulperhpuamo",
  "destroy_url":  "https://charon.example/api/e/kwwyvma5t53x4ulperhpuamo",
  "submit_id":    "fwlascooqxofa2ekujikwweq",
  "retrieve_id":  "po3nkmdgisdb5veespq6hfph",
  "manage_id":    "kwwyvma5t53x4ulperhpuamo",
  "expires_at":   "2026-09-03T14:59:51Z"
}
```

Then **wait on `poll_url` until `fulfilled` is true** and POST
`/api/e/{id}/retrieve` using `retrieve_id`. `?wait=30s` turns the poll into a long poll:
the response is held until the other side submits, the entry dies, or the
window (capped at 60s) elapses, then answers as a plain GET would. Loop on it
instead of sleeping between requests.

```console
curl "https://charon.example/api/e/<retrieve_id>?wait=30s"  # {"fulfilled": false, ...}
curl -X POST https://charon.example/api/e/<retrieve_id>/retrieve
```

A submitter may skip a field: the form asks them to confirm, then submits. Both
value keys are always present and `null` when skipped, so "they left it blank"
is distinguishable from "this version does not send that key".

`POST .../retrieve` answers `409` until the other side submits, so polling it
directly works too. Text values come inline; files come as download URLs, valid
until the entry self-destructs — an agent should not be handed a base64 blob.

```jsonc
{
  "title": "Deploy credentials",
  "destructs_at": "2026-09-03T13:59:56Z",
  "secrets": [
    {"name": "API_TOKEN", "type": "text", "text": "tok_live_...", "files": null},
    {"name": "deploy key", "type": "file", "text": null,
     "files": [{"filename": "id_ed25519", "size": 411, "url": ".../api/f/lzzulu..."}]},
    {"name": "optional note", "type": "text", "text": null, "files": null}
  ]
}
```

| Route | Purpose |
|---|---|
| `POST /api/requests` | Create a request (you receive the answer) |
| `POST /api/secrets` | Create a send (you provide the content) |
| `GET /api/e/{id}` | View / poll; `?wait=30s` long-polls. Draft values for a submit token, links for a manage token |
| `PUT /api/e/{id}/text/{idx}` | Autosave one field |
| `POST /api/e/{id}/files/{idx}` | Upload a file to one secret (multipart) |
| `DELETE /api/e/{id}/files/{idx}/{n}` | Remove a drafted file |
| `POST /api/e/{id}/submit` | Finalise the draft |
| `POST /api/e/{id}/retrieve` | Consume; `409` while pending |
| `DELETE /api/e/{id}` | Destroy; manage token only (`destroy_url`) |
| `GET /api/f/{token}` | Download a file |
| `GET /api/config` | Limits and TTL options, for the frontend |

### Callbacks

Instead of polling, a caller can pass `callback_url` and be pushed the payload
the moment the other side submits:

```jsonc
{"secrets": [{"name": "API_TOKEN"}], "callback_url": "http://hooks.internal:8080/hook"}
```

A caller-supplied URL is a "make my server issue a request" primitive, so the
feature is **off until you write a rule** saying what is allowed.
`CHARON_CALLBACK_RULE` is a [rulekit](https://github.com/qpoint-io/rulekit)
expression evaluated against the URL's parts:

| Field | Example | Notes |
|---|---|---|
| `url` | `http://hooks.internal:8080/hook` | the whole thing |
| `scheme` | `http` | only `http`/`https` ever reach the rule |
| `host` | `hooks.internal:8080` | as written, port included |
| `hostname` | `hooks.internal` | no port; IPv6 debracketed |
| `port` | `8080` | a number — defaults to 80/443 when the URL omits it |
| `path`, `query`, `fragment`, `user` | `/hook` | |
| `ip` | `192.168.0.10` | **only when the host is a literal IP** |

```sh
CHARON_CALLBACK_RULE='hostname == "hooks.internal" and port == 8080'
CHARON_CALLBACK_RULE='scheme == "https" and hostname matches /\.internal$/'
CHARON_CALLBACK_RULE='ip in 192.168.0.0/16'
CHARON_CALLBACK_RULE='hostname in ["hooks.internal", "localhost"]'
CHARON_CALLBACK_RULE='true'   # allow anything — only on a trusted network
```

The rule is parsed at startup, so a typo is a refusal to boot rather than a
surprise at request time. Evaluation **fails closed**: a rule error, or a rule
naming a field the URL cannot supply, rejects the callback. Only a clean pass
allows it. URLs are checked at creation, so a caller finds out immediately.

`ip` is populated only for literal addresses — **charon never resolves DNS
here**. Resolving would be a rebinding hole: a name could satisfy the rule and
then point elsewhere by delivery time. A CIDR rule therefore only matches URLs
that already carry an address.

Set `CHARON_CALLBACK_SECRET` to sign deliveries. Each request carries
`X-Charon-Timestamp` and `X-Charon-Signature: sha256=<hex>`, the HMAC-SHA256 of
`timestamp + "." + body`, so a receiver can reject both forgeries and replays.

Delivery is retried three times. If every attempt fails the entry is released
rather than consumed, so the retrieve link still works — a webhook that happened
to be down does not destroy the secret.

### Hermes

A typical pairing is Hermes (or any bot) that delivers the submit link and,
optionally, receives `callback_url`. If that link is opened as a Telegram Mini
App, the page reads `start_param` as the entry id. Charon holds no bot token
and does not check identities: possession of the link is still the whole
security model.

### Configuration

Environment variables for the `charon` binary.

| Variable | Default | Meaning |
|---|---|---|
| `CHARON_ADDR` | `:1337` | Listen address |
| `CHARON_BASE_URL` | `http://localhost:1337` | Public origin, used to build links |
| `CHARON_SCRATCH_DIR` | a fresh temp dir | Uploaded file bytes, encrypted. Mount a tmpfs here. |
| `CHARON_SECRET_KEY` | random at startup | Encrypts scratch files. Unset generates an ephemeral key; those files are unreadable after a restart. |
| `CHARON_MAX_TEXT_BYTES` | `65536` | Per-field text cap |
| `CHARON_MAX_FILE_BYTES` | `16777216` | Per-file cap |
| `CHARON_MAX_FILES` | `20` | Files per entry |
| `CHARON_MAX_SECRETS` | `20` | Secrets per entry |
| `CHARON_MAX_TOTAL_BYTES` | `268435456` | Global ceiling across all live entries |
| `CHARON_DEFAULT_TTL` | `24h` | Expiry when the caller does not ask for one |
| `CHARON_MAX_TTL` | `7d` | Longest expiry a caller may ask for |
| `CHARON_LINGER` | `60s` | Maximum post-retrieve window a create may request. Per-entry default is `0`. |
| `CHARON_CALLBACK_RULE` | — | rulekit expression; unset disables callbacks |
| `CHARON_CALLBACK_SECRET` | — | HMAC key for signing callback deliveries |

Durations (`CHARON_DEFAULT_TTL`, `CHARON_MAX_TTL`, `CHARON_LINGER`) accept `d`
and `w` in addition to Go's own units, so `7d` and `1w` both work.

### Running

```console
docker run -p 1337:1337 --tmpfs /scratch:size=512m,uid=65532,gid=65532 ghcr.io/kamaln7/charon
```

Behind a reverse proxy, set `CHARON_BASE_URL` to the public origin. Compose:

```yaml
services:
  charon:
    build: https://github.com/kamaln7/charon.git
    restart: unless-stopped
    environment:
      CHARON_BASE_URL: https://charon.example
    tmpfs:
      - /scratch:size=512m,uid=65532,gid=65532
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
```

## charonctl

The client an agent runs so it never has to touch the HTTP API or see a secret.
It does not read the server environment table above.

Install with `go install github.com/kamaln7/charon/cmd/charonctl@latest`.

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CHARON_API` | config `"api"`, else `http://localhost:1337` | Origin of the charon server |
| `CHARON_SCRATCH_DIR` | user cache (`~/.cache/charonctl`) | Local receipts after `await` |

Optional JSON config at `$XDG_CONFIG_HOME/charonctl/config.json` (default
`~/.config/charonctl/config.json`, also on macOS):

```json
{"api":"https://charon.example"}
```

`CHARON_API` overrides `"api"`. A missing file is fine; unreadable or malformed
config is an error, including unknown fields and trailing JSON. Relative
`XDG_CONFIG_HOME` values are ignored. `get`, `cleanup`, `exec-env`, and help
work without reading the config.

### Commands

Operands use named flags. `request` and `send` read their JSON spec from stdin.
`--retrieve-handle` is the collect token; `--manage-handle` is the owner token.

```console
$ echo '{"secrets":[{"name":"API_TOKEN"},{"name":"DEPLOY_KEY","type":"file"}]}' \
    | charonctl request
TITLE quiet-otter
LINK https://charon.example/e/fwlascooqxofa2ekujikwweq
EXPIRES 2026-09-04T13:59:51Z
RETRIEVE_HANDLE po3nkmdgisdb5veespq6hfph
MANAGE_HANDLE kwwyvma5t53x4ulperhpuamo

$ charon_exports=$(charonctl await --env --retrieve-handle po3nkmdgisdb5veespq6hfph) || exit "$?"
collected "quiet-otter"
set API_TOKEN (71 chars)
file DEPLOY_KEY (id_ed25519, 411 bytes)
receipt ~/.cache/charonctl/charonctl-po3nkmdgisdb5veespq6hfph
$ eval "$charon_exports"
$ unset charon_exports

$ charonctl get --to ~/.ssh/deploy --mode 0600 --retrieve-handle po3nkmdgisdb5veespq6hfph --name DEPLOY_KEY
$ charonctl cleanup --retrieve-handle po3nkmdgisdb5veespq6hfph
```

| Command | Purpose |
|---|---|
| `request` | Create a request; `--await` also collects the answer |
| `await` | Collect into a local receipt; `--env` emits shell exports containing secrets |
| `get` | Read a receipt value; `--to PATH` saves it to a file |
| `exec-env` | Run a command with receipt secrets; repeat `--name` to select fields |
| `send` | Create, fill, and submit a send from JSON sources |
| `status` | Show exchange state; a manage handle also returns the share link |
| `destroy` | Delete the server exchange using its manage handle |
| `cleanup` | Delete the local receipt using its retrieve handle |

Receipts are private local files, deleted after `--cleanup-after` (default 10m).
Repeated `await` calls reuse the receipt without extending its lifetime. Copy
files with `get --to` if needed longer; if cleanup scheduling warns, run `cleanup`.

Use `charonctl COMMAND --help` for flags and input formats. The
[agent skill](skills/charon-secrets/SKILL.md) covers source selection, safe
consumption, cancellation, and recovery.

## Development

```console
go test ./...
go run .
```

The static frontend in `web/` is embedded with `//go:embed`; no build step.
`go test` checks JavaScript syntax when Node is installed.

[Basecoat](https://basecoatui.com) 1.0.2 is vendored in `web/basecoat.css`.
It includes component classes, not Tailwind utilities; custom styles live in
`style.css`. Dark mode uses `html.dark`, set by the external `theme.js` to
respect the CSP.

## License

MIT
