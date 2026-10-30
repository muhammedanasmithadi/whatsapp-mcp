# Contributing

Thanks for your interest in contributing!

## About this project

This repository is an MCP (Model Context Protocol) server that **wraps the [whatsmeow](https://github.com/tulir/whatsmeow) Go library** to expose a personal WhatsApp account to LLMs. The Go bridge is a thin layer over whatsmeow that adds:

- A local SQLite store for message history
- LID resolution, disappearing-message handling, and targeted history sync
- A read-only mode that strips every send/edit/delete/mark/group/privacy tool
- An MCP surface served over HTTP via a lightweight daemon, plus `/healthz`, `/metrics`, WebSocket `/ws/events`, and SSE `/pair/events`

When making changes, keep this framing in mind: anything related to the underlying WhatsApp protocol, session management, or media upload/download should usually be solved upstream in whatsmeow, not reimplemented here.

## Ground rules

- Open an issue before starting non-trivial work so we can agree on scope.
- One logical change per pull request. Keep diffs focused and reviewable.
- Match existing code style in Go (`cmd/`, `internal/`).
- Do not commit `.env` files, database files (`*.db`), session state, or media.
- Do not re-add `.claude/` or `docs/superpowers/` — those were removed from the repo.

## Development workflow

1. Fork the repo and create a branch off `main`.
2. Build and test:

   ```bash
   make build        # writes ./bin/whatsapp-mcp
   make test         # unit tests
   make test-race    # race detector
   make vet          # go vet
   make upgrade-check  # bump go.mau.fi/whatsmeow, re-tidy, build, test
   ```

3. Pair your phone for integration testing:

   ```bash
   ./bin/whatsapp-mcp login
   ```

4. Test against a real WhatsApp account — there is no offline fixture for whatsmeow sessions.

## Pull requests

- Describe the user-facing change and the rationale in the PR description.
- Link the issue it closes, if any.
- Call out any breaking changes to MCP tool signatures, database schema, or the `pairDriver` interface.
- If you add a new tool, update `expectedToolNames` and the tool count in `internal/mcp/tools_test.go`.
- If you add a new env var or serve flag, document it in `README.md` under "Serve flags and environment".

## Rate-limiter design

The daemon-side `ratelimit` package is defense-in-depth against an LLM fanning out sends faster than WhatsApp tolerates. It is a single sliding-window counter shared by **all** outbound actions: `send_message`, `send_file`, `send_audio_message`, `send_poll`, `send_poll_vote`, `send_contact_card`, `send_reaction`, `send_reply`, `edit_message`, `delete_message`, `send_typing`, and `send_presence`. A denied decision does not consume budget.

- `checkSendRate` covers message sends.
- `checkActionRate` covers reactions, replies, edits, revokes, typing, polls, and cards — it calls `AllowSend` on the same limiter so an LLM cannot bypass the send cap by switching to reactions.
- Sent messages, replies, and reactions are cached in SQLite via `persistSent` / `persistReaction` so local history stays complete.

If you touch the rate limiter, update `internal/ratelimit/ratelimit.go` and `internal/client/features.go` / `internal/client/features_media.go` / `internal/client/send.go` consistently.

## Pairing state machine

The daemon owns pairing via `internal/daemon/pair_cache.go` and `internal/daemon/server.go`. Key behaviors:

- `StartPairing` retries with exponential backoff (2s → 30s cap) when the QR channel closes without success — WhatsApp rotates sessions roughly every 20 days.
- `onLoggedOut` runs in a separate goroutine so whatsmeow's event loop is not blocked.
- `handlerInstalled` on the production driver prevents stacking up logged-out handlers on reconnect.

If you modify pairing, test the `awaiting_qr` → `paired` → `error` transitions via `internal/daemon/pair_cache_test.go` and `internal/daemon/server_test.go`.

## Safety boundaries

### Read-only mode

Run with `-read-only` or `WHATSAPP_MCP_READ_ONLY=1`. The daemon registers only read/query tools — every send, edit, delete, mark-read, group mutation, blocklist mutation, privacy, and status-change tool is absent. This is the strongest mitigation against prompt injection. If you add a new tool, decide whether it belongs in the read-only subset (`registerQueryTools` path) or the full set.

### Lethal trifecta

Incoming message text is untrusted data fed to an LLM with tool access. Prompt injection in a message could exfiltrate chat history. Mitigations already in place:

- Loopback-only binding by default (`127.0.0.1:8765`).
- Media path allowlisting (`WHATSAPP_MCP_MEDIA_ROOT`); symlinks resolving outside are rejected.
- Log redaction: JIDs, chat names, contact names, and message bodies are redacted in stderr logs unless `-debug` is set.
- Read-only mode strips all mutation tools.

## whatsmeow upstream boundary

Because this project wraps whatsmeow, some classes of bug belong upstream:

- Protocol-level issues (auth failures, message decryption, presence) → [whatsmeow issues](https://github.com/tulir/whatsmeow/issues)
- MCP surface, storage, LID resolution, rate limiting, pairing state machine → this repo

If you're unsure, open an issue here first and we'll triage.

## Windows

`go-sqlite3` requires CGO at compile time. On Windows, CGO is disabled by default. See [docs/windows.md](docs/windows.md) for the MSYS2 setup and `go env -w CGO_ENABLED=1` step.

## Licence

By contributing, you agree that your contributions will be licensed under the [MIT Licence](LICENSE).