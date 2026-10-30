# WhatsApp MCP Server

[![License: MIT](https://img.shields.io/github/license/Sealjay/mcp-whatsapp)](LICENSE)
[![CI](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml/badge.svg)](https://github.com/Sealjay/mcp-whatsapp/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Sealjay/mcp-whatsapp)](https://goreportcard.com/report/github.com/Sealjay/mcp-whatsapp)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-protocol-6366f1)](https://modelcontextprotocol.io/)
[![45 tools](https://img.shields.io/badge/tools-45-blue)]()
[![whatsmeow](https://img.shields.io/badge/whatsmeow-multidevice-25D366?logo=whatsapp&logoColor=white)](https://github.com/tulir/whatsmeow)

A single-binary Go [MCP](https://modelcontextprotocol.io/) server that wraps [whatsmeow](https://github.com/tulir/whatsmeow) to expose a personal WhatsApp account to LLMs. `whatsapp-mcp serve` runs as a lightweight HTTP daemon on `127.0.0.1:8765`; MCP clients (Claude Desktop, Cursor, Claude Code, OpenCode, etc.) connect to it via HTTP — no process spawning, no stdin/stdout juggling. Messages are cached in local SQLite and only travel to the model when the agent calls a tool.

> **Unaffiliated.** This is an independent open-source project. It is not affiliated with, endorsed by, or otherwise associated with Meta Platforms, Inc., WhatsApp, or [whatsmeow](https://github.com/tulir/whatsmeow). "WhatsApp" is a trademark of Meta Platforms, Inc., used here nominatively to describe interoperability.

This started as a fork of [lharries/whatsapp-mcp](https://github.com/lharries/whatsapp-mcp) and has since been rewritten as a single Go binary.

## Setup

### Prerequisites

- Go 1.26+ (build-time only; runtime needs just the compiled binary).
- An MCP client that speaks HTTP (Claude Desktop, Cursor, Claude Code, OpenCode, etc.).
- FFmpeg (_optional_) — required only for `send_audio_message` when the input is not already `.ogg` Opus. Without it, use `send_file` to send raw audio.
- **Windows:** CGO must be enabled — see [docs/windows.md](docs/windows.md).

### Install

```bash
git clone https://github.com/muhammedanasmithadi/whatsapp-mcp.git
cd whatsapp-mcp
make build    # writes ./bin/whatsapp-mcp
```

### Pair your phone (first run only)

Start the daemon, then open the pairing page in a browser:

```bash
./bin/whatsapp-mcp serve          # starts on 127.0.0.1:8765
open http://127.0.0.1:8765/pair   # macOS; or visit the URL manually
```

Scan the QR with WhatsApp (*Settings → Linked Devices → Link a Device*). The session persists to `./store/whatsapp.db`; WhatsApp rotates it roughly every 20 days, when `/pair` shows a fresh QR.

- **No browser:** `./bin/whatsapp-mcp login` renders the QR in the terminal.
- **No QR scan:** use the phone-number form on `/pair` (or `login -phone +<international number>`), then type the code into *Link a Device → Link with phone number instead*.
- The `/pair/*` endpoints are CSRF-protected and rate-limited.
- **Auto re-pair:** if the QR expires or WhatsApp rotates the session, the daemon restarts pairing automatically with exponential backoff. You do not need to restart the process.

### Connect your MCP client

`serve` listens on `127.0.0.1:8765` (or `$WHATSAPP_MCP_ADDR`) and serves MCP at `/mcp`. It is stateless (no `Mcp-Session-Id`) and speaks MCP 2026-07-28 as well as older revisions.

```jsonc
// Claude Code (.claude/mcp.json or ~/.claude/mcp.json) and Cursor (~/.cursor/mcp.json)
{ "mcpServers": { "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" } } }

// OpenCode (opencode.jsonc)
{ "mcpServers": { "whatsapp": { "type": "remote", "url": "http://127.0.0.1:8765/mcp", "oauth": false } } }

// Claude Desktop only launches stdio servers, so bridge with mcp-remote
// (~/Library/Application Support/Claude/claude_desktop_config.json)
{ "mcpServers": { "whatsapp": { "command": "npx", "args": ["-y", "mcp-remote", "http://127.0.0.1:8765/mcp"] } } }
```

Restart the client. The daemon outlives client restarts.

### Send and receive files

`WHATSAPP_MCP_MEDIA_ROOT` (default `<store>/uploads/`, created on first run) is the only directory file tools may touch:

- **Sending:** `send_file` and `send_audio_message` take a `media_path` under the root.
- **Receiving:** `download_media` caches to `<store>/<chat_jid>/<hex(message_id)>/<safe_filename>` (checked against the stored size and SHA-256). An optional `output_path` also places the file under the root; an existing file there makes the call a no-op.

Paths outside the root, including symlinks that resolve outside it, are rejected. Anything inside the root is readable and writable by the tools, so keep secrets out.

**Sandboxed clients (Claude Cowork and similar)** cannot read the daemon's cache. Point `WHATSAPP_MCP_MEDIA_ROOT` at a directory the sandbox can read and give the client this instruction:

> When calling the WhatsApp MCP's `download_media`, always pass `output_path` set to a path under your shared workspace. Without it the decrypted file lands only in the daemon's local cache, which is outside your sandbox and unreadable. `output_path` must live under `WHATSAPP_MCP_MEDIA_ROOT` on the daemon side; the basename is yours to pick.

### Serve flags and environment

| Flag / env | Default | Meaning |
|---|---|---|
| `-addr` / `WHATSAPP_MCP_ADDR` | `127.0.0.1:8765` | HTTP bind address |
| `-allow-remote` | off | Bind to a non-loopback address. Requires `WHATSAPP_MCP_TOKEN` |
| `WHATSAPP_MCP_TOKEN` | — | Bearer token for `/mcp` and `/pair/*` when remote-bound |
| `-read-only` / `WHATSAPP_MCP_READ_ONLY` | off | Expose only read/query tools (16 tools; no send, edit, delete, mark-read, group/privacy mutations) |
| `-log-format` / `WHATSAPP_MCP_LOG_FORMAT` | `text` | `text` for human-readable stderr logs; `json` for structured JSON lines |
| `WHATSAPP_MCP_MEDIA_ROOT` | `<store>/uploads/` | File allowlist root for send/download tools |
| `WHATSAPP_MCP_DEVICE_NAME` | `WhatsApp MCP` | Name shown for this linked device in WhatsApp → Linked Devices |
| `WHATSAPP_MCP_FULL_HISTORY_SYNC` | off | After pairing, request backfill of older messages for every cached chat |
| `-debug` / `WHATSAPP_MCP_DEBUG` | off | Verbose (unredacted) logs |

## Running the daemon

Events reach SQLite **only while `serve` runs**. After downtime, whatsmeow backfills via `HistorySync` within WhatsApp's own retention window; anything older is lost, and `request_sync` backfills one chat on demand. A `flock(2)` on `store/.lock` allows one `serve` (or `login`) per store.

| Option | How |
|---|---|
| macOS launchd | Copy `docs/launchd/com.sealjay.whatsapp-mcp.plist` to `~/Library/LaunchAgents/`, fill `{{PATH_TO_REPO}}` / `{{STORE_DIR}}`, `launchctl load` |
| Linux systemd | Copy `docs/systemd/whatsapp-mcp.service` to `~/.config/systemd/user/`, fill placeholders, `systemctl --user enable --now whatsapp-mcp` |
| Manual | `./bin/whatsapp-mcp serve` |

**Docker.** The image (`debian:bookworm-slim` plus ffmpeg, a few hundred MB) runs `-store /home/app/store`; persist that volume. Pair with a one-off `login` container first, because `login` and a running `serve` cannot share the store lock:

```bash
docker build -t whatsapp-mcp --build-arg VERSION=$(git describe --tags --always) .
docker run --rm -it -v whatsapp-mcp-store:/home/app/store whatsapp-mcp login
TOKEN=$(openssl rand -hex 32); echo "$TOKEN"
docker run -d --name whatsapp-mcp -v whatsapp-mcp-store:/home/app/store \
  -p 127.0.0.1:8765:8765 -e WHATSAPP_MCP_TOKEN="$TOKEN" \
  whatsapp-mcp serve -addr 0.0.0.0:8765 -allow-remote
```

Clients then send `Authorization: Bearer $TOKEN`. The container binds loopback by default, so `-allow-remote` plus a token is what makes `-p` work. To re-pair after WhatsApp rotates the session (about every 20 days), stop the container, rerun the `login` container and start it again.

## Observability

The daemon exposes two HTTP endpoints on the same port:

- `GET /healthz` — JSON: `connected`, `paired`, `state`, `uptime_sec`, `detail`. Use this for systemd, load balancers, or MCP client health checks.
- `GET /metrics` — Prometheus text format: `whatsapp_mcp_messages_in_total`, `whatsapp_mcp_messages_out_total`, `whatsapp_mcp_sends_total`, `whatsapp_mcp_errors_total`, `whatsapp_mcp_rate_limited_total`, `whatsapp_mcp_pairing_state` (gauge: 0=unpaired, 1=awaiting_qr, 2=paired, 3=error), `whatsapp_mcp_uptime_seconds`.

## Real-time push

- `WS /ws/events` — WebSocket broadcast of new-message events as JSON frames. Clients connect and receive `{type: "message", data: "{...}"}` whenever a message arrives. Falls back to polling if your proxy does not support WebSocket.
- `GET /pair/events` — Server-Sent Events stream of pairing state transitions (`unpaired` → `awaiting_qr` → `paired` → `error`). The pairing page uses this so the QR updates live without refreshing.

## Architecture

```
cmd/whatsapp-mcp/       login / serve / smoke subcommands
internal/daemon/        HTTP server, pairing state machine, /pair endpoints, /healthz, /metrics, WebSocket hub
internal/client/        whatsmeow wrapper: send, download, events, history, features, rate limiting
internal/mcp/           mark3labs/mcp-go v1 server + tool registrations (45 tools)
internal/media/         ogg parsing, waveform synthesis, ffmpeg shell-out
internal/security/      path allowlisting, filename sanitisation, log redaction
internal/store/         SQLite cache, LID resolution, query layer, labels
internal/ratelimit/     sliding-window send/action rate limiter
```

Data lives under `./store/` (`-store DIR`): `messages.db` (message cache), `whatsapp.db` (whatsmeow session) and `.lock`. Query tools read SQLite; send and action tools call whatsmeow; incoming events are written to SQLite by a goroutine in the same process.

## Tools

45 tools, grouped by purpose.

### Read / query

| Tool | Purpose |
|---|---|
| `search_contacts` | Substring search across cached contact names and phone numbers |
| `list_messages` | Query + filter messages; returns formatted text with context windows. Every line includes `[Message ID: <id>]`. |
| `list_chats` | List chats with last-message preview; sort by activity or name |
| `get_chat` | Chat metadata by JID |
| `get_message_context` | Before/after window around a specific message |
| `download_media` | Download persisted media to a local path |
| `request_sync` | Ask WhatsApp to backfill history for a chat |
| `recover_media` | Recover a media message whose CDN URL expired (403/404/410). Requests a fresh history sync so WhatsApp re-sends it with a current URL; then retry `download_media`. |
| `list_labels` | List WhatsApp Business labels (label_id, name, color) |
| `list_chats_by_label` | List chat JIDs that have a given label |

### Send

| Tool | Purpose |
|---|---|
| `send_message` | Send a text message to a phone number or JID |
| `send_file` | Send image/video/document/raw audio with optional caption; `view_once: bool` marks image/video/audio submessages as view-once (ignored for documents) |
| `send_audio_message` | Send a voice note (auto-converts via ffmpeg if not `.ogg` Opus); supports `view_once: bool` |
| `send_poll` | Send a poll with a question and 2+ options; `selectable_count` controls how many options a voter may pick |
| `send_poll_vote` | Cast a vote on a previously-seen poll; `options` must match option names exactly |
| `get_poll_results` | Return the tally for a poll we have cached (includes 0-vote options) |
| `send_contact_card` | Send a contact card; synthesises a vCard 3.0 from `name` + `phone`, or pass a raw `vcard` to skip synthesis |

### Message actions

| Tool | Purpose |
|---|---|
| `mark_read` | Mark specific message IDs as read |
| `mark_chat_read` | Ack the most recent incoming messages in a chat to clear the unread badge |
| `send_reaction` | React to a message (empty emoji clears an existing reaction) |
| `send_reply` | Text reply that quotes a prior message |
| `edit_message` | Edit a previously-sent message |
| `delete_message` | Revoke (delete for everyone) a message |
| `send_typing` | Set per-chat composing / recording presence |

### Groups

| Tool | Purpose |
|---|---|
| `create_group` | Create a group with a name and initial participants |
| `leave_group` | Leave a group |
| `list_groups` | List all groups the user is a member of |
| `get_group_info` | Full group metadata (participants, settings, invite config) |
| `update_group_participants` | Add / remove / promote / demote participants |
| `set_group_name` | Change the group subject |
| `set_group_topic` | Change the group description; empty string clears it |
| `set_group_announce` | Toggle announce-only mode (only admins can send) |
| `set_group_locked` | Toggle locked mode (only admins can edit group metadata) |
| `get_group_invite_link` | Get the invite link; `reset: true` revokes the previous link first |
| `join_group_with_link` | Join a group via a `chat.whatsapp.com` URL or bare invite code |

### Blocklist

| Tool | Purpose |
|---|---|
| `get_blocklist` | Return the current blocklist (JIDs, usually `@lid`, plus phone numbers where known) |
| `block_contact` | Block a contact by phone number or JID |
| `unblock_contact` | Unblock a contact |

### Privacy / presence / status

| Tool | Purpose |
|---|---|
| `send_presence` | Set own availability (`available` or `unavailable`) — distinct from per-chat `send_typing` |
| `get_privacy_settings` | Current privacy settings as JSON |
| `set_privacy_setting` | Change one privacy setting by `name` + `value` (strict enum validation) |
| `set_status_message` | Update the profile "About" text; empty string clears it |

### Admin

| Tool | Purpose |
|---|---|
| `is_on_whatsapp` | Batch-check which phone numbers are registered on WhatsApp |
| `get_status` | Report whether the bridge is connected and which account it's paired as |
| `pairing_status` | Report the device-pairing state as a structured `setup_state` envelope |

### Read-only mode

Run with `-read-only` or `WHATSAPP_MCP_READ_ONLY=1`. The daemon registers only 16 read/query tools: `list_chats`, `list_messages`, `list_groups`, `list_labels`, `list_chats_by_label`, `search_contacts`, `get_chat`, `get_message_context`, `get_group_info`, `get_group_invite_link`, `get_poll_results`, `get_privacy_settings`, `get_status`, `is_on_whatsapp`, `download_media`, `request_sync`, `pairing_status`, `download_media`. Every send, edit, delete, mark-read, group-mutation, blocklist-mutation, privacy, and status-change tool is absent — even under prompt injection, the agent cannot act on WhatsApp.

## Deferred

Intentionally not exposed yet:

- **`subscribe_presence`** — no persistence layer for presence events, skipped to avoid a dangling tool.
- **Profile photo setter** — upstream whatsmeow doesn't expose a user-level setter.
- **Approval-mode participants, communities, newsletters** — low-use surface, deferred.

## Limitations

- **Prompt-injection risk:** as with many MCP servers, this one is subject to [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/). Prompt injection in incoming messages could lead to private data exfiltration — treat the tool surface accordingly. Use `-read-only` if you only need reading.
- **Message gaps:** see [Running the daemon](#running-the-daemon).
- **Single instance per store:** only one `whatsapp-mcp serve` can hold the store lock. Parallel MCP clients must point at different `-store` directories (and therefore different paired sessions).
- **Windows:** requires CGO and a C compiler — see [docs/windows.md](docs/windows.md).
- **Upstream bounds:** message fetch/send is bounded by what [whatsmeow](https://github.com/tulir/whatsmeow) supports against the WhatsApp web multidevice API.

## Development

```bash
make build        # writes ./bin/whatsapp-mcp
make test         # unit tests
make test-race    # with -race
make vet          # go vet
make smoke        # boot-test the server without connecting to WhatsApp
```

### Upgrading whatsmeow

Weekly CI runs an upstream upgrade probe. To do it manually:

```bash
make upgrade-check
```

This bumps `go.mau.fi/whatsmeow@main`, re-tidies, builds, and tests. If green, commit the `go.mod` / `go.sum` changes.

`scripts/mdtest-parity.sh` in CI fails the build early if upstream removes or renames any whatsmeow method we call — it's the canary for API drift.

## Troubleshooting

- **`connect failed …` on `serve`** — the daemon is not paired. Open `http://127.0.0.1:8765/pair` in a browser and scan the QR. Alternatively, run `./bin/whatsapp-mcp login` in a terminal.
- **`another whatsapp-mcp instance is already running`** — only one `serve` can hold the store lock. Check for a stray process (`ps aux | grep whatsapp-mcp`) or another MCP client pointed at the same `-store` directory.
- **QR doesn't display** — the terminal doesn't render half-block Unicode. Try iTerm2, Windows Terminal, or similar.
- **Device limit reached** — WhatsApp caps linked devices. Remove one from *Settings → Linked Devices* on your phone.
- **No messages loading** — after initial auth, it can take several minutes for history to backfill. Use `request_sync` to target a specific chat.
- **WhatsApp out of sync** — delete both database files (`store/messages.db` and `store/whatsapp.db`) and re-run `login`.
- **`ffmpeg not found`** — `send_audio_message` needs ffmpeg on `PATH` to convert non-Opus audio. Use `send_file` for raw audio instead.

### Debug logging

By default, stderr logs redact JIDs to `…<last-4>`, summarise bodies as `[<length>B: text|url|command]` and collapse media URLs to `<scheme>://<host>/…`. To see content while debugging, run `./bin/whatsapp-mcp -debug serve` or set `WHATSAPP_MCP_DEBUG=1`. Even then, phone-number-shaped digits show only their last 5 (`+15551234567` → `****34567`). That is obfuscation, not anonymisation: someone who knows your contacts can still correlate them.

For Claude Desktop integration issues, see the [MCP documentation](https://modelcontextprotocol.io/quickstart/server#claude-for-desktop-integration-issues).

## Contributing

Contributions welcome via pull request. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT Licence — see [LICENSE](LICENSE).