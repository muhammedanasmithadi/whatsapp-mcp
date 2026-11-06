// Package store owns the SQLite-backed message/chat cache and the mapping layer
// to whatsmeow's own SQLite database. The schema is kept byte-compatible with
// the historical layout so existing store/messages.db files keep working.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// ChmodBestEffort tightens a path's permissions but tolerates filesystems that
// do not support chmod. Network mounts such as Azure Files / CIFS SMB return
// EPERM/ENOTSUP/EINVAL from chmod(2) — on those mounts access is governed by
// the mount options (uid/gid/file_mode), not POSIX mode bits, so a rejected
// chmod is not a security regression. A local filesystem never returns these
// errors for a file the process owns, so genuine permission tightening on
// local disk is preserved. Missing files are likewise tolerated.
func ChmodBestEffort(path string, mode os.FileMode) error {
	err := os.Chmod(path, mode)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

// Message mirrors the on-wire shape used by downstream consumers.
type Message struct {
	ID          string    `json:"id"`
	ChatJID     string    `json:"chat_jid"`
	Sender      string    `json:"sender"`                 // bare phone/user part
	SenderPhone string    `json:"sender_phone,omitempty"` // full phone if resolvable
	Content     string    `json:"content"`
	Timestamp   time.Time `json:"timestamp"`
	IsFromMe    bool      `json:"is_from_me"`
	IsGroup     bool      `json:"is_group"`
	MediaType   string    `json:"media_type,omitempty"`
	Filename    string    `json:"filename,omitempty"`
	URL         string    `json:"url,omitempty"`
	// DirectPath is the CDN path (starting with `/`, including the signed
	// `?ccb=&oh=&oe=&_nc_sid=` auth query) captured verbatim from the
	// WhatsApp media protobuf at receive/send time. When present, the
	// download path uses it directly; when absent (legacy row written by
	// an older daemon build), Download falls back to parsing URL.
	DirectPath  string `json:"direct_path,omitempty"`
	ChatName    string `json:"chat_name,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
}

// Chat is a cached WhatsApp chat.
type Chat struct {
	JID             string    `json:"jid"`
	Name            string    `json:"name"`
	LastMessageTime time.Time `json:"last_message_time"`
	LastMessage     string    `json:"last_message,omitempty"`
	LastSender      string    `json:"last_sender,omitempty"`
	LastIsFromMe    bool      `json:"last_is_from_me"`
	PhoneNumber     string    `json:"phone_number,omitempty"`
	IsGroup         bool      `json:"is_group"`
}

// Contact is a directory entry derived from chat history.
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name,omitempty"`
	JID         string `json:"jid"`
}

// MessageContext wraps a target message with surrounding context.
type MessageContext struct {
	Message Message   `json:"message"`
	Before  []Message `json:"before,omitempty"`
	After   []Message `json:"after,omitempty"`
}

// MessageReadPosition tracks the last message the local agent has read (or
// otherwise processed) in a given chat. It is a client-side bookmark: WhatsApp
// itself does not expose read receipts for received messages to linked devices,
// so we store our own position so callers can resume walks without re-scanning
// from the top of a chat.
type MessageReadPosition struct {
	ChatJID           string    `json:"chat_jid"`
	LastReadMessageID string    `json:"last_read_message_id,omitempty"`
	LastReadAt        time.Time `json:"last_read_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

// ScheduledMessage is a message scheduled to be sent at a future time.
type ScheduledMessage struct {
	ID            string    `json:"id"`
	ChatJID       string    `json:"chat_jid"`
	Content       string    `json:"content"`
	ScheduledAt   time.Time `json:"scheduled_at"`
	Status        string    `json:"status"` // pending, sent, failed
	SentAt        time.Time `json:"sent_at,omitempty"`
	SentMessageID string    `json:"sent_message_id,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// stmtCache caches prepared SQL statements to avoid the overhead of
// re-preparing the same statement on every call. SQLite is a single-writer
// database, so with MaxOpenConns(1) there is no risk of concurrent
// preparation of the same statement by multiple connections.
type stmtCache struct {
	mu    sync.RWMutex
	db    *sql.DB
	stmts map[string]*sql.Stmt
}

// newStmtCache creates a statement cache bound to the given database.
func newStmtCache(db *sql.DB) *stmtCache {
	return &stmtCache{
		db:    db,
		stmts: make(map[string]*sql.Stmt),
	}
}

// prepare gets or creates a prepared statement for the given query.
// The statement is cached for reuse across calls. Callers must call
// stmt.Close() when done; the cache retains the underlying statement
// for reuse by subsequent callers.
func (sc *stmtCache) prepare(ctx context.Context, query string) (*sql.Stmt, error) {
	sc.mu.RLock()
	stmt, ok := sc.stmts[query]
	sc.mu.RUnlock()
	if ok {
		return stmt, nil
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	// Double-check after acquiring write lock.
	if stmt, ok := sc.stmts[query]; ok {
		return stmt, nil
	}
	stmt, err := sc.db.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	sc.stmts[query] = stmt
	return stmt, nil
}

// close closes all cached prepared statements.
func (sc *stmtCache) close() error {
	if sc == nil || sc.stmts == nil {
		return nil
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var firstErr error
	for _, stmt := range sc.stmts {
		if err := stmt.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	sc.stmts = make(map[string]*sql.Stmt)
	return firstErr
}

// Store handles the message cache and read-only access to whatsmeow's DB.
type Store struct {
	db          *sql.DB
	whatsmeowDB *sql.DB
	dir         string
	stmtCache   *stmtCache
}

// Open creates (if necessary) the store directory and opens both databases.
// Dir is the directory holding messages.db and whatsapp.db. The directory is
// created/tightened to 0700 and each .db file ends up at 0600 to keep session
// material and cached message contents off other local users' radar.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	if err := ChmodBestEffort(dir, 0o700); err != nil {
		return nil, fmt.Errorf("chmod store dir: %w", err)
	}
	msgPath := filepath.Join(dir, "messages.db")
	waPath := filepath.Join(dir, "whatsapp.db")

	db, err := sql.Open("sqlite3", "file:"+msgPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_cache_size=-64000&_mmap_size=268435456")
	if err != nil {
		return nil, fmt.Errorf("open messages db: %w", err)
	}
	// SQLite is a single-writer, multi-reader database. Limit open connections
	// to 1 to avoid "database is locked" errors under concurrent access from
	// the MCP HTTP handler, the history-sync goroutine, and the media downloader.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	// Tighten messages.db after schema has been applied (the file exists
	// by then). IsNotExist-tolerate in case a future sqlite driver version
	// defers file creation.
	if err := ChmodBestEffort(msgPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod messages.db: %w", err)
	}

	// whatsapp.db may not yet exist on first run; open lazily in read-only mode.
	waDB, err := sql.Open("sqlite3", "file:"+waPath+"?mode=ro")
	if err != nil {
		// Non-fatal: LID resolution falls through to identity.
		waDB = nil
	}
	// If whatsapp.db already exists (from a previous login), tighten it.
	if err := ChmodBestEffort(waPath, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("chmod whatsapp.db: %w", err)
	}

	return &Store{db: db, whatsmeowDB: waDB, dir: dir, stmtCache: newStmtCache(db)}, nil
}

// DB returns the underlying message database handle (tests only).
func (s *Store) DB() *sql.DB { return s.db }

// PrepareContext returns a cached prepared statement for the given query.
// The statement is reused across calls; callers must call stmt.Close() when
// done. The cache retains the underlying statement for reuse.
func (s *Store) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return s.stmtCache.prepare(ctx, query)
}

// closeStmtCache closes all cached prepared statements.
func (s *Store) closeStmtCache() error {
	return s.stmtCache.close()
}

// Dir returns the directory backing the store.
func (s *Store) Dir() string { return s.dir }

// Close releases both database connections and all cached prepared statements.
func (s *Store) Close() error {
	if s.whatsmeowDB != nil {
		_ = s.whatsmeowDB.Close()
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			return err
		}
	}
	return s.closeStmtCache()
}

// ListChatJIDs returns all cached chat JIDs. Used by the full history-sync
// opt-in to request backfill for every chat after pairing.
func (s *Store) ListChatJIDs() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT chat_jid FROM messages")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}

// StoreChat upserts a chat row.
func (s *Store) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, lastMessageTime,
	)
	return err
}

