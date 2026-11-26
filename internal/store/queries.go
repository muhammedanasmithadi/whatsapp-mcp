package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// listMessageColumns is the 8-column projection shared by ListMessages and
// queryContextWindow.
const listMessageColumns = "messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type"

// ListMessagesParams holds the filters accepted by ListMessages. Zero values
// mean "unset" except for Limit, which defaults to 20 when <= 0.
type ListMessagesParams struct {
	After          time.Time
	Before         time.Time
	SenderPhone    string
	ChatJID        string
	Query          string
	Limit          int
	Page           int
	Cursor         string
	IncludeContext bool
	ContextBefore  int
	ContextAfter   int
}

// ListMessagesResult is the output of ListMessagesCursor: a page of messages
// plus keyset-pagination metadata so callers can resume walks without offset
// drift.
type ListMessagesResult struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
	Count      int       `json:"count"`
}

// cursorPayload encodes the (timestamp, id) position of the last message in a
// page so the next call can resume keyset pagination from exactly that point.
// Timestamps are stored as "2006-01-02 15:04:05" to match the on-disk format;
// binding a time.Time would append a "+00:00" timezone suffix that breaks the
// TEXT comparison at the boundary (the stored value becomes a prefix of the
// bound value and incorrectly compares as less-than).
type cursorPayload struct {
	T string `json:"t"` // "2006-01-02 15:04:05" UTC timestamp
	I string `json:"i"` // message id
}

// encodeCursor returns an opaque cursor string for the given timestamp + id.
func encodeCursor(ts time.Time, id string) string {
	p := cursorPayload{T: ts.UTC().Format("2006-01-02 15:04:05"), I: id}
	return base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%s", p.T, p.I)))
}

// decodeCursor reverses encodeCursor, returning the timestamp string and message id.
// The timestamp is returned as a string (not time.Time) so it can be bound to
// SQLite without a timezone suffix.
func decodeCursor(cursor string) (ts string, id string, err error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", fmt.Errorf("decode cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid cursor format")
	}
	return parts[0], parts[1], nil
}

// ListMessages returns messages matching the given filters, optionally expanded
// with surrounding context messages when IncludeContext is true.
func (s *Store) ListMessages(ctx context.Context, params ListMessagesParams) ([]Message, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	page := params.Page
	if page < 0 {
		page = 0
	}

	var (
		whereClauses []string
		args         []any
	)

	if !params.After.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp > ?")
		args = append(args, params.After)
	}
	if !params.Before.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp < ?")
		args = append(args, params.Before)
	}
	if params.SenderPhone != "" {
		whereClauses = append(whereClauses, "messages.sender = ?")
		args = append(args, params.SenderPhone)
	}
	if params.ChatJID != "" {
		whereClauses = append(whereClauses, "messages.chat_jid = ?")
		args = append(args, params.ChatJID)
	}
	if params.Query != "" {
		whereClauses = append(whereClauses, "LOWER(messages.content) LIKE LOWER(?) ESCAPE '\\'")
		args = append(args, "%"+escapeLike(params.Query)+"%")
	}

	var b strings.Builder
	b.WriteString("SELECT " + listMessageColumns + " FROM messages")
	b.WriteString(" JOIN chats ON messages.chat_jid = chats.jid")
	if len(whereClauses) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(whereClauses, " AND "))
	}
	b.WriteString(" ORDER BY messages.timestamp DESC")
	b.WriteString(" LIMIT ? OFFSET ?")

	offset := page * limit
	args = append(args, limit, offset)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	var result []Message
	for rows.Next() {
		m, err := s.scanListMessageRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	// Resolve sender phones now that rows are closed (avoids connection
	// deadlock on single-connection stores).
	for i := range result {
		result[i].SenderPhone = s.resolveSenderPhone(result[i].Sender)
	}

	if !params.IncludeContext || len(result) == 0 {
		return result, nil
	}

	before := params.ContextBefore
	if before <= 0 {
		before = 1
	}
	after := params.ContextAfter
	if after <= 0 {
		after = 1
	}

	// Expand context: before messages (DESC), the match, after messages (ASC).
	var expanded []Message
	for _, m := range result {
		c, err := s.GetMessageContext(ctx, m.ID, before, after)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, c.Before...)
		expanded = append(expanded, c.Message)
		expanded = append(expanded, c.After...)
	}
	return expanded, nil
}

