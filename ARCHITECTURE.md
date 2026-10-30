# WhatsApp MCP — architecture review

## The choice we made

**Stack:** Go + `go.mau.fi/whatsmeow` (WhatsApp Web multidevice) + SQLite + MCP Streamable HTTP, self-hosted on loopback, driven from OpenCode via `tools.whatsapp.*`.

**Verdict:** For the stated goal — *"give my AI agent access to my personal WhatsApp account"* — this is the **best available option**, but it is **not** the "most correct" architecture in an absolute sense. It is the best *personal-account* option given the constraints. It is the wrong architecture for business/customer messaging.

## Why this is the right choice for personal use

| Option | Fit for personal account access | Why |
|---|---|---|
| **This fork (unofficial multidevice + self-hosted MCP)** | ✅ Best fit | Full read + send access to your real personal history; data stays on your machine; 42 tools; no Meta business account, no templates, no opt-in bureaucracy; loopback-only by default. |
| Official WhatsApp Business Cloud API + community MCP (`@pappcorn`, `waseemnasir`) | ❌ Wrong product | Requires Meta Business portfolio, verified phone number, approved templates, user opt-in. Cannot read your *personal* WhatsApp history — only a separate business number's conversations. Costs money at scale. |
| Hosted SaaS MCP (finereli, Periskope, etc.) | ⚠️ Convenient, privacy trade-off | Zero infra, but a third party sees your messages; the operator can derive encryption keys (finereli admits this); finereli is read-only only (no send). |
| Node.js + Baileys | ⚠️ Equivalent capability, heavier ops | Baileys is the JS/TS twin of whatsmeow. Same WhatsApp protocol, same ToS risk, but no single static binary and a larger npm supply-chain surface. No gain for a personal self-hosted server. |
| Evolution API | ⚠️ Overkill | Multi-instance WhatsApp hosting platform. You'd still need an MCP shim on top. Relevant only if you later want to sell WhatsApp MCP access to many users. |

**Bottom line:** Meta does **not** publish an official WhatsApp MCP server (their GitHub org contains only `meta/mcpguard-dynamic`, an eBPF sandbox). If you want an AI to read *your personal* WhatsApp, the unofficial multidevice route is the only path that works today. Accept it eyes-open.

## The honest caveats (you must accept these)

1. **WhatsApp ToS gray area.** `whatsmeow` impersonates a linked device. WhatsApp's terms discourage unofficial clients. Your account *could* be restricted. Risk for a read-mostly linked device is low but not zero.
2. **~20-day session rotation.** WhatsApp rotates linked-device sessions. You must re-scan the QR (or re-enter the phone code) roughly every 20 days, or when you remove/re-link devices.
3. **Linked-device cap (~4).** WhatsApp limits how many devices can be linked to one phone. If you hit it, log out an old one (Settings → Linked Devices).
4. **Lethal trifecta.** Incoming message text is untrusted data fed to an LLM with tool access. Prompt injection in a message could exfiltrate your chat history. Mitigations we applied: loopback-only binding, read-only mode, media path allowlist, log redaction. **Read-only mode is the strongest mitigation.**
5. **Contacts did not consent.** Their messages to you enter the agent's context window and go to the model provider. Use read-only mode if you don't need the AI to reply.
6. **Protocol fragility.** `whatsmeow` tracks WhatsApp Web's undocumented protocol. When WhatsApp changes, the bridge can break until `whatsmeow` catches up. We pin the dependency and run CI upgrade probes.

## What is architecturally weak (and what to fix next)

These are real limitations of the current design, ranked by impact:

### P0 — Health & observability
- `/healthz` added (reports connected/paired/state/uptime). Good first step.
- Still missing: structured JSON logs (`--log-format json`), Prometheus metrics endpoint (`/metrics`), and uptime/uptime breakdown. Makes the daemon hard to monitor at scale.

### P0 — Media reliability (upstream #72, #67, #73)
- `download_media` returns 403 for most history-synced media because WhatsApp's CDN signed URLs expire. Once expired, the media is unrecoverable unless you downloaded it before expiry. Fix: on 403, ask the phone to re-upload the media (requires the device to be online) or cache media permanently at download time.

### P0 — Pairing resilience (upstream #76, #77)
- When a pairing session expires, `serve` does not automatically start a new pairing flow; it stays "unpaired" until restarted. Fix: on session expiry / `LoggedOut`, auto-restart `StartPairing` and re-emit a QR without restarting the daemon.