// EnsureChat creates a chat row if the JID is not cached yet, preserving any
// existing row (name, last message time, archive flag). Scheduled messages
// reference chats, so scheduling to a brand-new recipient needs this first.
func (s *Store) EnsureChat(jid, name string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, time.Time{},
	)
	return err
}

// ArchiveChat archives a chat so it no longer appears in normal chat listings.
// Read-only chats remain in the database and can be unarchived later.
func (s *Store) ArchiveChat(chatJID string) error {
	_, err := s.db.Exec("UPDATE chats SET archived = 1 WHERE jid = ?", chatJID)
	return err
}

// UnarchiveChat unarchives a previously archived chat.
func (s *Store) UnarchiveChat(chatJID string) error {
	_, err := s.db.Exec("UPDATE chats SET archived = 0 WHERE jid = ?", chatJID)
	return err
}

// StoreMessage inserts or replaces a message row. Messages with no content and
// no media type are dropped to avoid polluting the cache with signal-only
// events (typing, receipts, key exchange, etc.).
func (s *Store) StoreMessage(ctx context.Context, m Message, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	if m.Content == "" && m.MediaType == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, direct_path, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Content, m.Timestamp, m.IsFromMe,
		m.MediaType, m.Filename, m.URL, m.DirectPath, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)
	return err
}

