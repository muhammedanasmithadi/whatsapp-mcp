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

type messageKind int

const (
	messageKindNormal messageKind = iota
	messageKindEdit
)

func (c *Client) handleCallOffer(evt *events.CallOffer) {
	chat := evt.From.ToNonAD().String()
	if !evt.GroupJID.IsEmpty() {
		chat = evt.GroupJID.String()
	}
	kind := "voice"
	if evt.Data != nil && len(evt.Data.GetChildren()) > 0 {
		for _, child := range evt.Data.GetChildren() {
			if child.Tag == "video" {
				kind = "video"
				break
			}
		}
	}
	c.log.Infof("Incoming %s call from %s (call %s)", kind, c.redactor.JID(evt.From.String()), c.redactor.MsgID(evt.CallID))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Store().StoreChat(chat, c.Store().FindChatName(chat), evt.Timestamp); err != nil {
		c.log.Warnf("Store chat for incoming call: %v", err)
		return
	}
	err := c.Store().StoreMessage(ctx, store.Message{
		ID:        evt.CallID,
		ChatJID:   chat,
		Sender:    evt.From.User,
		Content:   "[" + kind + " call]",
		Timestamp: evt.Timestamp,
	}, nil, nil, nil, 0)
	if err != nil {
		c.log.Warnf("Store incoming call: %v", err)
	}
}

func (c *Client) StartEventHandler() {
	if c.handlerState == handlerInstalled {
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
		case *events.Contact:
			c.handleContact(v)
		case *events.PushName:
			c.handlePushName(v)
		case *events.CallOffer:
			c.handleCallOffer(v)
		case *events.CallTerminate:
			c.log.Infof("Call %s from %s ended", c.redactor.MsgID(v.CallID), c.redactor.JID(v.From.String()))
		default:
			c.log.Debugf("Unhandled event type: %T", v)
		}
	})
	c.handlerState = handlerInstalled
}

type normalizedMessage struct {
	msg           store.Message
	mediaKey      []byte
	fileSHA256    []byte
	fileEncSHA256 []byte
	fileLength    uint64
	senderFullJID string // post-LID sender as "user@server"
	chatJID       string // post-LID-normalised chat JID
}

