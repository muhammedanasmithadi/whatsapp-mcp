package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// StartEventHandler registers the event handler on the underlying whatsmeow
// client. Must be called before Connect().
func (c *Client) StartEventHandler() {
	if c.handlerInstalled {
		return
	}
	c.handlerID = c.wa.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			c.handleMessage(v)
		case *events.HistorySync:
			c.handleHistorySync(v)
		case *events.Connected:
			c.log.Infof("Connected to WhatsApp")
		case *events.LoggedOut:
			c.log.Warnf("Device logged out, please scan QR code to log in again")
		case *events.OfflineSyncPreview:
			c.log.Infof("Offline sync starting - %d messages, %d receipts pending", v.Messages, v.Receipts)
		case *events.OfflineSyncCompleted:
			c.log.Infof("Offline sync completed - %d messages synced", v.Count)
		case *events.Receipt:
			c.log.Debugf("Receipt for %d messages: %s", len(v.MessageIDs), v.Type)
		case *events.Presence:
			c.handlePresence(v)
		case *events.UndecryptableMessage:
			c.log.Warnf("Undecryptable message from %s", v.Info.Sender)
		case *events.LabelEdit:
			c.handleLabelEdit(v)
		case *events.LabelAssociationChat:
			c.handleLabelAssociationChat(v)
		default:
			c.log.Debugf("Unhandled event type: %T", v)
		}
	})
	c.handlerInstalled = true
}

// normalizedMessage holds the result of normalizeIncomingMessage: the
// store-ready Message plus the raw media crypto fields and the full
// post-LID-resolution sender JID (used for poll-vote keying).
type normalizedMessage struct {
	msg           store.Message
	mediaKey      []byte
	fileSHA256    []byte
	fileEncSHA256 []byte
	fileLength    uint64
	senderFullJID string // post-LID sender as "user@server"
	chatJID       string // post-LID-normalised chat JID
}