// UpdateMessage replaces the content of an existing message row. Used when
// WhatsApp delivers an edit event for a previously-cached message. Returns
// sql.ErrNoRows when no matching row exists.
func (s *Store) UpdateMessage(ctx context.Context, id, chatJID, content string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE messages SET content = ? WHERE id = ? AND chat_jid = ?",
		content, id, chatJID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RevokeMessage marks a message as deleted-for-everyone in the local cache.
// The content is replaced with a placeholder and the media fields are cleared
// so downstream consumers (list_messages, download_media) treat it as revoked.
// Returns sql.ErrNoRows when no matching row exists.
func (s *Store) RevokeMessage(ctx context.Context, id, chatJID string) error {
	result, err := s.db.ExecContext(ctx,
		"UPDATE messages SET content = '[deleted]', media_type = '', url = '', direct_path = '', media_key = NULL, file_sha256 = NULL, file_enc_sha256 = NULL, file_length = 0 WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// StoreMediaInfo updates the media columns of an existing message row.
func (s *Store) StoreMediaInfo(id, chatJID, url, directPath string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	_, err := s.db.Exec(
		"UPDATE messages SET url = ?, direct_path = ?, media_key = ?, file_sha256 = ?, file_enc_sha256 = ?, file_length = ? WHERE id = ? AND chat_jid = ?",
		url, directPath, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
	)
	return err
}

// GetMediaInfo fetches the persisted media fields for a message. direct_path
// is the CDN path captured from the WhatsApp media protobuf at receive time,
// including the signed `?ccb=&oh=&oe=&_nc_sid=` auth query. It may be empty
// for legacy rows written before this column existed — callers that need
// to fetch bytes should fall back to parsing url in that case.
func (s *Store) GetMediaInfo(id, chatJID string) (mediaType, filename, url, directPath string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64, err error) {
	var dp sql.NullString
	err = s.db.QueryRow(
		"SELECT media_type, filename, url, direct_path, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	).Scan(&mediaType, &filename, &url, &dp, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)
	directPath = dp.String
	return
}

// GetMessage returns one cached message by ID and chat JID, or nil when it is
// not cached.
func (s *Store) GetMessage(ctx context.Context, id, chatJID string) (*Message, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, chat_jid, sender, COALESCE(content, ''), timestamp, is_from_me
		FROM messages WHERE id = ? AND chat_jid = ?`, id, chatJID)
	var m Message
	if err := row.Scan(&m.ID, &m.ChatJID, &m.Sender, &m.Content, &m.Timestamp, &m.IsFromMe); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.IsGroup = strings.HasSuffix(m.ChatJID, "@g.us")
	return &m, nil
}

// GetNewestMessage returns the latest message row for a chat, used by the
// history-sync code to anchor the request.
func (s *Store) GetNewestMessage(chatJID string) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ?
		ORDER BY timestamp DESC
		LIMIT 1`, chatJID).Scan(&id, &ts, &isFromMe)
	return
}

// GetOldestMessage returns the earliest message row for a chat. It is the
// counterpart to GetNewestMessage and exists so the history-sync code can
// anchor a backward walk on a real cached message: WhatsApp resolves an
// on-demand history-sync cursor by message key, so extending history backwards
// requires the key of the oldest row we already hold, not a synthesised one.
func (s *Store) GetOldestMessage(chatJID string) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ?
		ORDER BY timestamp ASC
		LIMIT 1`, chatJID).Scan(&id, &ts, &isFromMe)
	return
}

// GetMessageAtOrBefore returns the newest message row at or before ts for a
// chat. It lets a caller anchor a history-sync request on a real cached
// message near a requested point in time rather than fabricating a key that
// the server cannot resolve. Returns sql.ErrNoRows when the chat has nothing
// at or before ts.
func (s *Store) GetMessageAtOrBefore(chatJID string, before time.Time) (id string, ts time.Time, isFromMe bool, err error) {
	err = s.db.QueryRow(`
		SELECT id, timestamp, is_from_me
		FROM messages
		WHERE chat_jid = ? AND timestamp <= ?
		ORDER BY timestamp DESC
		LIMIT 1`, chatJID, before).Scan(&id, &ts, &isFromMe)
	return
}

// HasInboundFrom reports whether the cache holds at least one message we
// received (is_from_me = 0) in the given chat. It is the "have we ever heard
// from this JID" signal the rate limiter uses to classify a send target as a
// known contact (leash) versus cold outreach (stricter leash). A group JID is
// always a known context and callers treat it as such without querying here.
func (s *Store) HasInboundFrom(chatJID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM messages WHERE chat_jid = ? AND is_from_me = 0 LIMIT 1)",
		chatJID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// StoreLabel upserts a label row.
func (s *Store) StoreLabel(labelID, name, color string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO labels (label_id, name, color) VALUES (?, ?, ?)",
		labelID, name, color,
	)
	return err
}

// StoreLabelAssociation upserts a label association for a chat.
func (s *Store) StoreLabelAssociation(chatJID, labelID string, labeled bool) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO label_associations (chat_jid, label_id, labeled) VALUES (?, ?, ?)",
		chatJID, labelID, labeled,
	)
	return err
}

// ListLabels returns all known labels.
func (s *Store) ListLabels() ([]map[string]string, error) {
	rows, err := s.db.Query("SELECT label_id, name, color FROM labels ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []map[string]string
	for rows.Next() {
		var labelID, name, color string
		if err := rows.Scan(&labelID, &name, &color); err != nil {
			return nil, err
		}
		labels = append(labels, map[string]string{
			"label_id": labelID,
			"name":     name,
			"color":    color,
		})
	}
	return labels, rows.Err()
}

// ListChatsByLabel returns all chat JIDs that have the given label.
func (s *Store) ListChatsByLabel(labelID string) ([]string, error) {
	rows, err := s.db.Query(
		"SELECT chat_jid FROM label_associations WHERE label_id = ? AND labeled = 1",
		labelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	return jids, rows.Err()
}

// AuditLogEntry is a single entry in the audit log of agent-initiated actions.
type AuditLogEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	ToolName  string    `json:"tool_name"`
	ChatJID   string    `json:"chat_jid,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Action    string    `json:"action"`
	Result    string    `json:"result"`
	Details   string    `json:"details,omitempty"`
}

// AuditLog records an agent-initiated action in the append-only audit log.
func (s *Store) AuditLog(toolName, chatJID, messageID, action, result, details string) error {
	_, err := s.db.Exec(
		"INSERT INTO audit_log (tool_name, chat_jid, message_id, action, result, details) VALUES (?, ?, ?, ?, ?, ?)",
		toolName, chatJID, messageID, action, result, details,
	)
	return err
}

// ListAuditLog returns the most recent audit log entries, limited by count.
func (s *Store) ListAuditLog(limit int) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		"SELECT id, timestamp, tool_name, chat_jid, message_id, action, result, details FROM audit_log ORDER BY id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.ToolName, &e.ChatJID, &e.MessageID, &e.Action, &e.Result, &e.Details); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// StoreContact upserts a contact row.
func (s *Store) StoreContact(jid, name, phoneNumber string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO contacts (jid, name, phone_number, updated_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)",
		jid, name, phoneNumber,
	)
	return err
}

// GetContact returns a contact by JID.
func (s *Store) GetContact(jid string) (*Contact, error) {
	row := s.db.QueryRow("SELECT jid, name, phone_number FROM contacts WHERE jid = ?", jid)
	var c Contact
	if err := row.Scan(&c.JID, &c.Name, &c.PhoneNumber); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// ListContacts returns all saved contacts ordered by name.
func (s *Store) ListContacts() ([]Contact, error) {
	rows, err := s.db.Query("SELECT jid, name, phone_number FROM contacts ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var contacts []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.JID, &c.Name, &c.PhoneNumber); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// DeleteContact deletes a contact by JID.
func (s *Store) DeleteContact(jid string) error {
	_, err := s.db.Exec("DELETE FROM contacts WHERE jid = ?", jid)
	return err
}

// StoreScheduledMessage inserts a scheduled message.
func (s *Store) StoreScheduledMessage(id, chatJID, content string, scheduledAt time.Time) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO scheduled_messages (id, chat_jid, content, scheduled_at) VALUES (?, ?, ?, ?)",
		id, chatJID, content, scheduledAt,
	)
	return err
}

// NextScheduledMessageTime returns the scheduled time of the earliest pending
// scheduled message. ok is false when nothing is pending.
func (s *Store) NextScheduledMessageTime() (time.Time, bool, error) {
	var next time.Time
	err := s.db.QueryRow(`
		SELECT scheduled_at FROM scheduled_messages
		WHERE status = 'pending'
		ORDER BY scheduled_at ASC LIMIT 1
	`).Scan(&next)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return next, true, nil
}

// GetPendingScheduledMessages returns all scheduled messages with status 'pending'
// whose scheduled_at is <= now.
func (s *Store) GetPendingScheduledMessages(ctx context.Context) ([]ScheduledMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chat_jid, content, scheduled_at FROM scheduled_messages
		WHERE status = 'pending' AND scheduled_at <= ?
		ORDER BY scheduled_at ASC
	`, time.Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []ScheduledMessage
	for rows.Next() {
		var m ScheduledMessage
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Content, &m.ScheduledAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// MarkMessageSent marks a scheduled message as sent.
func (s *Store) MarkMessageSent(id, messageID string, sentAt time.Time) error {
	_, err := s.db.Exec(
		"UPDATE scheduled_messages SET status = 'sent', sent_at = ?, sent_message_id = ? WHERE id = ?",
		sentAt, messageID, id,
	)
	return err
}

// MarkMessageFailed marks a scheduled message as failed.
func (s *Store) MarkMessageFailed(id, reason string) error {
	_, err := s.db.Exec(
		"UPDATE scheduled_messages SET status = 'failed', error = ? WHERE id = ?",
		reason, id,
	)
	return err
}

// ListScheduledMessages returns scheduled messages, newest scheduled time
// first. status filters to pending, sent, or failed; an empty string returns
// every state.
func (s *Store) ListScheduledMessages(status string) ([]ScheduledMessage, error) {
	query := `SELECT id, chat_jid, content, scheduled_at, status,
		sent_at, COALESCE(sent_message_id, ''), COALESCE(error, '')
		FROM scheduled_messages`
	args := []any{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	query += " ORDER BY scheduled_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []ScheduledMessage
	for rows.Next() {
		var (
			m      ScheduledMessage
			sentAt sql.NullTime
		)
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.Content, &m.ScheduledAt, &m.Status, &sentAt, &m.SentMessageID, &m.Error); err != nil {
			return nil, err
		}
		if sentAt.Valid {
			m.SentAt = sentAt.Time
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// CancelScheduledMessage deletes a pending scheduled message. It reports
// whether a pending row was removed (false means the ID was unknown or the
// message had already been sent or failed).
func (s *Store) CancelScheduledMessage(id string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM scheduled_messages WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FindChatName returns the name stored for a chat JID, empty if none.
func (s *Store) FindChatName(jid string) string {
	var name string
	_ = s.db.QueryRow("SELECT name FROM chats WHERE jid = ?", jid).Scan(&name)
	return name
}

// RecentIncomingMessages returns up to `limit` recent incoming message IDs +
// their sender JIDs for the given chat. Used to drive the "mark whole chat as
// read" helper — we don't track per-message read state locally, so we treat
// recent incoming messages as the candidates to ack.
func (s *Store) RecentIncomingMessages(ctx context.Context, chatJID string, limit int) (ids []string, senders []string, err error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sender
		FROM messages
		WHERE chat_jid = ? AND is_from_me = 0
		ORDER BY timestamp DESC
		LIMIT ?`, chatJID, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, sender string
		if err := rows.Scan(&id, &sender); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		senders = append(senders, sender)
	}
	return ids, senders, rows.Err()
}

// GetReadPosition returns the stored read position for a chat, or
// sql.ErrNoRows when no position has been recorded yet.
func (s *Store) GetReadPosition(ctx context.Context, chatJID string) (*MessageReadPosition, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT chat_jid, last_read_message_id, last_read_at, updated_at
		FROM message_read_positions
		WHERE chat_jid = ?`, chatJID)
	var p MessageReadPosition
	var lastReadAt, updatedAt sql.NullTime
	if err := row.Scan(&p.ChatJID, &p.LastReadMessageID, &lastReadAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("get read position: %w", err)
	}
	if lastReadAt.Valid {
		p.LastReadAt = lastReadAt.Time
	}
	if updatedAt.Valid {
		p.UpdatedAt = updatedAt.Time
	}
	return &p, nil
}

// SetReadPosition upserts the read position for a chat. lastReadAt is the
// timestamp of the last message the caller has processed; if zero, the
// message's own timestamp is looked up and used.
func (s *Store) SetReadPosition(ctx context.Context, chatJID, messageID string, lastReadAt time.Time) error {
	if lastReadAt.IsZero() {
		err := s.db.QueryRowContext(ctx, `
			SELECT timestamp FROM messages WHERE id = ? AND chat_jid = ?`,
			messageID, chatJID).Scan(&lastReadAt)
		if err != nil {
			return fmt.Errorf("set read position: resolve message timestamp: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO message_read_positions (chat_jid, last_read_message_id, last_read_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(chat_jid) DO UPDATE SET
			last_read_message_id = excluded.last_read_message_id,
			last_read_at = excluded.last_read_at,
			updated_at = CURRENT_TIMESTAMP`,
		chatJID, messageID, lastReadAt,
	)
	if err != nil {
		return fmt.Errorf("set read position: %w", err)
	}
	return nil
}