// ListChats returns chats matching query. sortBy is "last_active" (default) or "name".
func (s *Store) ListChats(ctx context.Context, query string, limit, page int, includeLastMessage, includeArchived bool, sortBy string) ([]Chat, error) {
	if limit <= 0 {
		limit = 20
	}
	if page < 0 {
		page = 0
	}

	var b strings.Builder
	b.WriteString(chatSelectSQL("chats", includeLastMessage))

	var args []any
	clauses := []string{}
	if query != "" {
		clauses = append(clauses, "(LOWER(chats.name) LIKE LOWER(?) ESCAPE '\\' OR chats.jid LIKE ? ESCAPE '\\')")
		args = append(args, "%"+escapeLike(query)+"%", "%"+escapeLike(query)+"%")
	}
	if !includeArchived {
		clauses = append(clauses, "chats.archived = 0")
	}
	if len(clauses) > 0 {
		b.WriteString(" WHERE " + strings.Join(clauses, " AND "))
	}

	if sortBy == "name" {
		b.WriteString(" ORDER BY chats.name")
	} else {
		b.WriteString(" ORDER BY chats.last_message_time DESC")
	}

	b.WriteString(" LIMIT ? OFFSET ?")
	args = append(args, limit, page*limit)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer rows.Close()

	var result []Chat
	for rows.Next() {
		c, err := scanChatRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for i := range result {
		result[i].PhoneNumber = s.ResolveJIDToPhone(result[i].JID)
	}
	return result, nil
}

// GetChat returns a single chat by JID. Returns (nil, nil) when not found.
func (s *Store) GetChat(ctx context.Context, jid string, includeLastMessage bool) (*Chat, error) {
	var b strings.Builder
	b.WriteString(chatSelectSQL("c", includeLastMessage))
	b.WriteString(" WHERE c.jid = ?")

	row := s.db.QueryRowContext(ctx, b.String(), jid)
	c, err := scanChatRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get chat: %w", err)
	}
	c.PhoneNumber = s.ResolveJIDToPhone(c.JID)
	return &c, nil
}

func (s *Store) applyChatSettings(ctx context.Context, c *Chat) {
	if s.whatsmeowDB == nil {
		return
	}
	var (
		mutedUntil sql.NullTime
		pinned     int
	)
	err := s.whatsmeowDB.QueryRowContext(ctx,
		`SELECT cs.pinned, cs.muted_until FROM whatsmeow_chat_settings cs
		 JOIN whatsmeow_device d ON d.jid = cs.our_jid
		 WHERE cs.chat_jid = ?`, c.JID).Scan(&pinned, &mutedUntil)
	if err != nil {
		return
	}
	c.Pinned = pinned != 0
	c.Muted = mutedUntil.Valid && mutedUntil.Time.After(time.Now())
	if mutedUntil.Valid {
		c.MutedUntil = mutedUntil.Time
	}
}

func base64Encode(s string) string {
	return base64.URLEncoding.EncodeToString([]byte(s))
}