// normalizeIncomingMessage resolves sender and chat JIDs (LID → phone),
// extracts text/media content, and returns a store-ready normalizedMessage.
// It covers the logic shared between handleMessage and handleHistorySync.
//
// Parameters:
//   - rawChatJID: the raw chat JID string (may be @lid for DMs).
//   - rawSenderJID: the raw sender JID string (may be @lid).
//   - senderUser: the user-part of the sender JID (fallback when no
//     participant is available).
//   - isGroup: true when rawChatJID ends with @g.us.
//   - isFromMe: whether the message was sent by the local device.
//   - ownID: the local device's user-part (used when isFromMe is true and
//     no participant is set).
//   - msgID: the message ID.
//   - timestamp: the message timestamp.
//   - raw: the protobuf Message payload.
func (c *Client) normalizeIncomingMessage(
	rawChatJID, rawSenderJID, senderUser string,
	isGroup, isFromMe bool,
	ownID string,
	msgID string,
	timestamp time.Time,
	raw *waProto.Message,
) normalizedMessage {
	// Normalise chat JID: convert LID to standard JID for direct chats.
	chatJID := rawChatJID
	if !isGroup {
		chatJID = c.store.ResolveLIDToJID(rawChatJID)
		if chatJID != rawChatJID {
			c.log.Infof("Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))
		}
	}

	// Normalise sender (may be in LID form inside groups).
	sender := senderUser
	normalizedSender := c.store.ResolveLIDToJID(rawSenderJID)
	if normalizedSender != rawSenderJID {
		sender = strings.Split(normalizedSender, "@")[0]
		c.log.Infof("Normalized sender: %s -> %s", c.redactor.JID(senderUser), c.redactor.JID(sender))
	}

	// If the message is from us and we have no better sender, use ownID.
	if isFromMe && sender == "" && ownID != "" {
		sender = ownID
	}

	content := extractTextContent(raw)
	mediaType, filename, url, directPath, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(raw)

	return normalizedMessage{
		msg: store.Message{
			ID:         msgID,
			ChatJID:    chatJID,
			Sender:     sender,
			Content:    content,
			Timestamp:  timestamp,
			IsFromMe:   isFromMe,
			MediaType:  mediaType,
			Filename:   filename,
			URL:        url,
			DirectPath: directPath,
		},
		mediaKey:      mediaKey,
		fileSHA256:    fileSHA256,
		fileEncSHA256: fileEncSHA256,
		fileLength:    fileLength,
		senderFullJID: normalizedSender,
		chatJID:       chatJID,
	}
}

// handleMessage persists an incoming message and its chat metadata.
func (c *Client) handleMessage(msg *events.Message) {
	nm := c.normalizeIncomingMessage(
		msg.Info.Chat.String(),
		msg.Info.Sender.String(),
		msg.Info.Sender.User,
		strings.HasSuffix(msg.Info.Chat.String(), "@g.us"),
		msg.Info.IsFromMe,
		"", // ownID not needed for live messages — sender is always set
		msg.Info.ID,
		msg.Info.Timestamp,
		msg.Message,
	)

	name := c.GetChatName(msg.Info.Chat, nm.chatJID, nil, nm.msg.Sender)

	if err := c.store.StoreChat(nm.chatJID, name, msg.Info.Timestamp); err != nil {
		c.log.Warnf("Failed to store chat: %v", err)
	}

	// Poll vote — a signal-only event with no user-visible content. Decrypt,
	// tally into the local cache, and return. We pass the post-LID-resolution
	// full JID so StorePollVote's primary key never collides across servers.
	if msg.Message != nil && msg.Message.GetPollUpdateMessage() != nil {
		voterFullJID := nm.senderFullJID
		if voterFullJID == "" {
			voterFullJID = msg.Info.Sender.String()
		}
		c.handlePollVote(msg, nm.chatJID, voterFullJID)
		return
	}

	// Poll creation — store a "[poll] <question>" row for readability and
	// cache the option names so we can decode vote SHA-256 hashes later.
	var pollOptions []string
	if msg.Message != nil {
		if pc := msg.Message.GetPollCreationMessage(); pc != nil {
			pollOptions = extractPollOptionNames(pc)
			if nm.msg.Content == "" {
				nm.msg.Content = "[poll] " + pc.GetName()
			}
		}
	}

	// WhatsApp wraps edited messages in EditedMessage and sets IsEdit. When
	// we see one, update the stored row instead of inserting a duplicate.
	if msg.IsEdit {
		if err := c.store.UpdateMessage(context.Background(), msg.Info.ID, nm.chatJID, nm.msg.Content); err != nil {
			if err == sql.ErrNoRows {
				// No cached row to update — fall through and insert fresh.
				c.log.Warnf("Edit for unknown message %s in chat %s, inserting as new",
					c.redactor.MsgID(msg.Info.ID), c.redactor.JID(nm.chatJID))
			} else {
				c.log.Warnf("Failed to apply edit to message %s: %v", c.redactor.MsgID(msg.Info.ID), err)
				if c.metricSink != nil {
					c.metricSink.IncErrors()
				}
				return
			}
		} else {
			if c.metricSink != nil {
				c.metricSink.IncMessagesIn()
			}
			c.log.Infof("[%s] %s %s: EDITED %s", msg.Info.Timestamp.Format("2006-01-02 15:04:05"),
				"<-", c.redactor.JID(nm.msg.Sender), c.redactor.JID(nm.chatJID))
			return
		}
	}

	if nm.msg.Content == "" && nm.msg.MediaType == "" {
		// Store messages from status@broadcast even if decryption failed
		// (empty content/media type). WhatsApp delivers Status (Stories)
		// updates to status@broadcast, but some may be encrypted with sender
		// keys we don't have. We still store a placeholder so the user knows
		// a status was posted.
		if nm.msg.ChatJID == store.StatusBroadcastJID {
			nm.msg.Content = "[status update]"
			nm.msg.MediaType = "status"
		} else {
			return
		}
	}

	err := c.store.StoreMessage(context.Background(), nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength)

	if err == nil {
		if c.metricSink != nil {
			c.metricSink.IncMessagesIn()
		}
		if c.onIncomingMsg != nil {
			payload := fmt.Sprintf(`{"chat_jid":"%s","sender":"%s","content":"%s","media_type":"%s","timestamp":"%s"}`,
				c.redactor.JID(nm.msg.ChatJID), c.redactor.JID(nm.msg.Sender), c.redactor.Body(nm.msg.Content),
				nm.msg.MediaType, nm.msg.Timestamp.Format(time.RFC3339))
			c.onIncomingMsg(payload)
		}
		if len(pollOptions) > 0 {
			if perr := c.store.StorePollMetadata(context.Background(), msg.Info.ID, nm.chatJID, pollOptions); perr != nil {
				c.log.Warnf("Failed to store poll metadata for %s: %v", msg.Info.ID, perr)
			}
		}

		// Auto-transcribe incoming voice notes in the background.
		if nm.msg.MediaType == "audio" && c.sttClient != nil {
			msgID := msg.Info.ID
			chatJID := nm.chatJID
			go func() {
				text, err := c.TranscribeVoiceNote(context.Background(), msgID, chatJID)
				if err != nil {
					c.log.Warnf("stt: auto-transcribe voice note %s: %v", c.redactor.MsgID(msgID), err)
					return
				}
				if text == "" {
					return
				}
				// Append the transcript to the stored message content.
				updated := nm.msg.Content
				if updated != "" {
					updated += "\n"
				}
				updated += "[voice note transcript: " + text + "]"
				if uerr := c.store.UpdateMessage(context.Background(), msgID, chatJID, updated); uerr != nil {
					c.log.Warnf("stt: store transcript for %s: %v", c.redactor.MsgID(msgID), uerr)
				} else {
					c.log.Infof("stt: transcribed voice note %s: %s", c.redactor.MsgID(msgID), c.redactor.Body(text))
				}
			}()
		}

			// Auto-responder: reply to incoming 1:1 text messages when enabled.
			// Runs in a goroutine so a slow or rate-limited send never blocks
			// message handling. WhatsApp push delivers the incoming message, so
			// this is event-driven — no polling.
			chatJID := nm.chatJID
			content := nm.msg.Content
			isFromMe := msg.Info.IsFromMe
			go func() {
				can, err := c.store.CanAutoRespond(context.Background(), chatJID)
				if err != nil {
					c.log.Warnf("auto-responder: config check failed: %v", err)
					return
				}
				if !can {
					return
				}
				if isFromMe {
					return
				}
				if strings.HasSuffix(chatJID, "@g.us") || strings.HasSuffix(chatJID, "@newsletter") {
					return
				}
				if chatJID == store.StatusBroadcastJID {
					return
				}
				if strings.TrimSpace(content) == "" {
					return
				}
				// Get the reply message
				_, reply, _, _, _, err := c.store.GetAutoResponderWithConfig(context.Background())
				if err != nil {
					c.log.Warnf("auto-responder: config check failed: %v", err)
					return
				}
				if reply == "" {
					return
				}
				r := c.Send(context.Background(), chatJID, reply)
				if !r.Success {
					c.log.Warnf("auto-responder: failed to reply to %s: %s",
						c.redactor.JID(chatJID), r.Message)
					return
				}
				if err := c.store.RecordAutoRespond(context.Background(), chatJID); err != nil {
					c.log.Warnf("auto-responder: failed to record reply time: %v", err)
				}
				c.log.Infof("auto-responder: replied to %s", c.redactor.JID(chatJID))
			}()
	}

	if err != nil {
		c.log.Warnf("Failed to store message: %v", err)
		if c.metricSink != nil {
			c.metricSink.IncErrors()
		}
		return
	}

	direction := "<-"
	if msg.Info.IsFromMe {
		direction = "->"
	}
	ts := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
	if nm.msg.MediaType != "" {
		c.log.Infof("[%s] %s %s: [%s: %s] %s", ts, direction, c.redactor.JID(nm.msg.Sender), nm.msg.MediaType, nm.msg.Filename, c.redactor.Body(nm.msg.Content))
	} else if nm.msg.Content != "" {
		c.log.Infof("[%s] %s %s: %s", ts, direction, c.redactor.JID(nm.msg.Sender), c.redactor.Body(nm.msg.Content))
	}
}

// handleHistorySync processes a bulk history payload from WhatsApp.
func (c *Client) handleHistorySync(historySync *events.HistorySync) {
	syncType := "UNKNOWN"
	if historySync.Data.SyncType != nil {
		syncType = historySync.Data.SyncType.String()
	}
	c.log.Infof("Received history sync event (type: %s) with %d conversations",
		syncType, len(historySync.Data.Conversations))

	ctx := context.Background()
	syncedCount := 0

	ownID := ""
	if c.wa.Store.ID != nil {
		ownID = c.wa.Store.ID.User
	}

	for _, conversation := range historySync.Data.Conversations {
		if conversation.ID == nil {
			continue
		}
		rawChatJID := *conversation.ID

		jid, err := types.ParseJID(rawChatJID)
		if err != nil {
			c.log.Warnf("Failed to parse JID %s: %v", rawChatJID, err)
			continue
		}

		isGroup := strings.HasSuffix(rawChatJID, "@g.us")

		// Resolve the chat JID once for the conversation header. We
		// normalise it here so GetChatName and StoreChat use the
		// phone-number-based JID.
		chatJID := rawChatJID
		if !isGroup {
			chatJID = c.store.ResolveLIDToJID(rawChatJID)
			if chatJID != rawChatJID {
				c.log.Infof("History sync: Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))
			}
		}

		name := c.GetChatName(jid, chatJID, conversation, "")

		messages := conversation.Messages
		if len(messages) == 0 {
			continue
		}

		latestMsg := messages[0]
		if latestMsg == nil || latestMsg.Message == nil {
			continue
		}
		ts := latestMsg.Message.GetMessageTimestamp()
		if ts == 0 {
			continue
		}
		_ = c.store.StoreChat(chatJID, name, time.Unix(int64(ts), 0))

		for _, msg := range messages {
			if msg == nil || msg.Message == nil {
				continue
			}

			msgTS := msg.Message.GetMessageTimestamp()
			if msgTS == 0 {
				continue
			}
			timestamp := time.Unix(int64(msgTS), 0)

			// Determine sender and isFromMe from the message key.
			var rawSenderJID, senderUser string
			isFromMe := false
			if msg.Message.Key != nil {
				if msg.Message.Key.FromMe != nil {
					isFromMe = *msg.Message.Key.FromMe
				}
				if !isFromMe && msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "" {
					rawSenderJID = *msg.Message.Key.Participant
					senderUser = strings.Split(rawSenderJID, "@")[0]
				} else if isFromMe {
					rawSenderJID = ownID + "@s.whatsapp.net"
					senderUser = ownID
				} else {
					rawSenderJID = jid.String()
					senderUser = jid.User
				}
			} else {
				rawSenderJID = jid.String()
				senderUser = jid.User
			}

			msgID := ""
			if msg.Message.Key != nil && msg.Message.Key.ID != nil {
				msgID = *msg.Message.Key.ID
			}

			nm := c.normalizeIncomingMessage(
				rawChatJID, rawSenderJID, senderUser,
				isGroup, isFromMe, ownID,
				msgID, timestamp, msg.Message.Message,
			)

			c.log.Infof("Message content: %s, Media Type: %s", c.redactor.Body(nm.msg.Content), nm.msg.MediaType)
			if nm.msg.Content == "" && nm.msg.MediaType == "" {
				// Store messages from status@broadcast even if decryption failed
				// (empty content/media type). WhatsApp delivers Status (Stories)
				// updates to status@broadcast, but some may be encrypted with sender
				// keys we don't have. We still store a placeholder so the user knows
				// a status was posted.
				if nm.msg.ChatJID == store.StatusBroadcastJID {
					nm.msg.Content = "[status update]"
					nm.msg.MediaType = "status"
				} else {
					continue
				}
			}

			// Override chatJID to the already-resolved one from the
			// conversation header — avoids re-resolving per message.
			nm.msg.ChatJID = chatJID

			if err := c.store.StoreMessage(ctx, nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength); err != nil {
				c.log.Warnf("Failed to store history message: %v", err)
				if c.metricSink != nil {
					c.metricSink.IncErrors()
				}
				continue
			}
			if c.metricSink != nil {
				c.metricSink.IncMessagesIn()
			}
			syncedCount++
			if nm.msg.MediaType != "" {
				c.log.Infof("Stored message: [%s] %s -> %s: [%s: %s] %s",
					timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender), c.redactor.JID(chatJID), nm.msg.MediaType, nm.msg.Filename, c.redactor.Body(nm.msg.Content))
			} else {
				c.log.Infof("Stored message: [%s] %s -> %s: %s",
					timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender), c.redactor.JID(chatJID), c.redactor.Body(nm.msg.Content))
			}
		}
	}

	// Status (Stories) updates travel in their own HistorySync field, not as
	// conversations. The INITIAL_STATUS_V3 sync uses this field exclusively,
	// so ignoring it drops every status the phone includes in that chunk.
	statusCount := c.importStatusV3Messages(ctx, ownID, historySync.Data.GetStatusV3Messages())

	c.log.Infof("History sync complete. Stored %d messages and %d status updates.", syncedCount, statusCount)
}