const schema = `
CREATE TABLE IF NOT EXISTS chats (
	jid TEXT PRIMARY KEY,
	name TEXT,
	last_message_time TIMESTAMP,
	archived BOOLEAN NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT,
	chat_jid TEXT,
	sender TEXT,
	content TEXT,
	timestamp TIMESTAMP,
	is_from_me BOOLEAN,
	media_type TEXT,
	filename TEXT,
	url TEXT,
	direct_path TEXT,
	media_key BLOB,
	file_sha256 BLOB,
	file_enc_sha256 BLOB,
	file_length INTEGER,
	poll_options_json TEXT,
	PRIMARY KEY (id, chat_jid),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS poll_votes (
	poll_message_id TEXT NOT NULL,
	poll_chat_jid TEXT NOT NULL,
	voter_jid TEXT NOT NULL,
	selected_options_json TEXT NOT NULL,
	timestamp TIMESTAMP NOT NULL,
	PRIMARY KEY (poll_message_id, poll_chat_jid, voter_jid)
);

CREATE TABLE IF NOT EXISTS labels (
	label_id TEXT PRIMARY KEY,
	name TEXT,
	color TEXT
);

CREATE TABLE IF NOT EXISTS label_associations (
	chat_jid TEXT NOT NULL,
	label_id TEXT NOT NULL,
	labeled BOOLEAN NOT NULL DEFAULT 1,
	PRIMARY KEY (chat_jid, label_id),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid),
	FOREIGN KEY (label_id) REFERENCES labels(label_id)
);

CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	tool_name TEXT NOT NULL,
	chat_jid TEXT,
	message_id TEXT,
	action TEXT NOT NULL,
	result TEXT NOT NULL,
	details TEXT
);

CREATE TABLE IF NOT EXISTS message_read_positions (
	chat_jid TEXT PRIMARY KEY,
	last_read_message_id TEXT,
	last_read_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS contacts (
	jid TEXT PRIMARY KEY,
	name TEXT,
	phone_number TEXT,
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS scheduled_messages (
	id TEXT PRIMARY KEY,
	chat_jid TEXT NOT NULL,
	content TEXT NOT NULL,
	scheduled_at TIMESTAMP NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	sent_at TIMESTAMP,
	sent_message_id TEXT,
	error TEXT,
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);

CREATE TABLE IF NOT EXISTS presence (
	jid TEXT PRIMARY KEY,
	online BOOLEAN NOT NULL DEFAULT 0,
	last_seen TIMESTAMP,
	subscribed_at TIMESTAMP,
	updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_scheduled_messages_scheduled_at ON scheduled_messages(scheduled_at);
CREATE INDEX IF NOT EXISTS idx_messages_chat_timestamp ON messages(chat_jid, timestamp DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_messages_sender_timestamp ON messages(sender, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_messages_media_type ON messages(media_type, timestamp DESC) WHERE media_type != '';
CREATE INDEX IF NOT EXISTS idx_messages_is_from_me ON messages(is_from_me, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp DESC);

`