func base64Decode(s string) (string, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SearchContactsResult holds a page of contact search results together with
// pagination metadata.
type SearchContactsResult struct {
	Contacts   []Contact `json:"contacts"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
	Count      int       `json:"count"`
}

// SearchContacts searches the synced whatsmeow address book and the
// bridge-local address book, merges them by person (LID resolution), applies
// name precedence, filters by an optional query, and returns a page.
//
// Channels, groups, status broadcasts and unresolved LID rows are excluded
// from person results only when they cannot be resolved. Unresolved LIDs are
// kept as separate rows with an empty phone and status "unmapped".
func (s *Store) SearchContacts(ctx context.Context, query string, limit int, cursor string) (SearchContactsResult, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	q := strings.ToLower(strings.TrimSpace(query))

	// 1. Collect synced contacts from whatsmeow_contacts.
	type syncedRow struct {
		jid           string
		firstName     string
		fullName      string
		pushName      string
		businessName  string
		redactedPhone string
	}
	var synced []syncedRow
	if s.whatsmeowDB != nil {
		rows, err := s.whatsmeowDB.QueryContext(ctx, `
			SELECT their_jid, first_name, full_name, push_name, business_name, redacted_phone
			FROM whatsmeow_contacts`)
		if err != nil {
			// A failed read is not an empty address book: return it so the
			// caller can say "lookup failed" instead of "no such contact".
			return SearchContactsResult{}, fmt.Errorf("read synced contacts: %w", err)
		}
		{
			for rows.Next() {
				var r syncedRow
				var fn, ln, pn, bn, rp sql.NullString
				if err := rows.Scan(&r.jid, &fn, &ln, &pn, &bn, &rp); err != nil {
					continue
				}
				r.firstName = fn.String
				r.fullName = ln.String
				r.pushName = pn.String
				r.businessName = bn.String
				r.redactedPhone = rp.String
				if !isPersonJID(r.jid) {
					continue
				}
				synced = append(synced, r)
			}
			rows.Close()
		}
	}

	// 2. Build Contact rows from synced data.
	var syncedContacts []Contact
	for _, r := range synced {
		name, src := bestName(r.firstName, r.fullName, r.pushName, r.businessName, r.redactedPhone)
		if name == "" {
			name = jidUser(r.jid)
			src = "phone"
		}
		jid := r.jid
		phone := jidUser(jid)
		if strings.HasSuffix(jid, "@lid") {
			resolved := s.ResolveLIDToJID(jid)
			if resolved != jid {
				phone = strings.TrimSuffix(resolved, "@s.whatsapp.net")
			} else {
				phone = ""
			}
		}
		lid := ""
		if strings.HasSuffix(jid, "@lid") {
			lid = jid
		}
		status := "mapped"
		if strings.HasSuffix(jid, "@lid") && s.ResolveLIDToJID(jid) == jid {
			status = "unmapped"
		}
		saved := (strings.TrimSpace(r.fullName) != "" || strings.TrimSpace(r.firstName) != "")
		c := Contact{
			PhoneNumber: phone,
			Name:        name,
			JID:         jid,
			LID:         lid,
			NameSource:  src,
			Saved:       saved,
			Resolved:    phone != "" || !strings.HasSuffix(jid, "@lid"),
			Status:      status,
		}
		syncedContacts = append(syncedContacts, c)
	}

	// 2b. Collect chat-derived contacts as a secondary source. Chat names are
	// often push names or bare phone numbers, so they get lower precedence.
	type chatRow struct {
		jid  string
		name string
	}
	var chatData []chatRow
	chatRows, _ := s.db.QueryContext(ctx, `
		SELECT jid, name FROM chats
		WHERE jid NOT LIKE '%@g.us'
		  AND jid NOT LIKE '%@newsletter'`)
	if chatRows != nil {
		for chatRows.Next() {
			var jid string
			var name sql.NullString
			if err := chatRows.Scan(&jid, &name); err != nil {
				continue
			}
			chatData = append(chatData, chatRow{jid: jid, name: name.String})
		}
		chatRows.Close()
	}

	for _, cr := range chatData {
		jid := cr.jid
		if !isPersonJID(jid) {
			continue
		}
		n := strings.TrimSpace(cr.name)
		src := "push"
		if n == "" || isAllDigits(n) {
			n = jidUser(jid)
			src = "phone"
		}
		phone := jidUser(jid)
		if strings.HasSuffix(jid, "@lid") {
			resolved := s.ResolveLIDToJID(jid)
			if resolved != jid {
				phone = strings.TrimSuffix(resolved, "@s.whatsapp.net")
			} else {
				phone = ""
			}
		}
		lid := ""
		if strings.HasSuffix(jid, "@lid") {
			lid = jid
		}
		status := "mapped"
		if strings.HasSuffix(jid, "@lid") && s.ResolveLIDToJID(jid) == jid {
			status = "unmapped"
		}
		c := Contact{
			PhoneNumber: phone,
			Name:        n,
			JID:         jid,
			LID:         lid,
			NameSource:  src,
			Saved:       false,
			Resolved:    phone != "" || !strings.HasSuffix(jid, "@lid"),
			Status:      status,
		}
		syncedContacts = append(syncedContacts, c)
	}

	// 3. Collect local bridge contacts as overrides.
	localContacts, _ := s.ListContacts()
	for i := range localContacts {
		localContacts[i].NameSource = "local"
		localContacts[i].Saved = true
		localContacts[i].Resolved = true
		localContacts[i].Status = "mapped"
	}

	// 4. Merge by person.
	merged := s.mergeContacts(localContacts, syncedContacts)

	// 5. Filter by query.
	if q != "" {
		filtered := make([]Contact, 0, len(merged))
		for _, c := range merged {
			if strings.Contains(strings.ToLower(c.Name), q) ||
				strings.Contains(strings.ToLower(c.PhoneNumber), q) ||
				strings.Contains(strings.ToLower(c.JID), q) ||
				strings.Contains(strings.ToLower(c.LID), q) {
				filtered = append(filtered, c)
			}
		}
		merged = filtered
	}

	// 6. Decode cursor (format: "rank|name|jid").
	cursorRank, cursorName, cursorJID := -1, "", ""
	if cursor != "" {
		raw, err := base64Decode(cursor)
		if err == nil {
			parts := strings.SplitN(raw, "|", 3)
			if len(parts) == 3 {
				// ignore parse error; -1 means "before everything"
				fmt.Sscanf(parts[0], "%d", &cursorRank)
				cursorName = parts[1]
				cursorJID = parts[2]
			}
		}
	}

	// 7. Apply cursor (skip rows at or before the cursor position).
	start := 0
	if cursor != "" {
		for i, c := range merged {
			ri := nameSourceRank(c.NameSource)
			// Sort order is rank DESC, name ASC, jid ASC. An item comes after
			// the cursor when its rank is lower, or same rank with a greater
			// name, or same rank and name with a greater jid.
			if ri < cursorRank ||
				(ri == cursorRank && c.Name > cursorName) ||
				(ri == cursorRank && c.Name == cursorName && c.JID > cursorJID) {
				start = i
				break
			}
			start = i + 1
		}
		if start > len(merged) {
			start = len(merged)
		}
	}

	// 8. Paginate.
	hasMore := start+limit < len(merged)
	end := start + limit
	if end > len(merged) {
		end = len(merged)
	}
	page := merged[start:end]
	if page == nil {
		page = []Contact{}
	}

	var nextCursor string
	if hasMore && len(page) > 0 {
		last := page[len(page)-1]
		nextCursor = base64Encode(fmt.Sprintf("%d|%s|%s", nameSourceRank(last.NameSource), last.Name, last.JID))
	}

	return SearchContactsResult{
		Contacts:   page,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Count:      len(merged),
	}, nil
}

// GetMessageContext returns the target message together with before/after
// windows from the same chat.
func (s *Store) GetMessageContext(ctx context.Context, messageID string, before, after int) (*MessageContext, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = ?`, messageID)

	var (
		ts        time.Time
		sender    string
		chatName  sql.NullString
		content   sql.NullString
		isFromMe  bool
		chatJID   string
		id        string
		msgChat   string
		mediaType sql.NullString
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &msgChat, &mediaType); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("message with ID %s not found", messageID)
		}
		return nil, fmt.Errorf("get message context: %w", err)
	}

	target := Message{
		ID:        id,
		ChatJID:   chatJID,
		Sender:    sender,
		Content:   content.String,
		Timestamp: ts,
		IsFromMe:  isFromMe,
		MediaType: mediaType.String,
		ChatName:  chatName.String,
	}
	target.IsGroup = strings.HasSuffix(target.ChatJID, "@g.us")
	target.SenderPhone = s.resolveSenderPhone(target.Sender)

	beforeMsgs, err := s.queryContextWindow(ctx, msgChat, ts, before, false)
	if err != nil {
		return nil, err
	}
	afterMsgs, err := s.queryContextWindow(ctx, msgChat, ts, after, true)
	if err != nil {
		return nil, err
	}

	return &MessageContext{
		Message: target,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

// GetSenderName resolves a sender JID to a display name, preferring the
// synced address book (whatsmeow_contacts) over the chats table — 1:1 chat
// names are often just the bare phone number. Falls back to the input value
// if no name is found.
func (s *Store) GetSenderName(ctx context.Context, senderJID string) string {
	if name := s.ContactName(senderJID); name != "" {
		return name
	}
	if name := s.chatNameByJID(ctx, senderJID); name != "" {
		return name
	}

	phonePart := jidUser(senderJID)

	// Prefer an exact direct-chat match on phone + @s.whatsapp.net before
	// falling back to a LIKE — the LIKE can cross-match unrelated rows.
	if name := s.chatNameByJID(ctx, phonePart+"@s.whatsapp.net"); name != "" {
		return name
	}

	var name2 sql.NullString
	err := s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid LIKE ? ESCAPE '\\' LIMIT 1", "%"+escapeLike(phonePart)+"%").Scan(&name2)
	if err == nil && name2.Valid && name2.String != "" {
		return name2.String
	}
	return senderJID
}

// chatNameByJID looks up chats.name for an exact jid match. Returns "" if
// not found or the stored name is empty.
func (s *Store) chatNameByJID(ctx context.Context, jid string) string {
	var name sql.NullString
	if err := s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid = ? LIMIT 1", jid).Scan(&name); err == nil {
		return name.String
	}
	return ""
}

