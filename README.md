# WhatsApp MCP

A self-hosted Model Context Protocol (MCP) server that gives an AI agent read and send access to your personal WhatsApp account. It runs on your machine as a single Go binary, speaks MCP over Streamable HTTP at `127.0.0.1:8765/mcp`, and keeps your message history in a local SQLite database.

This is an independent, community project. It is not affiliated with, endorsed by, or operated by Meta Platforms, Inc. or WhatsApp LLC.

## What it does

Once paired with your phone (the same way you link WhatsApp Web), the daemon:

| Domain | What it does |
|--------|--------------|
| **Messages** | Read, search, send, edit, delete, and recover messages; read receipts; mark read; schedule messages; reply; react; poll; contact cards; locations; voice-note transcription |
| **Statuses (Stories)** | Read, view, post, delete statuses; read receipts; status privacy setting |
| **Profile** | Read/set About text, profile photo, contact metadata (About, verified name, LID, linked devices) |
| **Presence** | Query contact online/last-seen; set per-chat typing/recording indicators |
| **Groups** | Create, leave, join via link, participants, name, topic, announce, lock, invite link |
| **Channels** | Follow/unfollow, mute, list, info, messages |
| **Contacts** | Synced address book (saved, business, push names) with LID resolution, local overrides, search, resolve, diagnose, sync, create, update, delete |
| **Blocklist & Privacy** | Blocklist, privacy settings (group add, last seen, status, profile, read receipts, online, calls, messages, defense, stickers) |
| **Scheduling** | Timer-driven scheduled messages that survive restarts |
| **Auto-responder** | Event-driven out-of-office replies to incoming 1:1 text messages |
| **Audit & observability** | Audit log, health check, Prometheus metrics, WebSocket events, SSE pairing events |

The daemon exposes **100 MCP tools** over Streamable HTTP, serves health (`/healthz`) and Prometheus metrics (`/metrics`), and pushes new messages to connected clients over WebSocket (`/ws/events`).

## Architecture

```
┌─────────────────┐      Streamable HTTP      ┌─────────────────────┐
│   AI agent      │◄─────────────────────────►│  whatsapp-mcp       │
│  (Claude, etc.) │      /mcp                 │  MCP server (Go)    │
└─────────────────┘                           │  • 86 tool handlers   │
                                              │  • rate limiting      │
                                              │  • SQLite cache       │
                                              └──────────┬──────────┘
                                                         │
                                                         │ whatsmeow multidevice
                                                         │ protocol (vendored fork)
                                                         ▼
                                              ┌─────────────────────┐
                                              │  WhatsApp Web         │
                                              │  (linked device)      │
                                              │  phone ↔ server       │
                                              └─────────────────────┘
```

