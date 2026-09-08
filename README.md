# charon

One-way delivery for secrets. Nothing is persisted, nothing survives a restart.

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
| **manage** | the creator | read back the other two links, nothing else |

Holding one never grants another. In request mode you hand out the submit link;
in send mode you hand out the retrieve link. Creating either redirects you to
`/manage?token=…`, which is bookmarkable — a refresh still shows your links
instead of losing them to page state.

## Why not yopass

yopass encrypts in the browser, which means the link has to carry the key and
looks like `#/s/<id>/<key>`. charon holds plaintext in memory instead, so links
are a single opaque ID — and the server can read every secret. That is the whole
trade: **run this only on a network you trust.**

## Behaviour worth knowing

- **In memory only.** Text lives in the process; file bytes go to a scratch
  directory (mount a tmpfs over it). A restart drops everything, including
  half-finished drafts.
- **Drafts autosave.** The submit form PUTs each field as you type, so a
  Telegram webview being suspended mid-form costs nothing. Files upload the
  moment they are picked.
- **Retrieval lingers, then self-destructs.** The first read starts a timer
  (`CHARON_LINGER`, default 60s); reads inside that window return the identical
  payload, and afterwards the entry and its files are gone. Burning strictly on
  the first byte breaks any client that retries or drops a connection.