// importStatusV3Messages persists the Status (Stories) updates carried in a
// history-sync payload. WhatsApp separates these from conversations
// (HistorySync.statusV3Messages, field 3), and the INITIAL_STATUS_V3 sync
// populates only that field. A handler that walks conversations alone drops
// the whole chunk. Returns the number of status rows stored.
func (c *Client) importStatusV3Messages(ctx context.Context, ownID string, messages []*waWeb.WebMessageInfo) int {
	if len(messages) == 0 {
		return 0
	}
	// Messages carry a foreign key to chats. The status conversation header is
	// usually created by live handling, but a fresh link can receive
	// INITIAL_STATUS_V3 before any status message arrives, so guarantee the row
	// exists without overwriting a name learned from live traffic.
	if err := c.store.EnsureChat(store.StatusBroadcastJID, "Status"); err != nil {
		c.log.Warnf("Failed to ensure status chat row: %v", err)
		if c.metricSink != nil {
			c.metricSink.IncErrors()
		}
		return 0
	}
	stored := 0
	for _, wm := range messages {
		if wm == nil || wm.Message == nil {
			continue
		}
		key := wm.GetKey()
		if key == nil {
			continue
		}
		msgID := key.GetID()
		if msgID == "" {
			continue
		}
		ts := wm.GetMessageTimestamp()
		if ts == 0 {
			continue
		}
		timestamp := time.Unix(int64(ts), 0)
		isFromMe := key.GetFromMe()

		// The poster lives in key.participant for status messages. Some
		// payloads put it in WebMessageInfo.participant instead, and a few
		// use the key's remoteJID. Try each in order.
		rawSender := key.GetParticipant()
		if rawSender == "" {
			rawSender = wm.GetParticipant()
		}
		if rawSender == "" {
			if remote := key.GetRemoteJID(); remote != "" && remote != store.StatusBroadcastJID {
				rawSender = remote
			}
		}
		senderUser := ""
		if rawSender != "" {
			if jid, err := types.ParseJID(rawSender); err == nil {
				senderUser = jid.User
			} else {
				senderUser = strings.Split(rawSender, "@")[0]
			}
		}

		nm := c.normalizeIncomingMessage(
			store.StatusBroadcastJID,
			rawSender,
			senderUser,
			false, // status@broadcast is not a group chat
			isFromMe,
			ownID,
			msgID,
			timestamp,
			wm.Message,
		)
		// Status rows always live under the fixed broadcast JID, whatever the
		// payload put in the key's remoteJID.
		nm.msg.ChatJID = store.StatusBroadcastJID
		if nm.msg.Sender == "" {
			// Senderless rows cannot be grouped or receipted; drop them rather
			// than invent a sender.
			c.log.Warnf("Skipping status history message %s with no sender", c.redactor.MsgID(msgID))
			continue
		}
		if nm.msg.Content == "" && nm.msg.MediaType == "" {
			// The payload can hold a status whose media was pruned. Keep a
			// placeholder so the post is still visible.
			nm.msg.Content = "[status update]"
			nm.msg.MediaType = "status"
		}
		if err := c.store.StoreMessage(ctx, nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength); err != nil {
			c.log.Warnf("Failed to store status history message %s: %v", c.redactor.MsgID(msgID), err)
			if c.metricSink != nil {
				c.metricSink.IncErrors()
			}
			continue
		}
		if c.metricSink != nil {
			c.metricSink.IncMessagesIn()
		}
		stored++
		c.log.Infof("[%s] status <- %s: [%s] %s",
			timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender),
			nm.msg.MediaType, c.redactor.Body(nm.msg.Content))
	}
	return stored
}

