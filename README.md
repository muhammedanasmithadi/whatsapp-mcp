# WhatsApp MCP

A self-hosted Model Context Protocol (MCP) server that gives an AI agent read and send access to your personal WhatsApp account. It runs on your machine as a single Go binary, speaks MCP over HTTP at `127.0.0.1:8765/mcp`, and keeps your message history in a local SQLite database.

This is an independent, community project. It is not affiliated with, endorsed by, or operated by Meta Platforms, Inc. or WhatsApp LLC.

## What it does

Once paired with your phone (the same way you link WhatsApp Web), the daemon:

- Reads and searches your message history, contacts, and groups from local SQLite
- Sends text messages, media, polls, reactions, replies, and contact cards
- Manages groups, blocklist, and privacy settings
- Exposes 45 MCP tools over Streamable HTTP
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