// queryContextWindow fetches `limit` messages in chat either strictly before
// (ascending=false) or strictly after (ascending=true) the anchor timestamp.
func (s *Store) queryContextWindow(ctx context.Context, chatJID string, anchor time.Time, limit int, ascending bool) ([]Message, error) {
	op := "<"
	order := "DESC"
	if ascending {
		op = ">"
		order = "ASC"
	}
	q := fmt.Sprintf(`
		SELECT `+listMessageColumns+`
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = ? AND messages.timestamp %s ?
		ORDER BY messages.timestamp %s
		LIMIT ?`, op, order)

	rows, err := s.db.QueryContext(ctx, q, chatJID, anchor, limit)
	if err != nil {
		return nil, fmt.Errorf("context window: %w", err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := s.scanListMessageRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for i := range out {
		out[i].SenderPhone = s.resolveSenderPhone(out[i].Sender)
	}
	return out, nil
}

// scanListMessageRow scans the 8-column projection used by list_messages and
// the context window queries.
func (s *Store) scanListMessageRow(row interface {
	Scan(dest ...any) error
}) (Message, error) {
	var (
		ts        time.Time
		sender    string
		chatName  sql.NullString
		content   sql.NullString
		isFromMe  bool
		chatJID   string
		id        string
		mediaType sql.NullString
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &mediaType); err != nil {
		return Message{}, err
	}
	m := Message{
		ID:        id,
		ChatJID:   chatJID,
		Sender:    sender,
		Content:   content.String,
		Timestamp: ts,
		IsFromMe:  isFromMe,
		MediaType: mediaType.String,
		ChatName:  chatName.String,
	}
	m.IsGroup = strings.HasSuffix(m.ChatJID, "@g.us")
	// SenderPhone is resolved after rows are closed to avoid a connection
	// deadlock on single-connection stores (resolveSenderPhone queries s.db).
	m.SenderPhone = m.Sender
	return m, nil
}

// ListMessagesCursor returns a page of messages using keyset pagination.
// When cursor is set, only messages strictly older than the cursor's
// (timestamp, id) are returned. Fetches limit+1 rows to detect has_more.
// Ordering is always timestamp DESC, id DESC.
func (s *Store) ListMessagesCursor(ctx context.Context, params ListMessagesParams) (*ListMessagesResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var (
		whereClauses []string
		args         []any
	)

	if !params.After.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp > ?")
		args = append(args, params.After)
	}
	if !params.Before.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp < ?")
		args = append(args, params.Before)
	}
	if params.SenderPhone != "" {
		whereClauses = append(whereClauses, "messages.sender = ?")
		args = append(args, params.SenderPhone)
	}
	if params.ChatJID != "" {
		whereClauses = append(whereClauses, "messages.chat_jid = ?")
		args = append(args, params.ChatJID)
	}
	if params.Query != "" {
		whereClauses = append(whereClauses, "LOWER(messages.content) LIKE LOWER(?)")
		args = append(args, "%"+escapeLike(params.Query)+"%")
	}
	if params.Cursor != "" {
		cursorTs, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		whereClauses = append(whereClauses,
			"(messages.timestamp < ? OR (messages.timestamp = ? AND messages.id < ?))")
		args = append(args, cursorTs, cursorTs, cursorID)
	}

	var b strings.Builder
	b.WriteString("SELECT " + listMessageColumns + " FROM messages")
	b.WriteString(" JOIN chats ON messages.chat_jid = chats.jid")
	if len(whereClauses) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(whereClauses, " AND "))
	}
	b.WriteString(" ORDER BY messages.timestamp DESC, messages.id DESC")
	b.WriteString(" LIMIT ?")
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list messages cursor: %w", err)
	}
	defer rows.Close()

	var result []Message
	for rows.Next() {
		m, err := s.scanListMessageRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for i := range result {
		result[i].SenderPhone = s.resolveSenderPhone(result[i].Sender)
	}

	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}

	// Only emit a next_cursor when there are more rows to fetch. On the last
	// page the cursor would point at the final message, which is useless.
	var nextCursor string
	if hasMore && len(result) > 0 {
		last := result[len(result)-1]
		nextCursor = encodeCursor(last.Timestamp, last.ID)
	}

	return &ListMessagesResult{
		Messages:   result,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Count:      len(result),
	}, nil
}