// migrateSchema brings legacy databases up to the current layout. SQLite lacks
// "ADD COLUMN IF NOT EXISTS", so we inspect PRAGMA table_info before issuing
// any ALTER. Every step is idempotent — running migrateSchema twice is a
// no-op on an up-to-date DB.
func migrateSchema(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return fmt.Errorf("pragma table_info(messages): %w", err)
	}
	defer rows.Close()
	hasPollOptions := false
	hasDirectPath := false
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scan table_info(messages): %w", err)
		}
		if name == "poll_options_json" {
			hasPollOptions = true
		}
		if name == "direct_path" {
			hasDirectPath = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info(messages): %w", err)
	}
	if !hasPollOptions {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN poll_options_json TEXT"); err != nil {
			return fmt.Errorf("add column poll_options_json: %w", err)
		}
	}
	if !hasDirectPath {
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN direct_path TEXT"); err != nil {
			return fmt.Errorf("add column direct_path: %w", err)
		}
	}
	// chats table: add archived column if missing (legacy databases)
	chatRows, err := db.Query("PRAGMA table_info(chats)")
	if err != nil {
		return fmt.Errorf("pragma table_info(chats): %w", err)
	}
	defer chatRows.Close()
	hasArchived := false
	for chatRows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := chatRows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			return fmt.Errorf("scan table_info(chats): %w", err)
		}
		if name == "archived" {
			hasArchived = true
		}
	}
	if err := chatRows.Err(); err != nil {
		return fmt.Errorf("iterate table_info(chats): %w", err)
	}
	if !hasArchived {
		if _, err := db.Exec("ALTER TABLE chats ADD COLUMN archived BOOLEAN NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add column archived: %w", err)
		}
	}
	// scheduled_messages table: add the error column used to record send
	// failures (missing from the first release of the scheduler schema).
	if err := chatRows.Close(); err != nil {
		return fmt.Errorf("close table_info(chats): %w", err)
	}
	schedRows, err := db.Query("PRAGMA table_info(scheduled_messages)")
	if err != nil {
		return fmt.Errorf("pragma table_info(scheduled_messages): %w", err)
	}
	hasScheduleError := false
	for schedRows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notnull    int
			dfltValue  sql.NullString
			primaryKey int
		)
		if err := schedRows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &primaryKey); err != nil {
			schedRows.Close()
			return fmt.Errorf("scan table_info(scheduled_messages): %w", err)
		}
		if name == "error" {
			hasScheduleError = true
		}
	}
	if err := schedRows.Err(); err != nil {
		schedRows.Close()
		return fmt.Errorf("iterate table_info(scheduled_messages): %w", err)
	}
	if err := schedRows.Close(); err != nil {
		return fmt.Errorf("close table_info(scheduled_messages): %w", err)
	}
	// A legacy database in tests may predate the scheduler table entirely.
	// Open() applies the full schema afterwards, so only migrate when present.
	var schedTable string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='scheduled_messages'").Scan(&schedTable); err == nil {
		if !hasScheduleError {
			if _, err := db.Exec("ALTER TABLE scheduled_messages ADD COLUMN error TEXT"); err != nil {
				return fmt.Errorf("add column error: %w", err)
			}
		}
	}
	return nil
}