// GetChatName determines the appropriate name for a chat. It checks the
// existing database entry first, then falls back to conversation metadata
// (for history sync), contact store lookups, and finally the JID.
// The conversation parameter is the typed whatsmeow Conversation from a
// history-sync payload; pass nil for live messages.
func (c *Client) GetChatName(jid types.JID, chatJID string, conversation *waHistorySync.Conversation, sender string) string {
	if existing := c.store.FindChatName(chatJID); existing != "" {
		c.log.Infof("Using existing chat name for %s: %s", c.redactor.JID(chatJID), existing)
		return existing
	}

	var name string

	if jid.Server == "g.us" {
		c.log.Infof("Getting name for group: %s", c.redactor.JID(chatJID))

		if conversation != nil {
			if dn := conversation.GetDisplayName(); dn != "" {
				name = dn
			} else if cn := conversation.GetName(); cn != "" {
				name = cn
			}
		}

		if name == "" {
			name = c.resolveChatNameFallback(context.Background(), jid, sender)
		}

		c.log.Infof("Using group name: %s", name)
	} else {
		c.log.Infof("Getting name for contact: %s", c.redactor.JID(chatJID))

		name = c.resolveChatNameFallback(context.Background(), jid, sender)

		c.log.Infof("Using contact name: %s", c.redactor.Body(name))
	}

	return name
}

