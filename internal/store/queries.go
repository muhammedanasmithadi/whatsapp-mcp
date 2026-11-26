package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

const listMessageColumns = "messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type, messages.is_starred"

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

type ListMessagesResult struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
	Count      int       `json:"count"`
}

type cursorPayload struct {
	T string `json:"t"` // "2006-01-02 15:04:05" UTC timestamp
	I string `json:"i"` // message id
}

func encodeCursor(ts time.Time, id string) string {
	p := cursorPayload{T: ts.UTC().Format("2006-01-02 15:04:05"), I: id}
	return base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%s", p.T, p.I)))
}

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

func (s *Store) ListChats(ctx context.Context, query string, limit, page int, includeLastMessage, includeArchived bool, sortBy string) ([]Chat, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
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

type SearchContactsResult struct {
	Contacts   []Contact `json:"contacts"`
	NextCursor string    `json:"next_cursor,omitempty"`
	HasMore    bool      `json:"has_more"`
	Count      int       `json:"count"`
}

func (s *Store) SearchContacts(ctx context.Context, query string, limit int, cursor string) (SearchContactsResult, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	q := strings.ToLower(strings.TrimSpace(query))

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

	localContacts, _ := s.ListContacts()
	for i := range localContacts {
		localContacts[i].NameSource = "local"
		localContacts[i].Saved = true
		localContacts[i].Resolved = true
		localContacts[i].Status = "mapped"
	}

	merged := s.mergeContacts(localContacts, syncedContacts)

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

	cursorRank, cursorName, cursorJID := -1, "", ""
	if cursor != "" {
		raw, err := base64Decode(cursor)
		if err == nil {
			parts := strings.SplitN(raw, "|", 3)
			if len(parts) == 3 {
				fmt.Sscanf(parts[0], "%d", &cursorRank)
				cursorName = parts[1]
				cursorJID = parts[2]
			}
		}
	}

	start := 0
	if cursor != "" {
		for i, c := range merged {
			ri := nameSourceRank(c.NameSource)
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

func (s *Store) GetMessageContext(ctx context.Context, messageID string, before, after int) (*MessageContext, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.chat_jid, messages.media_type, messages.is_starred
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
		isStarred sql.NullBool
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &msgChat, &mediaType, &isStarred); err != nil {
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
		IsStarred: isStarred.Bool,
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

func (s *Store) GetSenderName(ctx context.Context, senderJID string) string {
	if name := s.ContactName(senderJID); name != "" {
		return name
	}
	if name := s.chatNameByJID(ctx, senderJID); name != "" {
		return name
	}

	phonePart := jidUser(senderJID)

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

func (s *Store) chatNameByJID(ctx context.Context, jid string) string {
	var name sql.NullString
	if err := s.db.QueryRowContext(ctx,
		"SELECT name FROM chats WHERE jid = ? LIMIT 1", jid).Scan(&name); err == nil {
		return name.String
	}
	return ""
}

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
		isStarred sql.NullBool
	)
	if err := row.Scan(&ts, &sender, &chatName, &content, &isFromMe, &chatJID, &id, &mediaType, &isStarred); err != nil {
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
		IsStarred: isStarred.Bool,
	}
	m.IsGroup = strings.HasSuffix(m.ChatJID, "@g.us")
	m.SenderPhone = m.Sender
	return m, nil
}

func (s *Store) ListMessagesCursor(ctx context.Context, params ListMessagesParams) (*ListMessagesResult, error) {
	return s.listMessagesPage(ctx, params, false)
}

func (s *Store) ListMediaMessages(ctx context.Context, params ListMessagesParams) (*ListMessagesResult, error) {
	return s.listMessagesPage(ctx, params, true)
}

func (s *Store) listMessagesPage(ctx context.Context, params ListMessagesParams, mediaOnly bool) (*ListMessagesResult, error) {
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

	if mediaOnly {
		whereClauses = append(whereClauses, "messages.media_type != '' AND messages.media_type IS NOT NULL")
	}
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
	if !mediaOnly && params.Query != "" {
		whereClauses = append(whereClauses, "LOWER(messages.content) LIKE LOWER(?) ESCAPE '\\'")
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

	for i := range result {
		result[i].SenderPhone = s.resolveSenderPhone(result[i].Sender)
	}

	hasMore := len(result) > limit
	if hasMore {
		result = result[:limit]
	}

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