func (c *Client) normalizeIncomingMessage(
	rawChatJID, rawSenderJID, senderUser string,
	isGroup, isFromMe bool,
	ownID string,
	msgID string,
	timestamp time.Time,
	raw *waProto.Message,
) normalizedMessage {
	chatJID := rawChatJID
	if !isGroup {
		chatJID = c.store.ResolveLIDToJID(rawChatJID)
		if chatJID != rawChatJID {
			c.log.Infof("Normalized chat JID: %s -> %s", c.redactor.JID(rawChatJID), c.redactor.JID(chatJID))
		}
	}

	sender := senderUser
	normalizedSender := c.store.ResolveLIDToJID(rawSenderJID)
	if normalizedSender != rawSenderJID {
		sender = strings.Split(normalizedSender, "@")[0]
		c.log.Infof("Normalized sender: %s -> %s", c.redactor.JID(senderUser), c.redactor.JID(sender))
	}

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

	if msg.Info.SenderAlt.String() != "" && msg.Info.SenderAlt.String() != msg.Info.Sender.String() {
		senderLID := msg.Info.Sender.String()
		senderPN := msg.Info.SenderAlt.String()
		if strings.HasSuffix(senderLID, "@lid") && strings.HasSuffix(senderPN, "@s.whatsapp.net") {
			lid := strings.TrimSuffix(senderLID, "@lid")
			pn := strings.TrimSuffix(senderPN, "@s.whatsapp.net")
			if err := c.store.RecordEvidence(lid+"@lid", pn+"@s.whatsapp.net", "message_alt", "", true); err != nil {
				c.log.Warnf("Failed to record message evidence: %v", err)
			}
		}
		if strings.HasSuffix(senderPN, "@lid") && strings.HasSuffix(senderLID, "@s.whatsapp.net") {
			lid := strings.TrimSuffix(senderPN, "@lid")
			pn := strings.TrimSuffix(senderLID, "@s.whatsapp.net")
			if err := c.store.RecordEvidence(lid+"@lid", pn+"@s.whatsapp.net", "message_alt", "", true); err != nil {
				c.log.Warnf("Failed to record message evidence: %v", err)
			}
		}
	}

	name := c.GetChatName(msg.Info.Chat, nm.chatJID, nil, nm.msg.Sender)

	if err := c.store.StoreChat(nm.chatJID, name, msg.Info.Timestamp); err != nil {
		c.log.Warnf("Failed to store chat: %v", err)
	}

	if msg.Message != nil && msg.Message.GetPollUpdateMessage() != nil {
		voterFullJID := nm.senderFullJID
		if voterFullJID == "" {
			voterFullJID = msg.Info.Sender.String()
		}
		c.handlePollVote(msg, nm.chatJID, voterFullJID)
		return
	}

	var pollOptions []string
	if msg.Message != nil {
		if pc := msg.Message.GetPollCreationMessage(); pc != nil {
			pollOptions = extractPollOptionNames(pc)
			if nm.msg.Content == "" {
				nm.msg.Content = "[poll] " + pc.GetName()
			}
		}
	}

	messageKind := messageKindNormal
	if msg.IsEdit {
		messageKind = messageKindEdit
	}

	if messageKind == messageKindEdit {
		if err := c.store.UpdateMessage(context.Background(), msg.Info.ID, nm.chatJID, nm.msg.Content); err != nil {
			if err == sql.ErrNoRows {
				c.log.Warnf("Edit for unknown message %s in chat %s, inserting as new",
					c.redactor.MsgID(msg.Info.ID), c.redactor.JID(nm.chatJID))
			} else {
				c.log.Warnf("Failed to apply edit to message %s: %v", c.redactor.MsgID(msg.Info.ID), err)
				c.incErrors()
				return
			}
		} else {
			c.incMessagesIn()
			c.log.Infof("[%s] %s %s: EDITED %s", msg.Info.Timestamp.Format("2006-01-02 15:04:05"),
				"<-", c.redactor.JID(nm.msg.Sender), c.redactor.JID(nm.chatJID))
			return
		}
	}

	if nm.msg.Content == "" && nm.msg.MediaType == "" {
		if nm.msg.ChatJID == store.StatusBroadcastJID {
			nm.msg.Content = "[status update]"
			nm.msg.MediaType = "status"
		} else {
			return
		}
	}

	err := c.store.StoreMessage(context.Background(), nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength)

	if err == nil {
		c.incMessagesIn()
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

		chatJID := nm.chatJID
		content := nm.msg.Content
		isFromMe := msg.Info.IsFromMe
		go func() {
			decision, err := c.store.CanAutoRespond(context.Background(), chatJID)
			if err != nil {
				c.log.Warnf("auto-responder: config check failed: %v", err)
				return
			}
			if decision != store.AutoRespondAllowed {
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
		c.incErrors()
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
				if nm.msg.ChatJID == store.StatusBroadcastJID {
					nm.msg.Content = "[status update]"
					nm.msg.MediaType = "status"
				} else {
					continue
				}
			}

			nm.msg.ChatJID = chatJID

			if err := c.store.StoreMessage(ctx, nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength); err != nil {
				c.log.Warnf("Failed to store history message: %v", err)
				c.incErrors()
				continue
			}
			c.incMessagesIn()
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

	statusCount := c.importStatusV3Messages(ctx, ownID, historySync.Data.GetStatusV3Messages())

	c.log.Infof("History sync complete. Stored %d messages and %d status updates.", syncedCount, statusCount)
}

// (HistorySync.statusV3Messages, field 3), and the INITIAL_STATUS_V3 sync
func (c *Client) importStatusV3Messages(ctx context.Context, ownID string, messages []*waWeb.WebMessageInfo) int {
	if len(messages) == 0 {
		return 0
	}
	if err := c.store.EnsureChat(store.StatusBroadcastJID, "Status"); err != nil {
		c.log.Warnf("Failed to ensure status chat row: %v", err)
		c.incErrors()
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
		nm.msg.ChatJID = store.StatusBroadcastJID
		if nm.msg.Sender == "" {
			c.log.Warnf("Skipping status history message %s with no sender", c.redactor.MsgID(msgID))
			continue
		}
		if nm.msg.Content == "" && nm.msg.MediaType == "" {
			nm.msg.Content = "[status update]"
			nm.msg.MediaType = "status"
		}
		if err := c.store.StoreMessage(ctx, nm.msg, nm.mediaKey, nm.fileSHA256, nm.fileEncSHA256, nm.fileLength); err != nil {
			c.log.Warnf("Failed to store status history message %s: %v", c.redactor.MsgID(msgID), err)
			c.incErrors()
			continue
		}
		c.incMessagesIn()
		stored++
		c.log.Infof("[%s] status <- %s: [%s] %s",
			timestamp.Format("2006-01-02 15:04:05"), c.redactor.JID(nm.msg.Sender),
			nm.msg.MediaType, c.redactor.Body(nm.msg.Content))
	}
	return stored
}

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

func (c *Client) resolveChatNameFallback(ctx context.Context, jid types.JID, sender string) string {
	if jid.Server == "g.us" {
		if gi, err := c.wa.GetGroupInfo(ctx, jid); err == nil && gi.Name != "" {
			return gi.Name
		}
		return "Group " + jid.User
	}
	if c.wa != nil {
		if contact, err := c.wa.Store.Contacts.GetContact(ctx, jid); err == nil && contact.FullName != "" {
			return contact.FullName
		}
	}
	if sender != "" {
		return sender
	}
	return jid.User
}

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

// signed `?ccb=&oh=&oe=&_nc_sid=` CDN auth query, so the download path can
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

	return "", "", "", "", nil, nil, nil, 0
}

func generatedMediaFilename(prefix string, fileSHA256 []byte, extension string) string {
	digest := hex.EncodeToString(fileSHA256)
	if digest == "" {
		digest = "unknown"
	}
	return prefix + "_" + digest + extension
}

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

func (c *Client) handleLabelEdit(evt *events.LabelEdit) {
	c.incMessagesIn()
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

func (c *Client) handleLabelAssociationChat(evt *events.LabelAssociationChat) {
	c.incMessagesIn()
	chatJID := evt.JID.String()
	labelID := evt.LabelID
	labeled := store.LabelUnlabeled
	if action := evt.Action; action != nil {
		if action.GetLabeled() {
			labeled = store.LabelLabeled
		}
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

func (c *Client) handleContact(evt *events.Contact) {
	jid := c.store.ResolveLIDToJID(evt.JID.String())
	if evt.Action == nil {
		if err := c.store.MarkContactStale(jid); err != nil {
			c.log.Warnf("Failed to mark contact stale %s: %v", jid, err)
		} else {
			c.log.Infof("[contact] removed on another device: %s", c.redactor.JID(jid))
		}
		return
	}
	username := ""
	if evt.Action != nil {
		username = evt.Action.GetUsername()
	}
	if evt.Action != nil {
		lidJID := evt.Action.GetLidJID()
		pnJID := evt.Action.GetPnJID()
		if lidJID != "" && pnJID != "" {
			lid := strings.TrimSuffix(lidJID, "@lid")
			pn := strings.TrimSuffix(pnJID, "@s.whatsapp.net")
			if err := c.store.RecordEvidence(lid+"@lid", pn+"@s.whatsapp.net", "app_state", "", true); err != nil {
				c.log.Warnf("Failed to record contact evidence: %v", err)
			}
		}
	}
	nameSource := "synced"
	if err := c.store.UpsertContactProfile(jid, username, nameSource, "", false); err != nil {
		c.log.Warnf("Failed to upsert contact profile %s: %v", jid, err)
	}
	c.log.Debugf("[contact] updated on another device: %s username=%s", c.redactor.JID(jid), username)
}

func (c *Client) handlePushName(evt *events.PushName) {
	c.log.Debugf("[pushname] %s -> %s", c.redactor.JID(evt.JID.String()), evt.NewPushName)
}

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

	options, err := c.store.GetPollOptions(ctx, pollID, chatJID)
	if err != nil {
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

	ts := msg.Info.Timestamp
	if now := time.Now(); ts.After(now) {
		ts = now
	}

	accepted, err := c.store.StorePollVote(ctx, pollID, chatJID, voterJID, selected, ts)
	if err != nil {
		c.log.Warnf("Failed to store poll vote for poll %s in %s: %v", pollID, chatJID, err)
		return
	}
	if !accepted {
		c.log.Debugf("Ignored stale poll vote from %s on poll %s", voterJID, pollID)
		return
	}

	c.log.Debugf("Recorded poll vote from %s on poll %s with %d selections", voterJID, pollID, len(selected))
}
