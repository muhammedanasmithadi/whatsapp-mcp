# WhatsApp MCP — improvement plan

## Status of this fork (applied already)

| # | Gap | Fix applied | Where |
|---|---|---|---|
| 1 | No read-only mode (AI can send/delete under prompt injection) | `-read-only` flag / `WHATSAPP_MCP_READ_ONLY=1` exposes only 16 read/query tools | `cmd/whatsapp-mcp/serve.go`, `internal/mcp/tools.go` |
| 2 | Media captions dropped (not searchable) | Image/video/document captions now stored in message `Content` | `internal/client/events.go` `extractTextContent` |
| 3 | Incoming contact cards silently dropped | Stored as `[contact card] Name` (+ vCard) text | `internal/client/events.go` |
| 4 | Device shows as "whatsmeow" in Linked Devices | Configurable `DeviceName` (default "WhatsApp MCP") via `WHATSAPP_MCP_DEVICE_NAME` | `internal/client/client.go`, `cmd/whatsapp-mcp/{serve,login}.go` |
| 5 | Pairing page auto-refreshes every 5 s (polling) | Event-driven SSE `/pair/events` pushes QR/state transitions live | `internal/daemon/pair_handler.go`, `pair_cache.go`, `templates/pair.html.tmpl` |
| 6 | No health/readiness endpoint | `GET /healthz` returns connected/paired/state/uptime | `internal/daemon/pair_handler.go` |
| 7 | No Prometheus metrics | `GET /metrics` returns messages in/out, sends, errors, rate-limited, pairing state, uptime | `internal/daemon/pair_handler.go` |
| 8 | No structured logging | `--log-format json` / `WHATSAPP_MCP_LOG_FORMAT=json` emits JSON log lines | `internal/client/logger.go`, `cmd/whatsapp-mcp/serve.go` |
| 9 | No WebSocket real-time push | `WS /ws/events` broadcasts new-message events to connected clients | `internal/daemon/server.go` |
| 10 | Edit/delete propagation to cache | `IsEdit` handled in `handleMessage`; `store.UpdateMessage` / `store.RevokeMessage` added | `internal/client/events.go`, `internal/store/store.go` |
| 11 | Media 403 / unrecoverable after CDN expiry | `IsMediaExpired` detects 403/404/410; `recover_media` MCP tool + `RecoverMedia` client method | `internal/client/download.go`, `internal/mcp/tools_message.go` |
| 12 | Pairing session expires; needs restart to re-pair | Auto-retry with exponential backoff in `StartPairing`; `onLoggedOut` runs in goroutine | `cmd/whatsapp-mcp/production_driver.go`, `internal/daemon/server.go` |
| 13 | Replies & reactions bypass send rate limiter; sent replies not cached | `checkActionRate` covers send/reaction/reply/edit/revoke/typing/poll/card; sent replies & reactions cached via `persistSent`/`persistReaction` | `internal/client/features.go`, `internal/client/features_media.go`, `internal/client/send.go` |
| 14 | No full history sync opt-in when linking | `-full-history-sync` flag / `WHATSAPP_MCP_FULL_HISTORY_SYNC=1` requests backfill for all cached chats after pairing | `internal/daemon/server.go`, `cmd/whatsapp-mcp/serve.go`, `cmd/whatsapp-mcp/production_driver.go`, `internal/store/store.go` |
| 15 | No WhatsApp Business label support | `list_labels` + `list_chats_by_label` MCP tools; `LabelEdit`/`LabelAssociationChat` events handled; `labels` + `label_associations` tables | `internal/client/events.go`, `internal/store/store.go`, `internal/mcp/tools_query.go` |
| 16 | Message ID missing from `list_messages` text lines | Every `FormatMessage` line now includes `[Message ID: <id>]` (media lines keep full media prefix) | `internal/store/format.go` |
| 17 | Chat/contact names logged unredacted | All chat name, contact name, group name, sender name logs use `c.redactor.JID()` / `c.redactor.Body()` | `internal/client/events.go` |

## Gap backlog (from upstream issues, not yet fixed)

| Priority | Issue | Description | Effort |
|---|---|---|---|
| P2 | #52/#56 Voice-note transcription via pluggable STT endpoint | Transcribe incoming `.ogg` voice notes to text | ~6h (external ASR dep) |
| P3 | #80/#85 Remote clients can't retrieve documents/videos / send attachments from download links | Embed or proxy media for remote MCP clients | ~4h |

## Suggested additions beyond the backlog

These are not in the upstream issue list but raise the project from "usable" to "professional":

### A. MCP resources (in addition to tools)
Expose chat history as MCP `resources` (`whatsapp://chats/{jid}/messages`) so agents can `resources/read` a full transcript instead of paging `list_messages`. Resources are cacheable and cheaper than repeated tool calls.

### B. Audit log
Append-only log (SQLite table or file) of every agent-initiated WhatsApp action: who (tool), what (message_id/chat), when, result. Essential when the AI can send messages — gives you an accountability trail.

### C. Per-tool allowlist / sandbox
Beyond read-only mode, let the operator enable/disable individual tools (e.g. allow `send_message` but block `delete_message` / `leave_group`). MCP hint annotations already declare destructive/read-only; enforce them server-side.

### D. Reverse webhook (WhatsApp → server → agent)
Right now the agent polls WhatsApp (pull). A webhook endpoint (`/webhook`) that WhatsApp pushes incoming messages to would make the agent reactive (instant replies) instead of latency-bound by poll intervals. Requires a public URL or tunnel.

### E. Session analytics dashboard
A `/dashboard` page: message volume by day, top contacts, group activity, unread count. Reuses the existing SQLite — no new deps.

### F. Multi-account / multi-store
Run multiple daemon instances on different ports/stores (e.g. personal + business). A small process supervisor or a single binary with `--store` per instance is enough; document it.

### G. Test coverage for pairing SSE
Add a test that subscribes to `/pair/events` and asserts a `SetQR` → `SetPaired` transition arrives as SSE `data:` frames.

## Dependency: whatsmeow

| Detail | Value |
|---|---|
| Module | `go.mau.fi/whatsmeow` |
| Version pinned | `v0.0.0-20261029193418-2fc844fe1693` (Oct 29 2026) |
| Role | WhatsApp Web multidevice client — E2E Signal protocol, WebSocket to WhatsApp servers, LID resolution, history sync, media download/upload |
| Direct imports | `client`, `store/sqlstore`, `types`, `types/events`, `binary/proto`, `proto/waHistorySync`, `util/log` |
| Risk | Unofficial multidevice library; WhatsApp ToS discourages it (~20-day session rotation, linked-device cap ~4). Protocol changes can break the bridge until whatsmeow catches up. |

## Testing notes

- `go test ./...` passes (all packages).
- Read-only mode verified: 16 tools, zero mutation tools.
- SSE pairing verified: `/pair/events` emits state transitions immediately.
- WebSocket verified: `/ws/events` broadcasts new-message events.
- `/healthz` and `/metrics` verified responding.
- Manual tool verification done via OpenCode: `get_status`, `list_chats`, `list_groups`, `get_blocklist`, `get_privacy_settings`, `list_messages` (captions present, message IDs on every line), `get_message_context`, `download_media`, `request_sync` all return correct data.
- Write tools (`send_message`, `edit_message`, `delete_message`, etc.) are registered but intentionally not fired against live contacts to avoid sending real messages.