// resolveChatNameFallback looks up a chat's name via GetGroupInfo (for
// groups) or the contact store (for DMs), falling back to sender (if given)
// and finally jid.User. This is the shared tail of GetChatName's group and
// contact branches, also used directly by persistSent (which has no
// conversation metadata to check first).
func (c *Client) resolveChatNameFallback(ctx context.Context, jid types.JID, sender string) string {
	if jid.Server == "g.us" {
		if gi, err := c.wa.GetGroupInfo(ctx, jid); err == nil && gi.Name != "" {
			return gi.Name
		}
		return "Group " + jid.User
	}
	if contact, err := c.wa.Store.Contacts.GetContact(ctx, jid); err == nil && contact.FullName != "" {
		return contact.FullName
	}
	if sender != "" {
		return sender
	}
	return jid.User
}

// extractTextContent returns the plain-text body of a Message, if any.
func extractTextContent(msg *waProto.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		if t := ext.GetText(); t != "" {
			return t
		}
	}
	// Media messages may carry a caption; keep it so it is stored and searchable.
	if img := msg.GetImageMessage(); img != nil {
		if c := img.GetCaption(); c != "" {
			return c
		}
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		if c := vid.GetCaption(); c != "" {
			return c
		}
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		if c := doc.GetCaption(); c != "" {
			return c
		}
	}
	// Incoming contact cards: store them as text so they are searchable.
	if cm := msg.GetContactMessage(); cm != nil {
		name := ""
		if cm.DisplayName != nil {
			name = *cm.DisplayName
		}
		if name == "" {
			name = "contact"
		}
		if cm.Vcard != nil && *cm.Vcard != "" {
			return "[contact card] " + name + "\n" + *cm.Vcard
		}
		return "[contact card] " + name
	}
	if ca := msg.GetContactsArrayMessage(); ca != nil {
		names := make([]string, 0, len(ca.Contacts))
		for _, c := range ca.Contacts {
			if c != nil && c.DisplayName != nil && *c.DisplayName != "" {
				names = append(names, *c.DisplayName)
			}
		}
		label := "contacts"
		if ca.DisplayName != nil && *ca.DisplayName != "" {
			label = *ca.DisplayName
		}
		if len(names) > 0 {
			return "[contact cards] " + label + ": " + strings.Join(names, ", ")
		}
		return "[contact cards] " + label
	}
	if gi := msg.GetGroupInviteMessage(); gi != nil {
		return "[group invite]"
	}
	if sm := msg.GetStickerMessage(); sm != nil {
		return "[sticker]"
	}
	if loc := msg.GetLocationMessage(); loc != nil {
		return "[location]"
	}
	if rxn := msg.GetReactionMessage(); rxn != nil {
		emoji := rxn.GetText()
		if emoji == "" {
			emoji = "reaction"
		}
		return "[reaction: " + emoji + "]"
	}
	if pay := msg.GetSendPaymentMessage(); pay != nil {
		return "[payment]"
	}
	if pay := msg.GetRequestPaymentMessage(); pay != nil {
		return "[payment request]"
	}
	return ""
}