// ListMediaMessages returns messages that have media attached (media_type != ""),
// using the same keyset-pagination contract as ListMessagesCursor.
func (s *Store) ListMediaMessages(ctx context.Context, params ListMessagesParams) (*ListMessagesResult, error) {
	params.Query = "" // media listing does not do substring matching
	return s.listMessagesWithMediaFilter(ctx, params)
}

func (s *Store) listMessagesWithMediaFilter(ctx context.Context, params ListMessagesParams) (*ListMessagesResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var (
		whereClauses []string
		args         []any
	)

	// Only messages that actually have media.
	whereClauses = append(whereClauses, "messages.media_type != '' AND messages.media_type IS NOT NULL")

	if !params.After.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp > ?")
		args = append(args, params.After)
	}
	if !params.Before.IsZero() {
		whereClauses = append(whereClauses, "messages.timestamp < ?")
		args = append(args, params.Before)
	}
	if params.SenderPhone != "" {
		whereClauses = append(whereClauses, "messages.sender = ?")
		args = append(args, params.SenderPhone)
	}
	if params.ChatJID != "" {
		whereClauses = append(whereClauses, "messages.chat_jid = ?")
		args = append(args, params.ChatJID)
	}
	if params.Cursor != "" {
		cursorTs, cursorID, err := decodeCursor(params.Cursor)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		whereClauses = append(whereClauses,
			"(messages.timestamp < ? OR (messages.timestamp = ? AND messages.id < ?))")
		args = append(args, cursorTs, cursorTs, cursorID)
	}

	var b strings.Builder
	b.WriteString("SELECT " + listMessageColumns + " FROM messages")
	b.WriteString(" JOIN chats ON messages.chat_jid = chats.jid")
	b.WriteString(" WHERE ")
	b.WriteString(strings.Join(whereClauses, " AND "))
	b.WriteString(" ORDER BY messages.timestamp DESC, messages.id DESC")
	b.WriteString(" LIMIT ?")
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list media messages: %w", err)
	}
	defer rows.Close()

	var result []Message
	for rows.Next() {
		m, err := s.scanListMessageRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for i := range result {
		result[i].SenderPhone = s.resolveSenderPhone(result[i].Sender)
	}

	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}

	// Only emit a next_cursor when there are more rows to fetch.
	var nextCursor string
	if hasMore && len(result) > 0 {
		last := result[len(result)-1]
		nextCursor = encodeCursor(last.Timestamp, last.ID)
	}

	return &ListMessagesResult{
		Messages:   result,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Count:      len(result),
	}, nil
}

