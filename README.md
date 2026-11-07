# WhatsApp MCP

A self-hosted Model Context Protocol (MCP) server that gives an AI agent read and send access to your personal WhatsApp account. It runs on your machine as a single Go binary, speaks MCP over HTTP at `127.0.0.1:8765/mcp`, and keeps your message history in a local SQLite database.

This is an independent, community project. It is not affiliated with, endorsed by, or operated by Meta Platforms, Inc. or WhatsApp LLC.

## What it does

Once paired with your phone (the same way you link WhatsApp Web), the daemon:

- Reads and searches your message history, contacts, and groups from local SQLite
- Sends text messages, media, polls, reactions, replies, contact cards, and map locations
- Reads Status (Stories), profile photos, contact metadata (About, verified name, LID), and online/last-seen presence
- Follows and reads WhatsApp Channels; mutes/unmutes channels
- Manages groups, blocklist, and privacy settings
- Controls chats: mute, pin, star messages, disappearing-message timer
- Schedules messages for later delivery (timer-driven, survives restarts)
- Keeps a local address book (bridge-only; not synced to the phone)
- Exposes 76 MCP tools over Streamable HTTP
- Serves health (`/healthz`) and Prometheus metrics (`/metrics`) endpoints
- Pushes new messages to connected clients over WebSocket (`/ws/events`)

## Quick start

### Prerequisites

- Go 1.26+ (build-time only; runtime needs just the compiled binary)
- FFmpeg (optional, for voice-note transcoding)
- An MCP client that speaks HTTP (Claude Desktop, Cursor, Claude Code, OpenCode, etc.)

### Build

```bash
git clone https://github.com/muhammedanasmithadi/whatsapp-mcp.git
cd whatsapp-mcp
make build   # writes ./bin/whatsapp-mcp
```

### Pair your phone

Start the daemon, then open the pairing page in a browser:

```bash
./bin/whatsapp-mcp serve          # starts on 127.0.0.1:8765
```

Visit `http://127.0.0.1:8765/pair`, scan the QR code with WhatsApp (Settings → Linked Devices → Link a Device), and the session persists to `./store/whatsapp.db`. WhatsApp rotates the session roughly every 20 days; the daemon re-pairs automatically with exponential backoff.

No browser? Run `./bin/whatsapp-mcp login` to render the QR in the terminal, or use the phone-number form on `/pair`.

### Connect your MCP client

```jsonc
// OpenCode (opencode.jsonc), Claude Code, Cursor, etc.
{ "mcpServers": { "whatsapp": { "type": "http", "url": "http://127.0.0.1:8765/mcp" } } }
```

Restart your client. The daemon outlives client restarts.

## Configuration

| Flag / env var | Default | Purpose |
|---|---|---|
| `-addr` / `WHATSAPP_MCP_ADDR` | `127.0.0.1:8765` | HTTP bind address |
| `-allow-remote` | off | Bind to a non-loopback address (requires `WHATSAPP_MCP_TOKEN`) |
| `WHATSAPP_MCP_TOKEN` | — | Bearer token for `/mcp` and `/pair/*` when remote-bound |
| `-read-only` / `WHATSAPP_MCP_READ_ONLY` | off | Expose only read/query tools (no send, edit, delete, or mutations) |
| `-log-format` / `WHATSAPP_MCP_LOG_FORMAT` | `text` | `text` for human logs; `json` for structured JSON lines |
| `WHATSAPP_MCP_MEDIA_ROOT` | `<store>/uploads/` | File allowlist root for send/download tools |
| `WHATSAPP_MCP_DEVICE_NAME` | `WhatsApp MCP` | Name shown for this device in WhatsApp → Linked Devices |
| `WHATSAPP_MCP_FULL_HISTORY_SYNC` | off | Backfill older messages for all cached chats after pairing |
| `-debug` / `WHATSAPP_MCP_DEBUG` | off | Verbose (unredacted) logs |

## Observability

- `GET /healthz` — JSON: `connected`, `paired`, `state`, `uptime_sec`
- `GET /metrics` — Prometheus text format: messages in/out, sends, errors, rate-limited, pairing state, uptime