// extractMediaInfo pulls out the storable media fields from a Message.
// directPath is captured verbatim from the media protobuf and includes the
// signed `?ccb=&oh=&oe=&_nc_sid=` CDN auth query, so the download path can
// hand it straight to whatsmeow without any URL string manipulation.
func extractMediaInfo(msg *waProto.Message) (mediaType, filename, url, directPath string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", "", nil, nil, nil, 0
	}

	if img := msg.GetImageMessage(); img != nil {
		return "image", generatedMediaFilename("image", img.GetFileSHA256(), ".jpg"),
			img.GetURL(), img.GetDirectPath(),
			img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", generatedMediaFilename("video", vid.GetFileSHA256(), ".mp4"),
			vid.GetURL(), vid.GetDirectPath(),
			vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", generatedMediaFilename("audio", aud.GetFileSHA256(), ".ogg"),
			aud.GetURL(), aud.GetDirectPath(),
			aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		fname := security.SafeFilename(doc.GetFileName())
		return "document", fname,
			doc.GetURL(), doc.GetDirectPath(),
			doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}
	if sticker := msg.GetStickerMessage(); sticker != nil {
		return "sticker", generatedMediaFilename("sticker", sticker.GetFileSHA256(), ".webp"),
			sticker.GetURL(), sticker.GetDirectPath(),
			sticker.GetMediaKey(), sticker.GetFileSHA256(), sticker.GetFileEncSHA256(), sticker.GetFileLength()
	}
	// Location, reaction, and payment messages carry no downloadable payload,
	// so they stay text-only placeholders and must not appear in
	// list_media_messages.

	return "", "", "", "", nil, nil, nil, 0
}

