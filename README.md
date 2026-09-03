# charon

One-way delivery for secrets. Nothing is persisted, nothing survives a restart.

Two modes, one mechanism:

- **Send** — you enter a title, a description and one or more named secrets, and
  get a link to hand to someone.
- **Request** — you describe what you need (a title plus a list of named items),
  get a link to hand to someone, and they fill it in. Built for asking an agent's
  operator for credentials without those credentials landing in a chat log.

Every entry has two independent tokens: a **submit** token and a **retrieve**
token. In request mode you hand out the submit link; in send mode you hand out
the retrieve link. Holding one never grants the other.

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

## API

Create a request — this is the endpoint an agent calls:

```console
curl -X POST https://secrets.example/api/requests -H 'Content-Type: application/json' -d '{
  "title": "DigitalOcean deploy credentials",
  "description": "Needed for the MCP server config.",
  "items": [
    {"name": "DO_API_TOKEN", "description": "read+write, no expiry"},
    {"name": "deploy key",   "description": "the private key file", "type": "file"}
  ],
  "ttl": "1h"
}'
```

```jsonc
{
  "submit_url":   "https://secrets.example/e/fwlascooqxofa2ekujikwweq",  // hand this out
  "retrieve_url": "https://secrets.example/e/po3nkmdgisdb5veespq6hfph",  // keep this
  "telegram_url": "https://t.me/yourbot/secrets?startapp=fwlascoo...",   // if configured
  "poll_url":     "https://secrets.example/api/e/po3nkmdgisdb5veespq6hfph",
  "expires_at":   "2026-09-03T14:59:51Z"
}
```

Then **poll `poll_url` until `fulfilled` is true** and `POST` the retrieve URL
once:

```console
curl https://secrets.example/api/e/<retrieve_id>            # {"fulfilled": false, ...}
curl -X POST https://secrets.example/api/e/<retrieve_id>/retrieve
```

`POST .../retrieve` answers `409` until the other side submits, so polling it
directly works too. Text values come inline; files come as one-shot URLs, valid
until the entry self-destructs — an agent should not be handed a base64 blob.

```jsonc
{
  "title": "DigitalOcean deploy credentials",
  "destructs_at": "2026-09-03T13:59:56Z",
  "items": [
    {"name": "DO_API_TOKEN", "type": "text", "text": "dop_v1_..."},
    {"name": "deploy key", "type": "file",
     "files": [{"filename": "id_ed25519", "size": 411, "url": ".../api/f/lzzulu..."}]}
  ]
}
```

| Route | Purpose |
|---|---|
| `POST /api/requests` | Create a request (you receive the answer) |
| `POST /api/secrets` | Create a send (you provide the content) |
| `GET /api/e/{id}` | View / poll. Returns the draft for a submit token only |
| `PUT /api/e/{id}/text/{idx}` | Autosave one field |
| `POST /api/e/{id}/files/{idx}` | Upload a file to one item (multipart) |
| `DELETE /api/e/{id}/files/{idx}/{n}` | Remove a drafted file |
| `POST /api/e/{id}/submit` | Finalise the draft |
| `POST /api/e/{id}/retrieve` | Consume; `409` while pending |
| `GET /api/f/{token}` | Download a file |
| `GET /api/config` | Limits and TTL options, for the frontend |

## Telegram Mini App

The frontend runs as a [Mini App](https://core.telegram.org/bots/webapps), so a
bot can send you a link that opens the form inline in the chat.

1. In BotFather: `/newapp`, pick your bot, give it a short name (e.g. `secrets`)
   and point it at your charon URL.
2. Set `CHARON_TELEGRAM_BOT_TOKEN`, `CHARON_TELEGRAM_BOT_NAME` and
   `CHARON_TELEGRAM_APP_NAME`.
3. `telegram_url` now comes back on every create. A bot only needs to send it as
   **plain text** — no inline-keyboard payload — and Telegram renders an Open
   button. The ID travels in `?startapp=`, which the page reads back as
   `initDataUnsafe.start_param`.

The page sends `initData` in an `X-Telegram-Init-Data` header on every call.
The server verifies it (HMAC-SHA256 under a `WebAppData`-derived key, plus an
`auth_date` freshness check) and can restrict access to specific user IDs.

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
| `CHARON_MAX_FILES` | `20` | Files per entry, and items per entry |
| `CHARON_MAX_TOTAL_BYTES` | `268435456` | Global ceiling across all live entries |
| `CHARON_MAX_TTL` | `168h` | Longest expiry a caller may ask for |
| `CHARON_LINGER` | `60s` | Grace period after the first read |
| `CHARON_TELEGRAM_BOT_TOKEN` | — | Enables initData verification |
| `CHARON_TELEGRAM_BOT_NAME` | — | Bot username, for `telegram_url` |
| `CHARON_TELEGRAM_APP_NAME` | — | Mini App short name |
| `CHARON_TELEGRAM_ALLOWED_USERS` | — | Comma-separated Telegram user IDs |
| `CHARON_TELEGRAM_REQUIRED` | `false` | Reject calls with no valid initData |

With no bot token set the API is unauthenticated by design — put it on a trusted
network, or in front of a reverse proxy that authenticates.

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

The frontend is three static files under `web/`, embedded with `//go:embed`.
No build step, no bundler, no dependencies.

## License

MIT