The daemon wraps [whatsmeow](https://github.com/tulir/whatsmeow), the Go WhatsApp Web multidevice library. Because WhatsApp changes its protocol server-side without notice, the project **vendors whatsmeow** via a Go `replace` directive and applies protocol fixes immediately rather than waiting for upstream:

```text
replace go.mau.fi/whatsmeow => ./vendor-whatsmeow/go.mau.fi/whatsmeow
```

Protocol fixes carried in the fork (also contributed upstream where applicable):

| Fix | What it does |
|-----|--------------|
| **SKDM group ID** | `handleSenderKeyDistributionMessage` uses the protobuf `groupId`, not the enclosing stanza chat, to decide sender-key ownership. This makes statuses and group messages decrypt correctly when retried as direct-to-device SKDMs. Upstream: [tulir/whatsmeow#1330](https://github.com/tulir/whatsmeow/pull/1330) |
| **Profile picture tctoken** | Media requests include the required token so thumbnails resolve |
| **Presence subscribe** | Missing `id` attribute added to presence subscription stanzas |
| **Argo decoding** | Hard-coded failure and macOS early-return removed |
| **SendPresence race** | Presence updates no longer race with session teardown |
| **GetPrivacySettings** | Errors are no longer swallowed |
| **set_status_message** | WhatsApp Web calls the `xwa2_update_text_status` MEX mutation (ID `9152604461510864`) first, then the legacy `<iq xmlns="status">` stanza. The MEX mutation is account-gated and fails with a 400 CRITICAL error on accounts without the text-status feature, so it is sent only when an emoji or ephemeral duration is requested; the legacy IQ is always sent |
| **canonicalContactJID** | Wrong suffix `@s.client.net` → `@s.whatsapp.net` fixed |

## Quick start

### Prerequisites

- **Go 1.27+** (build-time only; runtime needs just the compiled binary)
- **FFmpeg** (optional, for voice-note transcoding via `send_audio_message`)
- An MCP client that speaks HTTP (Claude Desktop, Cursor, Claude Code, OpenCode, etc.)

### Build

Download a prebuilt binary from the [latest release](https://github.com/muhammedanasmithadi/whatsapp-mcp/releases/latest). Release assets cover Linux (amd64, arm64), macOS (amd64, arm64) and Windows (amd64), with SHA-256 sums in `checksums.txt`. The binary needs no runtime dependencies except FFmpeg for voice-note transcoding.

Or build from source:

```bash
git clone https://github.com/muhammedanasmithadi/whatsapp-mcp.git
cd whatsapp-mcp
make build   # writes ./bin/whatsapp-mcp
```

### Pair your phone

Start the daemon, then open the pairing page:

```bash
./bin/whatsapp-mcp serve          # starts on 127.0.0.1:8765
```

Visit `http://127.0.0.1:8765/pair`, scan the QR code with WhatsApp (Settings → Linked Devices → Link a Device), and the session persists to `./store/whatsapp.db`. WhatsApp rotates the session roughly every 20 days; the daemon re-pairs automatically with exponential backoff.

No browser? Run `./bin/whatsapp-mcp login` to render the QR in the terminal, or use the phone-number form on `/pair`.

### Connect your MCP client

Add this to your client configuration:

```jsonc
// OpenCode (opencode.jsonc), Claude Code, Cursor, etc.
{
  "mcpServers": {
    "whatsapp": {
      "type": "http",
      "url": "http://127.0.0.1:8765/mcp"
    }
  }
}
```

Restart your client. The daemon outlives client restarts.

### One-click install with MCPB

Each release also ships a `.mcpb` bundle for Linux, macOS and Windows. Install the bundle matching your platform in an MCPB-capable desktop client; the client starts the binary over **stdio** and fills in the configuration for you.

Pair once before installing the bundle, because stdio mode has no browser to show a QR code:

```bash
whatsapp-mcp -store ~/.whatsapp-mcp login
```

The bundle's `store_dir` setting defaults to `~/.whatsapp-mcp`, and its `read_only` setting maps to `WHATSAPP_MCP_READ_ONLY`. To run stdio mode by hand:

```bash
whatsapp-mcp -store ~/.whatsapp-mcp stdio -read-only
```

stdout carries JSON-RPC frames only; every diagnostic goes to stderr.

## Serve flags and environment

| Flag / env var | Default | Purpose |
|---|---|---|
| `-addr` / `WHATSAPP_MCP_ADDR` | `127.0.0.1:8765` | HTTP bind address |
| `-allow-remote` | off | Bind to a non-loopback address (requires `WHATSAPP_MCP_TOKEN`) |
| `WHATSAPP_MCP_TOKEN` | — | Bearer token for `/mcp` and `/pair/*` when remote-bound |
| `-read-only` / `WHATSAPP_MCP_READ_ONLY` | off | Expose only read/query tools (no send, edit, delete, mark-read, group, blocklist, privacy, status-change tools) |
| `-log-format` / `WHATSAPP_MCP_LOG_FORMAT` | `text` | `text` for human logs; `json` for structured JSON lines |
| `WHATSAPP_MCP_MEDIA_ROOT` | `<store>/uploads/` | File allowlist root for send/download tools |
| `WHATSAPP_MCP_DEVICE_NAME` | `WhatsApp MCP` | Name shown for this device in WhatsApp → Linked Devices |
| `WHATSAPP_MCP_FULL_HISTORY_SYNC` | off | Backfill older messages for all cached chats after pairing |
| `-debug` / `WHATSAPP_MCP_DEBUG` | off | Verbose (unredacted) logs |

### Observability

- `GET /healthz` — JSON: `connected`, `paired`, `state`, `uptime_sec`
- `GET /metrics` — Prometheus text format: messages in/out, sends, errors, rate-limited, pairing state, uptime

### Real-time push

- `WS /ws/events` — WebSocket broadcast of new-message events as JSON
- `GET /pair/events` — Server-Sent Events stream of pairing state transitions

## MCP tools reference

The server exposes **100 tools**, grouped by domain:

### Messaging

| Tool | Description |
|------|-------------|
| `send_message` | Send a text message to a person or group |
| `send_file` | Upload and send a picture, video, document, or attachment |
| `send_audio_message` | Send an audio file as a voice note (transcodes via ffmpeg if needed) |
| `send_sticker` | Send a WebP sticker file to a chat |
| `send_location` | Send a map location |
| `send_contact_card` | Send a contact card (vCard) |
| `send_poll` | Send a poll with 2–32 options |
| `send_poll_vote` | Cast a vote on a poll |
| `send_reaction` | Add or replace an emoji reaction on a message |
| `send_reply` | Send a reply that quotes a previous message |
| `edit_message` | Edit the text of a sent message |
| `delete_message` | Revoke (delete) a sent message |
| `recover_media` | Request WhatsApp to re-send media for a message |
| `transcribe_voice_note` | Transcribe a voice note via the configured STT endpoint |
| `batch_send_messages` | Send multiple messages in one call |
| `preview_send` | Dry-run a send: show the post-processed payload, and for a Status the audience, device count, and participant-list hash, without sending |
| `forward_message` | Forward a cached message to another chat |
| `reject_call` | Reject an incoming call using the call id from the [voice call] / [video call] row in the chat |

### Statuses (Stories)

| Tool | Description |
|------|-------------|
| `list_status_updates` | List cached Status updates grouped by sender |
| `mark_status_viewed` | Send read receipts for a contact's statuses |
| `send_status` | Post a Status update (image/video + caption) |
| `delete_status` | Delete one of your own status updates |
| `get_status_viewers` | Always fails: WhatsApp does not expose status viewer lists to linked devices. Check the viewer count on the phone |
| `set_status_privacy_setting` | Set who can see your statuses (`all`, `contacts`, `close_friends`, `custom`) |
| `get_status` | Read the paired account's current About/status text |
| `request_status_sync` | Ask the phone to backfill recent Status (Stories) updates into the cache |

`send_status` fails when WhatsApp does not accept the fan-out: the server
returns the participant-list hash it computed for your audience, and the post
is rejected when it differs from the hash this client declared. The error
carries both hashes and the message id. Use `preview_send` with
`recipient='status@broadcast'` to see the audience, its device count, and the
hash before posting.


### Profile & presence

| Tool | Description |
|------|-------------|
| `get_contact_info` | Live profile metadata (About, profile-picture ID, LID, devices, business verification) |
| `get_profile_picture` | Fetch a contact's or group's profile photo |
| `set_profile_picture` | Set the paired account's profile photo |
| `set_status_message` | Set the About text shown in the profile |
| `get_presence` | Query a contact's online/last-seen state |
| `send_typing` | Show or hide per-chat typing/recording indicator |
| `send_presence` | Set global online/offline availability |

### Groups

| Tool | Description |
|------|-------------|
| `create_group` | Create a group with participants |
| `leave_group` | Leave a group |
| `join_group_with_link` | Join a group via invite link |
| `get_group_info` | Get group metadata |
| `list_groups` | List the groups the account belongs to |
| `get_group_invite_link` | Get or reset a group invite link |
| `set_group_name` | Rename a group |
| `set_group_topic` | Set group topic/description |
| `set_group_announce` | Set announce-only mode |
| `set_group_locked` | Lock/unlock a group |
| `update_group_participants` | Add/remove/promote/demote participants |
| `set_group_photo` | Set the group's photo (admin) |
| `set_group_join_approval` | Require admin approval before new members join (admin) |
| `set_group_member_add_mode` | Choose who may add members: `admin_add` or `all_member_add` (admin) |
| `list_group_join_requests` | List pending requests to join an approval-gated group (admin) |
| `resolve_group_join_request` | Approve or reject pending join requests (admin) |
| `mute_chat` | Mute/unmute a chat |
| `pin_chat` | Pin/unpin a chat in the chat list |

### Channels

| Tool | Description |
|------|-------------|
| `list_channels` | List followed WhatsApp Channels |
| `follow_channel` | Follow a channel |
| `unfollow_channel` | Unfollow a channel |
| `mute_channel` | Mute/unmute a channel |
| `get_channel_info` | Get channel metadata |
| `list_channel_messages` | List messages from a channel |
| `send_channel_message` | Post a message to a channel you administer. Public and irreversible: every follower sees it at once and there is no delete |
| `create_channel` | Create a new channel with a name and description |

### Contacts

| Tool | Description |
|------|-------------|
| `list_contacts` | List contacts from the synced WhatsApp address book (saved, business, and push names), with cursor pagination. Local bridge contacts take precedence on name. |
| `search_contacts` | Search all cached contacts by name, phone, JID, or LID, with cursor pagination |
| `resolve_contacts` | Resolve a batch of identifiers (phone, JID, LID, or name) to canonical identities with provenance |
| `diagnose_contacts` | Diagnostic summary: counts by name source, unmapped LIDs, stale rows, last sync |
| `sync_contacts` | Force a full resync of the address book from WhatsApp (rate-limited, 10 min cooldown) |
| `get_sticker_pack` | Fetch a sticker pack by its id and list its stickers |
| `resync_app_state` | Force a full app-state resync for one patch (regular_low by default) when app-state writes fail with a 409 or a mismatching LThash |
| `create_contact` | Create a local (bridge-only) contact entry |
| `update_contact` | Update a local contact |
| `delete_contact` | Delete a local contact |
| `is_on_whatsapp` | Check if phone numbers are registered on WhatsApp |
| `get_contact_info` | Fetch live profile metadata (About, picture, LID, devices, business verification) |

### Blocklist & privacy

| Tool | Description |
|------|-------------|
| `get_blocklist` | List blocked contacts |
| `block_contact` | Block a contact |
| `unblock_contact` | Unblock a contact |
| `get_privacy_settings` | Read privacy settings |
| `set_privacy_setting` | Set a privacy setting by its protocol name (`groupadd`, `last`, `status`, `profile`, `readreceipts`, `online`, `calladd`, `messages`, `defense`, `stickers`) |

### Scheduling

| Tool | Description |
|------|-------------|
| `schedule_message` | Queue a text or media message for later delivery (timer-driven, survives restarts, retries a transient transport failure with a 30s-per-attempt backoff up to five attempts) |
| `cancel_scheduled_message` | Cancel a pending scheduled message |
| `list_scheduled_messages` | List queued messages with status, attempt count, and the failure reason when one is recorded |

### Auto-responder

| Tool | Description |
|------|-------------|
| `set_auto_responder` | Enable/disable automatic replies to incoming 1:1 text messages |
| `get_auto_responder` | Read current auto-responder configuration |

### Chat management

| Tool | Description |
|------|-------------|
| `mute_chat` | Mute/unmute a chat |
| `pin_chat` | Pin/unpin a chat |
| `archive_chat` | Archive a chat so it drops out of the default listing |
| `unarchive_chat` | Restore an archived chat to the default listing |
| `set_disappearing_timer` | Set disappearing-message timer |
| `star_message` | Star/unstar a message |

### History & queries

| Tool | Description |
|------|-------------|
| `list_chats` | List cached chats |
| `list_messages` | List messages in a chat |
| `list_chats_by_label` | List chats by label |
| `get_message_context` | Get context around a message |
| `get_chat` | Get chat metadata |
| `list_media_messages` | List messages with media |
| `download_media` | Download media for a message |
| `search_contacts` | Search contacts |
| `get_read_position` / `mark_read_position` | Read or advance the local read position in a chat (bridge-side bookkeeping; no message is sent) |
| `get_poll_results` | Read the current tally of a poll |
| `list_labels` | List the labels on the account |
| `list_chats_by_label` | List the chats carrying one label |
| `request_sync` | Ask the phone to backfill older messages for a chat |
| `mark_chat_read` / `mark_read` | Mark messages as read |
| `mark_read_position` | Mark read position |

### Pairing & audit

| Tool | Description |
|------|-------------|
| `pairing_status` | Return pairing state |
| `get_audit_log` | Return audit-log entries |

## Statuses (Stories) — push-only delivery, recoverable via re-pair

Status updates arrive as **push broadcasts** while the daemon is online and paired. There is no on-demand server fetch for statuses on an already-paired device — `WAWebApiStatus.getAllStatuses` reads from local cache only.

**Missing statuses can be recovered by re-pairing.** When a linked device pairs for the first time, WhatsApp sends `INITIAL_STATUS_V3` (history sync type 1) which carries recent status updates. The daemon imports `HistorySync.statusV3Messages` (field 3) into `status@broadcast` rows, resolves the poster via `key.participant` / `WebMessageInfo.participant` (LID → phone number), and keeps placeholders for pruned payloads.

To recover statuses posted while the daemon was offline:

1. Log out the MCP device from your phone (WhatsApp → Linked Devices → "WhatsApp MCP" → Log out)
2. Restart the daemon: `systemctl --user restart whatsapp-mcp.service`
3. Scan the fresh QR code at `http://127.0.0.1:8765/pair`
4. During pairing bootstrap, WhatsApp sends `INITIAL_STATUS_V3` and the daemon caches recent statuses automatically

Profile photos and online/last-seen presence work independently of status delivery.

## Safety

This tool connects an AI agent to your personal WhatsApp account. Treat it accordingly:

- **Loopback only by default.** The daemon binds to `127.0.0.1`. Do not expose it remotely without `WHATSAPP_MCP_TOKEN`.
- **Read-only mode.** Run with `-read-only` if you only need the agent to read and summarize chats. It strips every send, edit, delete, mark-read, group, blocklist, privacy, and status-change tool.
- **Untrusted input.** Incoming message text is data fed to an LLM. Prompt injection could exfiltrate chat history. Do not assume message content is safe.
- **Contacts did not consent.** Their messages to you enter the agent's context window and go to the model provider.

## Media

`WHATSAPP_MCP_MEDIA_ROOT` (default `<store>/uploads/`) is the only directory send/download tools may touch. Paths outside the root, including symlinks that resolve outside it, are rejected.

## Operations

### systemd (Linux)

The project includes a systemd user unit at `docs/systemd/whatsapp-mcp.service`. Install it:

```bash
mkdir -p ~/.config/systemd/user
cp docs/systemd/whatsapp-mcp.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now whatsapp-mcp.service
```

### launchd (macOS)

A launchd plist is provided at `docs/launchd/com.sealjay.whatsapp-mcp.plist`.

### Webhooks

The daemon optionally verifies `/webhook/` signatures using `WHATSAPP_WEBHOOK_SECRET`.

## Development

```bash
make build        # writes ./bin/whatsapp-mcp
make test         # unit tests
make test-race    # with -race
make test-cover   # coverage report
make vet          # go vet
make fmt          # gofmt check
make lint         # vet + fmt
make smoke        # boot-test without connecting to WhatsApp
make e2e          # end-to-end tests (requires paired daemon)
make upgrade-check # bump go.mau.fi/whatsmeow, re-tidy, build, test
```

### Pair for integration testing

```bash
./bin/whatsapp-mcp login
```

There is no offline fixture for whatsmeow sessions; integration testing requires a real paired WhatsApp account.

### Contributing

- Open an issue before starting non-trivial work so we can agree on scope.
- One logical change per pull request. Keep diffs focused and reviewable.
- Match existing code style in Go (`cmd/`, `internal/`).
- Do not commit `.env` files, database files (`*.db`), session state, or media.
- Do not re-add `.claude/` or `docs/superpowers/` — those were removed from the repo.
- If you add a new tool, update `expectedToolNames` and the tool count in `internal/mcp/tools_test.go`.
- If you add a new env var or serve flag, document it in `README.md` under "Serve flags and environment".

## License

MIT. See [LICENSE](LICENSE).