func generatedMediaFilename(prefix string, fileSHA256 []byte, extension string) string {
	digest := hex.EncodeToString(fileSHA256)
	if digest == "" {
		digest = "unknown"
	}
	return prefix + "_" + digest + extension
}

// extractPollOptionNames pulls the option names out of a PollCreationMessage.
// Returns nil if the input is nil or the options list is empty.
func extractPollOptionNames(pc *waProto.PollCreationMessage) []string {
	if pc == nil {
		return nil
	}
	opts := pc.GetOptions()
	if len(opts) == 0 {
		return nil
	}
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.GetOptionName())
	}
	return out
}

// handleLabelEdit stores a label edit event (label created/updated/deleted).
func (c *Client) handleLabelEdit(evt *events.LabelEdit) {
	if c.metricSink != nil {
		c.metricSink.IncMessagesIn()
	}
	labelID := evt.LabelID
	labelName := ""
	color := ""
	if action := evt.Action; action != nil {
		if action.Name != nil {
			labelName = *action.Name
		}
		if action.Color != nil {
			color = fmt.Sprintf("%d", *action.Color)
		}
	}

	if err := c.store.StoreLabel(labelID, labelName, color); err != nil {
		c.log.Warnf("Failed to store label %s: %v", labelID, err)
		return
	}
	c.log.Infof("[label] %s: name=%s color=%s", labelID, labelName, color)
}