### P1 — Edit/delete propagation (upstream #70, #71)
- Edited and "delete for everyone" messages keep their original body in the cache. Fix: apply `edit` and `revoke` protocol events to stored rows so the cache mirrors WhatsApp.

### P1 — Rate-limit replies & reactions (upstream #78, #79)
- Replies and reactions bypass the send rate limiter, and sent replies are not cached. Fix: extend `ratelimit.Limiter` to replies/reactions; persist outbound messages.

### P1 — No real-time push to the agent
- The architecture is **pull-only**: WhatsApp → bridge → SQLite, then agent polls via `list_messages`/`search_messages`. The agent cannot react to an incoming message instantly. Fix: add a webhook endpoint (`/webhook`) or an SSE/MCP resource stream that pushes new messages to the agent as they arrive.

### P2 — No audit log
- If the AI sends a message, there is no tamper-evident trail of what was sent, when, and by which tool. Fix: append-only audit table (tool, message_id, chat_jid, timestamp, result) for every mutation.

### P2 — Single-instance store lock
- `store/.lock` allows only one `serve` per store directory. Fine for a single personal account, but blocks running a second paired session (e.g. personal + business) on the same binary without separate store dirs. Documented workaround: separate `-store` dirs per instance.

## What we improved in this fork

| Area | Change | File |
|---|---|---|
| Read-only safety | `-read-only` flag / `WHATSAPP_MCP_READ_ONLY=1`; 16 read-only tools, 0 mutation tools | `cmd/whatsapp-mcp/serve.go`, `internal/mcp/tools.go`, `internal/mcp/server.go` |
| Event-driven pairing | SSE `/pair/events` replaces 5-second page auto-refresh; `PairCache` fans state to subscribers | `internal/daemon/pair_cache.go`, `internal/daemon/pair_handler.go`, `templates/pair.html.tmpl` |
| Professional device name | `WHATSAPP_MCP_DEVICE_NAME` (default "WhatsApp MCP") instead of "whatsmeow" | `cmd/whatsapp-mcp/{serve,login}.go`, `internal/client/client.go` |
| Captions stored | Image/video/document captions now in message `Content` and searchable | `internal/client/events.go` |
| Contact cards stored | Incoming contact cards saved as `[contact card] Name` (+ vCard) text | `internal/client/events.go` |
| Health endpoint | `GET /healthz` returns connected/paired/state/uptime | `internal/daemon/pair_handler.go` |

## Directory layout

```
whatsapp-mcp/
├── cmd/whatsapp-mcp/      # CLI: serve, login, smoke
├── internal/daemon/       # HTTP server, pairing, SSE, /healthz
├── internal/client/       # whatsmeow wrapper: send, download, events
├── internal/mcp/          # 42 MCP tool registrations, read-only mode
├── internal/store/        # SQLite: messages, chats, media metadata
├── internal/media/        # polls, contact cards
├── internal/security/     # path allowlisting, log redaction
├── internal/ratelimit/    # send rate limiter
├── docs/                  # systemd, launchd, hook examples
├── Dockerfile             # container image
├── ARCHITECTURE.md        # this file
├── TECH_STACK.md          # stack research + alternatives
└── IMPROVEMENTS.md        # prioritized backlog
```

## Data flow (incoming message)

```
WhatsApp servers
      │ WebSocket push (E2E encrypted)
      ▼
whatsapp bridge (whatsmeow)
      │ LID→phone resolution · extract text/caption/vcard
      ▼
SQLite store (messages.db)
      │ agent queries on demand
      ▼
MCP server (127.0.0.1:8765/mcp)
      │ tools.whatsapp.list_messages / search_messages
      ▼
OpenCode agent
```

## Data flow (outgoing message — normal mode only)

```
OpenCode agent
      │ tools.whatsapp.send_message / send_file …
      ▼
MCP server ──(read-only mode? error: tool disabled)──
      ▼
whatsapp bridge ──(rate-limited)──
      ▼
WhatsApp servers → recipient
      ▼
bridge caches sent message locally (conversation stays complete)
```

## Recommendation

**Keep this stack for personal use.** It is the only practical way to give an AI agent access to a personal WhatsApp account today. Do **not** use it for business/customer messaging — switch to the official WhatsApp Business Cloud API for that.

**Next work:** P0 items first — `/metrics` + structured logs, media 403 recovery, auto re-pair on expiry. Then P1 — edit/delete propagation, real-time push, audit log.
