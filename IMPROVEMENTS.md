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
| 12 | Media 403 / unrecoverable after CDN expiry | `IsMediaExpired` detects 403/404/410; `recover_media` MCP tool + `RecoverMedia` client method | `internal/client/download.go`, `internal/mcp/tools_message.go` |
| 13 | Pairing session expires; needs restart to re-pair | Auto-retry with exponential backoff in `StartPairing`; `onLoggedOut` runs in goroutine | `cmd/whatsapp-mcp/production_driver.go`, `internal/daemon/server.go` |

## Gap backlog (from upstream issues, not yet fixed)

| Priority | Issue | Description | Effort |
|---|---|---|---|
| P1 | #78/#79 Replies & reactions bypass send rate limiter; sent replies not cached | Extend ratelimit to replies/reactions; cache outbound messages | ~3h |
| P2 | #59/#60 Full history sync when linking a device | Opt-in bulk backfill at pairing time instead of only recent window | ~4h |
| P2 | #52/#56 Voice-note transcription via pluggable STT endpoint | Transcribe incoming `.ogg` voice notes to text | ~6h (external ASR dep) |
| P2 | #51/#53 Read-only WhatsApp Business label support | List labels, find chats by label | ~3h |
| P3 | #69 Publish container image to GHCR on release tags | CI container build/publish | ~2h |
| P3 | #82 Include message ID on every `list_messages` line | Debuggability | ~0.5h |
| P3 | #80/#85 Remote clients can't retrieve documents/videos / send attachments from download links | Embed or proxy media for remote MCP clients | ~4h |
| P3 | #75/#74 Redact chat names, contact names, full JIDs in logs | Already partially done; extend to all log paths | ~1h |
| P2 | #59/#60 Full history sync when linking a device | Opt-in bulk backfill at pairing time instead of only recent window | ~4h |
| P2 | #52/#56 Voice-note transcription via pluggable STT endpoint | Transcribe incoming `.ogg` voice notes to text | ~6h (external ASR dep) |
| P2 | #51/#53 Read-only WhatsApp Business label support | List labels, find chats by label | ~3h |
| P3 | #69 Publish container image to GHCR on release tags | CI container build/publish | ~2h |
| P3 | #82 Include message ID on every `list_messages` line | Debuggability | ~0.5h |
| P3 | #80/#85 Remote clients can't retrieve documents/videos / send attachments from download links | Embed or proxy media for remote MCP clients | ~4h |
| P3 | #75/#74 Redact chat names, contact names, full JIDs in logs | Already partially done; extend to all log paths | ~1h |

## Suggested additions beyond the backlog

These are not in the upstream issue list but raise the project from "usable" to "professional":

### A. Health & observability (#65 is the gatekeeper)
- `GET /healthz` → JSON `{connected, paired, own_phone, store_dir, media_root, uptime, tool_count}`.
- `GET /metrics` (Prometheus text format): messages_in/out, sends, errors, pairing events, rate-limit rejections.
- Structured JSON logs (replace ad-hoc stderr `Infof`) with a `--log-format json` flag.

### B. MCP resources (in addition to tools)
Expose chat history as MCP `resources` (`whatsapp://chats/{jid}/messages`) so agents can `resources/read` a full transcript instead of paging `list_messages`. Resources are cacheable and cheaper than repeated tool calls.

### C. Audit log
Append-only log (SQLite table or file) of every agent-initiated WhatsApp action: who (tool), what (message_id/chat), when, result. Essential when the AI can send messages — gives you an accountability trail.

### D. Per-tool allowlist / sandbox
Beyond read-only mode, let the operator enable/disable individual tools (e.g. allow `send_message` but block `delete_message` / `leave_group`). MCP hint annotations already declare destructive/read-only; enforce them server-side.

### E. Reverse webhook (WhatsApp → server → agent)
Right now the agent polls WhatsApp (pull). A webhook endpoint (`/webhook`) that WhatsApp pushes incoming messages to would make the agent reactive (instant replies) instead of latency-bound by poll intervals. Requires a public URL or tunnel.

### F. Session analytics dashboard
A `/dashboard` page: message volume by day, top contacts, group activity, unread count. Reuses the existing SQLite — no new deps.

### G. Multi-account / multi-store
Run multiple daemon instances on different ports/stores (e.g. personal + business). A small process supervisor or a single binary with `--store` per instance is enough; document it.

### H. Test coverage for pairing SSE
Add a test that subscribes to `/pair/events` and asserts a `SetQR` → `SetPaired` transition arrives as SSE `data:` frames.

## Suggested roadmap (order by risk/value)

1. **P0 first:** `/healthz` + structured logs (#65). Makes the daemon ops-ready.
2. **Pairing resilience:** auto-re-pair on expiry (#76/#77) + full-history-sync opt-in (#59/#60). Removes the "restart to pair" footgun.
3. **Media reliability:** fix 403 downloads + CDN expiry recovery (#72/#67/#73). Media is a core feature; broken downloads erode trust.
4. **Edit/delete propagation** (#70/#71) and **rate-limit replies/reactions** (#78/#79). Correctness of the send path.
5. **Nice-to-have:** voice transcription (#52), business labels (#51), audit log (C), MCP resources (B).

## Testing notes

- `go test ./...` passes (all packages).
- Read-only mode verified: 16 tools, zero mutation tools.
- SSE pairing verified: `/pair/events` emits state transitions immediately.
- Manual tool verification done via OpenCode: `get_status`, `list_chats`, `list_groups`, `get_blocklist`, `get_privacy_settings`, `list_messages` (captions present), `get_message_context`, `download_media`, `request_sync` all return correct data.
- Write tools (`send_message`, `edit_message`, `delete_message`, etc.) are registered but intentionally not fired against live contacts to avoid sending real messages.