// handleLabelAssociationChat stores a label association event (chat labeled/unlabeled).
func (c *Client) handleLabelAssociationChat(evt *events.LabelAssociationChat) {
	if c.metricSink != nil {
		c.metricSink.IncMessagesIn()
	}
	chatJID := evt.JID.String()
	labelID := evt.LabelID
	labeled := false
	if action := evt.Action; action != nil {
		labeled = action.GetLabeled()
	}

	if err := c.store.StoreLabelAssociation(chatJID, labelID, labeled); err != nil {
		c.log.Warnf("Failed to store label association %s/%s: %v", chatJID, labelID, err)
		return
	}
	actionStr := "unlabeled"
	if labeled {
		actionStr = "labeled"
	}
	c.log.Infof("[label] %s: %s with label %s", actionStr, c.redactor.JID(chatJID), labelID)
}

// handlePollVote decrypts an incoming PollUpdateMessage, reverses its
// SelectedOptions SHA-256 hashes against the cached option names, and records
// the voter's current selection. Failures are logged (without poll content)
// and swallowed so a broken vote never blocks the rest of message handling.
// voterJID must be the full post-LID-resolution JID string.
func (c *Client) handlePollVote(msg *events.Message, chatJID, voterJID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vote, err := c.wa.DecryptPollVote(ctx, msg)
	if err != nil {
		c.log.Warnf("Failed to decrypt poll vote from %s in %s: %v", voterJID, chatJID, err)
		return
	}

	pu := msg.Message.GetPollUpdateMessage()
	key := pu.GetPollCreationMessageKey()
	pollID := key.GetID()
	if pollID == "" {
		c.log.Warnf("Poll vote missing poll creation message id (chat=%s)", chatJID)
		return
	}

	// Scope lookup to our locally-observed chatJID; ignore key.RemoteJID to
	// prevent a crafted update from another chat polluting this chat's tally.
	options, err := c.store.GetPollOptions(ctx, pollID, chatJID)
	if err != nil {
		// No cached metadata — we've probably never seen the poll creation
		// message. Log without listing option text and bail.
		c.log.Warnf("Poll vote for unknown poll %s in %s: %v", pollID, chatJID, err)
		return
	}

	optionHashes := whatsmeow.HashPollOptions(options)
	selectedHashes := vote.GetSelectedOptions()
	selected := make([]string, 0, len(selectedHashes))
	for _, h := range selectedHashes {
		for i, known := range optionHashes {
			if bytes.Equal(h, known) {
				selected = append(selected, options[i])
				break
			}
		}
	}

	// Clamp the peer-supplied timestamp to now so a malicious far-future value
	// can't permanently suppress later legitimate updates via the replay guard.
	ts := msg.Info.Timestamp
	if now := time.Now(); ts.After(now) {
		ts = now
	}

	if err := c.store.StorePollVote(ctx, pollID, chatJID, voterJID, selected, ts); err != nil {
		c.log.Warnf("Failed to store poll vote for poll %s in %s: %v", pollID, chatJID, err)
		return
	}

	c.log.Debugf("Recorded poll vote from %s on poll %s with %d selections", voterJID, pollID, len(selected))
}
