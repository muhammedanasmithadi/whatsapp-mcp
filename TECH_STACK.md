# WhatsApp MCP — tech stack research & position

## Current stack (this fork)

| Layer | Choice | Why |
|---|---|---|
| Language | Go 1.26 | Single static binary, fast startup, low memory (~16 MB RSS), excellent concurrency for the long-lived WhatsApp socket + MCP HTTP server. You already have Go 1.27 via mise. |
| WhatsApp protocol | whatsmeow (WhatsApp Web multidevice) | The de-facto Go library for linking a personal account as a "device." Reads your real chats via QR pairing. Unofficial — WhatsApp ToS discourages it (~20-day session rotation, linked-device cap). |
| Message store | SQLite (2 files: messages.db, whatsapp.db) | Zero-config, file-per-account, ACID, easy to back up/encrypt. No separate DB process. |
| MCP transport | Streamable HTTP (`mark3labs/mcp-go`) | Stateless, works over HTTP, no stdio juggling. OpenCode/Claude/Cursor connect to `http://127.0.0.1:8765/mcp`. |
| Pairing UX | Embedded Go templates + SSE (`/pair/events`) | Event-driven QR/status push instead of 5-second page auto-refresh. |
| Packaging / ops | `go build` single binary + systemd unit + Dockerfile | Deploy once, auto-restart, loopback-only by default. |

**Dependency footprint:** ~12 direct deps, dominated by `go.mau.fi/whatsmeow` (Signal-protocol E2E, WebSocket to WhatsApp servers) and `mattn/go-sqlite3` (CGO). Binary ~37 MB.

## Alternative stacks considered

### 1. Node.js + Baileys (the most popular unofficial WA lib)
Baileys is the JS/TS equivalent of whatsmeow and powers most community WhatsApp bots.
- **Pros:** Largest community, most examples, easy to find contributors.
- **Cons:** Node single-threaded event loop blocks on the long-lived WhatsApp socket; npm supply-chain surface is much larger than Go's module graph; no single static binary (needs Node runtime + `node_modules`). For a personal, self-hosted MCP this adds ops surface for little gain.
- **Verdict:** Not worth switching. The MCP layer (what the agent sees) is language-independent.

### 2. Python + PyWa (official WhatsApp Business Cloud API wrapper)
PyWa wraps Meta's official Cloud API (REST/Graph + webhooks), not the multidevice personal protocol.
- **Pros:** ToS-compliant, no QR pairing, no session rotation, business-grade rate limits (80 msg/s), templates, opt-in management.
- **Cons:** Requires a Meta Business portfolio, verified phone number, approved templates, and user opt-in before messaging. Costs money at scale. Cannot read your *personal* WhatsApp history — only a business number's conversations.
- **Verdict:** Different product. Use this if you're a business messaging customers, not if you want an AI to read your personal chats.

### 3. Go + Evolution API (`evolution-foundation/evolution-go`, 967★)
Multi-instance WhatsApp management API with its own REST surface, instances, QR pairing, webhooks.
- **Pros:** Purpose-built for hosting WhatsApp bots for many users; instance isolation; Go native.
- **Cons:** Heavier (its own DB, web server, instance manager); you'd still need an MCP shim on top. Overkill for a single personal account.
- **Verdict:** Relevant if you later want to offer WhatsApp MCP as a multi-tenant service. Not for personal use.

### 4. Official Meta WhatsApp Business Cloud API (no community wrapper)
Meta does **not** publish an official MCP server. Their org has only `meta/mcpguard-dynamic` (an eBPF sandbox for MCP tool calls). The official path is the Cloud API + a community MCP wrapper (e.g. `@pappcorn/whatsapp-mcp`, `waseemnasir2k26/whatsapp-cloud-mcp`).

## Recommendation

**Stay on the current Go + whatsmeow stack.** For a personal-account WhatsApp MCP the language is not the bottleneck — the WhatsApp protocol (unofficial multidevice), message freshness, media CDN expiry, and prompt-injection safety are. Switching to Node/Python/Rust buys nothing and adds ops surface. Invest effort in the MCP tool surface, pairing UX, health endpoints, and media reliability instead.

---

## Where the official API fits

If you need **compliant business messaging** (send templates to customers, opt-in, bulk broadcast), use the WhatsApp Business Cloud API via a community wrapper (`@pappcorn/whatsapp-mcp` / `waseemnasir2k26/whatsapp-cloud-mcp`). That is a separate deployment from this personal-account server. Do not mix personal multidevice and business Cloud API in one store.