## Real-time push

- `WS /ws/events` — WebSocket broadcast of new-message events as JSON
- `GET /pair/events` — Server-Sent Events stream of pairing state transitions

## Safety

This tool connects an AI agent to your personal WhatsApp account. Treat it accordingly:

- **Loopback only by default.** The daemon binds to `127.0.0.1`. Do not expose it remotely without `WHATSAPP_MCP_TOKEN`.
- **Read-only mode.** Run with `-read-only` if you only need the agent to read and summarize chats. It strips every send, edit, delete, mark-read, group, blocklist, privacy, and status-change tool.
- **Untrusted input.** Incoming message text is data fed to an LLM. Prompt injection could exfiltrate chat history. Do not assume message content is safe.
- **Contacts did not consent.** Their messages to you enter the agent's context window and go to the model provider.

## Media

`WHATSAPP_MCP_MEDIA_ROOT` (default `<store>/uploads/`) is the only directory send/download tools may touch. Paths outside the root, including symlinks that resolve outside it, are rejected.

## WhatsApp protocol layer

The daemon speaks the WhatsApp Web multidevice protocol through [whatsmeow](https://github.com/tulir/whatsmeow). Because WhatsApp changes the protocol server-side without notice, the project vendors whatsmeow and applies protocol fixes immediately via a Go `replace` directive:

```text
replace go.mau.fi/whatsmeow => ./vendor-whatsmeow/go.mau.fi/whatsmeow
```

The vendored fork lives under `vendor-whatsmeow/` and is kept in sync with upstream. Key fixes carried in the fork:

- **SKDM group ID**: `handleSenderKeyDistributionMessage` uses the protobuf `groupId` (not the enclosing stanza chat) to decide sender-key ownership, matching WhatsApp Web and Baileys. This is what makes statuses and group messages decrypt correctly when retried as direct-to-device SKDMs.
- **Profile picture tctoken**: media requests include the required token so thumbnails resolve.
- **Presence subscribe**: missing `id` attribute added to presence subscription stanzas.
- **Argo decoding**: hard-coded failure and macOS early-return removed.
- **SendPresence race**: presence updates no longer race with session teardown.
- **GetPrivacySettings**: errors are no longer swallowed.
- **Status About (set_status_message)**: WhatsApp Web calls the `xwa2_update_text_status` MEX mutation (mutation ID `9152604461510864`) first, then the legacy `<iq xmlns="status">` stanza. The MEX mutation is account-gated and fails with a 400 CRITICAL error on accounts without the text-status feature, so it is sent only when an emoji or ephemeral duration is requested; the legacy IQ is always sent to update the About text contacts see.
- **canonicalContactJID**: wrong suffix `@s.client.net` → `@s.whatsapp.net` fixed.

Protocol fixes are also contributed upstream where applicable: https://github.com/tulir/whatsmeow/pull/1330 (SKDM group ID) and a set-status-about branch tracking this repository's fork.

## Statuses (Stories) — push-only delivery

Status updates are cached as `status@broadcast` messages and arrive only via push. WhatsApp Web's `WAWebApiStatus.getAllStatuses` is a local DB query — there is no on-demand server fetch for statuses.

Two consequences:

1. **Missed broadcasts cannot be backfilled.** If the daemon was offline when a contact posted a status, that status is gone forever; WhatsApp does not re-send it, and `request_sync` for `status@broadcast` returns no history. Do not rely on the daemon to recover statuses posted while it was down.
2. **StatusV3 history import.** `HistorySync.statusV3Messages` (field 3) is the real `INITIAL_STATUS_V3` payload. The daemon imports it into `status@broadcast` rows on sync, resolves the poster via `key.participant` / `WebMessageInfo.participant` (LID → phone number), and keeps placeholders for pruned payloads. Statuses posted while the daemon is online and paired appear normally.

Profile photos and online/last-seen presence work independently of status delivery.

## Development

```bash
make build        # writes ./bin/whatsapp-mcp
make test         # unit tests
make test-race    # with -race
make vet          # go vet
make smoke        # boot-test without connecting to WhatsApp
```

## License

MIT. See [LICENSE](LICENSE).