- **IDs are random, not sequential.** 120 bits of `crypto/rand` in base32, which
  looks like an [xid](https://github.com/rs/xid) but cannot be enumerated. A real
  xid encodes a timestamp and a counter; on a service whose only security is
  possession of the URL, that would be a hole.
- **Markdown is rendered with raw HTML escaped.** Descriptions are
  attacker-supplied as soon as anything can reach the create endpoint.
- **Callbacks are rule-gated and off by default.** See below.

## API

Create a request — this is the endpoint an agent calls:

```console
curl -X POST https://secrets.example/api/requests -H 'Content-Type: application/json' -d '{
  "title": "DigitalOcean deploy credentials",
  "description": "Needed for the MCP server config.",
  "secrets": [
    {"name": "DO_API_TOKEN", "description": "read+write, no expiry"},
    {"name": "deploy key",   "description": "the private key file", "type": "file"}
  ],
  "ttl": "1h"
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
  "submit_url":   "https://secrets.example/e/fwlascooqxofa2ekujikwweq",  // hand this out
  "retrieve_url": "https://secrets.example/e/po3nkmdgisdb5veespq6hfph",  // keep this
  "poll_url":     "https://secrets.example/api/e/po3nkmdgisdb5veespq6hfph",
  "manage_url":   "https://secrets.example/manage?token=kwwyvma5t53x4ulperhpuamo",
  "expires_at":   "2026-09-03T14:59:51Z"
}
```

Then **wait on `poll_url` until `fulfilled` is true** and `POST` the retrieve
URL once. `?wait=30s` turns the poll into a long poll: the response is held
until the other side submits, the entry dies, or the window (capped at 60s,
see `max_wait_seconds` in `/api/config`) elapses, then answers as a plain GET
would. Loop on it instead of sleeping between requests.

```console
curl "https://secrets.example/api/e/<retrieve_id>?wait=30s"  # {"fulfilled": false, ...}
curl -X POST https://secrets.example/api/e/<retrieve_id>/retrieve
```

A submitter may skip a field: the form asks them to confirm, then submits. Both
value keys are always present and `null` when skipped, so "they left it blank"
is distinguishable from "this version does not send that key".

`POST .../retrieve` answers `409` until the other side submits, so polling it
directly works too. Text values come inline; files come as one-shot URLs, valid
until the entry self-destructs — an agent should not be handed a base64 blob.

```jsonc
{
  "title": "DigitalOcean deploy credentials",
  "destructs_at": "2026-09-03T13:59:56Z",
  "secrets": [
    {"name": "DO_API_TOKEN", "type": "text", "text": "dop_v1_...", "files": null},
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
| `GET /api/f/{token}` | Download a file |
| `GET /api/config` | Limits and TTL options, for the frontend |

## charonctl

`cmd/charonctl` is the client an agent runs so it never has to touch the API
or see a secret. Install it with `go install github.com/kamaln7/charon/cmd/charonctl@latest`
and point it at your server with `CHARON_API`.

```console
$ echo '{"secrets":[{"name":"DO_TOKEN"},{"name":"DEPLOY_KEY","type":"file"}]}' \
    | charonctl request
LINK https://secrets.example/e/fwlascooqxofa2ekujikwweq
EXPIRES 2026-09-04T13:59:51Z
HANDLE po3nkmdgisdb5veespq6hfph

$ eval "$(charonctl await --env po3nkmdgisdb5veespq6hfph)"
collected "quiet-otter"
set DO_TOKEN (71 chars)
file DEPLOY_KEY (id_ed25519, 411 bytes)
receipt /tmp/charonctl-po3nkmdgisdb5veespq6hfph

$ charonctl get --to ~/.ssh/deploy --mode 0600 po3nkmdgisdb5veespq6hfph DEPLOY_KEY
$ charonctl cleanup po3nkmdgisdb5veespq6hfph
```

- `request` takes the same JSON as `POST /api/requests`, defaults the TTL to
  `1d`, and insists every name is a shell identifier, because `--env` turns
  them into variables. `--await` continues straight into `await`.
- `await` long-polls, collects once, and keeps the values in a private
  receipt directory (`CHARON_SCRATCH`, default the temp dir). Later calls
  answer from the receipt, so charon's linger window never matters. What was
  set goes to stderr, values never do; `--env` writes `export` lines to
  stdout, files as `NAME_FILE=path`. The receipt is removed after
  `--cleanup-after` (default 10m) by a detached copy of the process.
- `get` reads one value, to stdout or to a path. Exit code 3 means the user
  left it blank.
- `send` takes `{"secrets":[{"name":..,"text":..}|{"name":..,"file":path}]}`,
  fills the entry, and prints the retrieve link.
- Exit codes: 0 fine, 1 error, 2 timed out or expired, 3 blank.

## Callbacks

Instead of polling, a caller can pass `callback_url` and be pushed the payload
the moment the other side submits:

```jsonc
{"secrets": [{"name": "DO_API_TOKEN"}], "callback_url": "http://hermes:9119/hook"}
```

A caller-supplied URL is a "make my server issue a request" primitive, so the
feature is **off until you write a rule** saying what is allowed.
`CHARON_CALLBACK_RULE` is a [rulekit](https://github.com/qpoint-io/rulekit)
expression evaluated against the URL's parts:

| Field | Example | Notes |
|---|---|---|
| `url` | `http://hermes:9119/hook` | the whole thing |
| `scheme` | `http` | only `http`/`https` ever reach the rule |
| `host` | `hermes:9119` | as written, port included |
| `hostname` | `hermes` | no port; IPv6 debracketed |
| `port` | `9119` | a number — defaults to 80/443 when the URL omits it |
| `path`, `query`, `fragment`, `user` | `/hook` | |
| `ip` | `192.168.0.3` | **only when the host is a literal IP** |

```sh
CHARON_CALLBACK_RULE='hostname == "hermes" and port == 9119'
CHARON_CALLBACK_RULE='scheme == "https" and hostname matches /\.internal$/'
CHARON_CALLBACK_RULE='ip in 192.168.0.0/16'
CHARON_CALLBACK_RULE='hostname in ["hermes", "localhost"]'
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

## Telegram Mini App

The frontend works as a [Mini App](https://core.telegram.org/bots/webapps)
without charon knowing anything about your bot.

1. In BotFather: `/newapp`, pick your bot, short name (e.g. `secrets`), URL
   pointing at your charon.
2. Link straight to one entry by appending the submit token as `startapp`:

   ```
   https://t.me/<bot>/<app>?startapp=<the id from submit_url>
   ```

   Telegram hands that back to the page as `initDataUnsafe.start_param`, which
   is the only Mini App wiring charon has. A bot can send the link as plain
   text — no inline-keyboard payload needed.

charon does **not** verify Telegram identities, and holds no bot token or bot
name: possession of a link is the whole security model. Whoever owns the bot
composes the `t.me` link, because they are the one who knows its name.

> The page is loaded by **your device**, not by Telegram's servers. If charon is
> only reachable on a LAN or a VPN, the Mini App works only when your phone is on
> that network. The TLS certificate must be publicly trusted either way.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CHARON_ADDR` | `:1337` | Listen address |
| `CHARON_BASE_URL` | `http://localhost:1337` | Public origin, used to build links |
| `CHARON_SCRATCH_DIR` | a fresh temp dir | Where file bytes go |
| `CHARON_MAX_TEXT_BYTES` | `65536` | Per-field text cap |
| `CHARON_MAX_FILE_BYTES` | `16777216` | Per-file cap |
| `CHARON_MAX_FILES` | `20` | Files per entry |
| `CHARON_MAX_SECRETS` | `20` | Secrets per entry |
| `CHARON_MAX_TOTAL_BYTES` | `268435456` | Global ceiling across all live entries |
| `CHARON_DEFAULT_TTL` | `24h` | Expiry when the caller does not ask for one |
| `CHARON_MAX_TTL` | `7d` | Longest expiry a caller may ask for |
| `CHARON_LINGER` | `60s` | Grace period after the first read |
| `CHARON_CALLBACK_RULE` | — | rulekit expression; unset disables callbacks |
| `CHARON_CALLBACK_SECRET` | — | HMAC key for signing callback deliveries |

Durations (`CHARON_DEFAULT_TTL`, `CHARON_MAX_TTL`, `CHARON_LINGER`) accept `d`
and `w` in addition to Go's own units, so `7d` and `1w` both work.

**The API is unauthenticated by design.** Possession of a link is the whole
security model. Run charon on a trusted network, or behind a reverse proxy that
authenticates.

## Running

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
      CHARON_BASE_URL: https://secrets.example
    tmpfs:
      - /scratch:size=512m,uid=65532,gid=65532
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
```

## Development

```console
go test ./...
go run .
```

The frontend is static files under `web/`, embedded with `//go:embed`. No build
step, no bundler, no npm.

Styling is [Basecoat](https://basecoatui.com) 1.0.2, vendored as
`web/basecoat.css` — the standalone CDN build, which has Tailwind already
compiled in. It is copied into the repo rather than hotlinked so the UI works on
a network with no route to a CDN. Two things to know if you touch it:

- **The bundle ships Basecoat's component classes only, not Tailwind's
  utilities.** `w-full`, `text-sm` and friends do not exist; anything
  utility-shaped lives in `style.css`.
- **Dark mode keys off `html.dark`, not `prefers-color-scheme`**, so `theme.js`
  wires the media query up by hand. It is a separate file because the CSP is
  `script-src 'self'` and loosening that for one inline script would be the
  wrong trade on a page that displays secrets.

| File | Holds |
|---|---|
| `main.go` | wiring, scratch setup, the reaper |
| `server.go` | routes, middleware, handler plumbing |
| `handlers.go` | one function per endpoint |
| `model.go` | every request and response type, and the conversions |
| `store.go` | entries, tokens, byte accounting — memory only |
| `scratch.go` | the disk side: expiry-encoded filenames and sweeps |
| `callback.go`, `telegram.go`, `ttl.go`, `config.go`, `names.go` | as named |

Scratch filenames are `<unix expiry>-<random>`. The store deletes files as
their entries die, but it only knows about entries this process created — a
crash or a SIGKILL with a persistent scratch mount leaves orphans nothing would
collect. Encoding the deadline in the name means a sweep needs no state: read
the directory, parse the prefix, delete what is past due. That runs on a timer
and again at startup.

## License

MIT