// resolveSenderPhone returns a full phone (bare user part) for a message
// sender. The stored sender may be a LID user part (group participants, and
// DMs whose LID mapping was unknown when the message arrived), so try the
// LID map; a phone-number sender simply misses and is returned unchanged.
func (s *Store) resolveSenderPhone(sender string) string {
	if sender == "" || sender == "me" {
		return sender
	}
	resolved := s.ResolveLIDToJID(sender + "@lid")
	if u := jidUser(resolved); u != "" {
		return u
	}
	return sender
}

// chatSelectSQL returns the shared 6-column chat projection (jid, name,
// last_message_time, plus the latest message's content/sender/is_from_me
// when includeLastMessage) used by both ListChats and GetChat. alias
// qualifies the chat column references; pass "chats" for an unaliased FROM
// clause (ListChats) or a short alias like "c" when the caller appends its
// own WHERE clause on that alias (GetChat).
func chatSelectSQL(alias string, includeLastMessage bool) string {
	from := "chats"
	if alias != "chats" {
		from = "chats " + alias
	}
	flags := alias + ".archived, " + alias + ".pinned, " + alias + ".muted_until"
	if !includeLastMessage {
		return `SELECT ` + alias + `.jid, ` + alias + `.name, ` + alias + `.last_message_time,
			NULL, NULL, NULL, ` + flags + `
		FROM ` + from
	}
	return `SELECT ` + alias + `.jid, ` + alias + `.name, ` + alias + `.last_message_time,
		lm.content, lm.sender, lm.is_from_me, ` + flags + `
	FROM ` + from + `
		LEFT JOIN (
			SELECT m.chat_jid, m.content, m.sender, m.is_from_me
			FROM messages m
			INNER JOIN (
				SELECT chat_jid, MAX(timestamp) AS max_ts, MAX(id) AS max_id
				FROM messages
				GROUP BY chat_jid
			) m2 ON m.chat_jid = m2.chat_jid AND m.timestamp = m2.max_ts AND m.id = m2.max_id
		) lm ON ` + alias + `.jid = lm.chat_jid`
}
func scanChatRow(row interface {
	Scan(dest ...any) error
}) (Chat, error) {
	var (
		jid          string
		name         sql.NullString
		lastTime     sql.NullTime
		lastMessage  sql.NullString
		lastSender   sql.NullString
		lastIsFromMe sql.NullBool
		archived     sql.NullBool
		pinned       sql.NullBool
		mutedUntil   sql.NullTime
	)
	if err := row.Scan(&jid, &name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe, &archived, &pinned, &mutedUntil); err != nil {
		return Chat{}, err
	}
	c := Chat{
		JID:          jid,
		Name:         name.String,
		LastMessage:  lastMessage.String,
		LastSender:   lastSender.String,
		LastIsFromMe: lastIsFromMe.Bool,
		Archived:     archived.Bool,
		Pinned:       pinned.Bool,
	}
	if lastTime.Valid {
		c.LastMessageTime = lastTime.Time
	}
	if mutedUntil.Valid {
		c.MutedUntil = mutedUntil.Time
		c.Muted = c.MutedUntil.After(time.Now())
	}
	c.IsGroup = strings.HasSuffix(c.JID, "@g.us")
	return c, nil
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
