package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

func (c *Client) SendPoll(ctx context.Context, recipient, question string, options []string, selectableCount int) SendResult {
	if len(options) < 2 {
		return SendResult{Success: false, Message: "poll needs at least 2 options"}
	}
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	if selectableCount <= 0 {
		selectableCount = 1
	}

	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}
	if err := c.checkSendWindow(ctx, recipientJID); err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	msg := c.wa.BuildPollCreation(question, options, selectableCount)

	resp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending poll: %v", err)}
	}

	c.persistSent(ctx, recipientJID, resp.ID, question, "", msg)

	if err := c.store.StorePollMetadata(ctx, resp.ID, recipientJID.String(), options); err != nil {
		c.log.Warnf("Failed to store poll metadata for %s: %v", resp.ID, err)
	}

	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Poll sent to %s", recipient),
		ID:      resp.ID,
	}
}

func (c *Client) SendPollVote(ctx context.Context, chatJID, pollMessageID string, options []string) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	if pollMessageID == "" {
		return SendResult{Success: false, Message: "poll_message_id is required"}
	}
	if len(options) == 0 {
		return SendResult{Success: false, Message: "options must not be empty"}
	}

	chat, err := parseRecipient(chatJID)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}
	if err := c.checkSendWindow(ctx, chat); err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	creator, err := c.store.GetPollCreator(ctx, pollMessageID, chat.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SendResult{Success: false, Message: "poll not found in cache — wait for message sync"}
		}
		return SendResult{Success: false, Message: fmt.Sprintf("Error looking up poll: %v", err)}
	}
	sender, err := resolvePollSenderJID(creator, chat)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	msgInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: false,
			IsGroup:  chat.Server == types.GroupServer,
		},
		ID: pollMessageID,
	}

	voteMsg, err := c.wa.BuildPollVote(ctx, msgInfo, options)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error building poll vote: %v", err)}
	}

	resp, err := c.wa.SendMessage(ctx, chat, voteMsg)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending poll vote: %v", err)}
	}

	ownJID := chat.String()
	if c.wa.Store.ID != nil {
		ownJID = c.wa.Store.ID.ToNonAD().String()
	}
	if _, err := c.store.StorePollVote(ctx, pollMessageID, chat.String(), ownJID, options, time.Now()); err != nil {
		c.log.Warnf("Record own poll vote on %s: %v", pollMessageID, err)
	}

	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Poll vote sent to %s", chatJID),
		ID:      resp.ID,
	}
}

type PollResults struct {
	PollMessageID string         `json:"poll_message_id"`
	ChatJID       string         `json:"chat_jid"`
	Tally         map[string]int `json:"tally"` // option name -> vote count
}

func (c *Client) GetPollResults(ctx context.Context, chatJID, pollMessageID string) (*PollResults, error) {
	if chatJID == "" {
		return nil, errors.New("chat_jid is required")
	}
	if pollMessageID == "" {
		return nil, errors.New("poll_message_id is required")
	}
	if c.store == nil {
		return nil, errors.New("poll store not initialised")
	}
	opts, results, err := c.store.GetPollResults(ctx, pollMessageID, chatJID)
	if err != nil {
		return nil, err
	}
	tally := make(map[string]int, len(opts))
	for _, o := range opts {
		tally[o] = 0
	}
	for _, r := range results {
		tally[r.Option] = r.Votes
	}
	return &PollResults{
		PollMessageID: pollMessageID,
		ChatJID:       chatJID,
		Tally:         tally,
	}, nil
}

func resolvePollSenderJID(sender string, chat types.JID) (types.JID, error) {
	if sender == "" {
		return types.JID{}, errors.New("poll creator JID not known — wait for message sync")
	}
	if strings.Contains(sender, "@") {
		jid, err := types.ParseJID(sender)
		if err != nil {
			return types.JID{}, fmt.Errorf("parse poll sender JID: %w", err)
		}
		return jid, nil
	}
	server := types.DefaultUserServer
	if chat.Server != types.GroupServer && chat.Server != "" {
		server = chat.Server
	}
	return types.JID{User: sender, Server: server}, nil
}

func (c *Client) SendContactCard(ctx context.Context, recipient, name, phone, vcardOverride string) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	if name == "" && phone == "" && vcardOverride == "" {
		return SendResult{Success: false, Message: errors.New("contact requires name, phone, or vcard").Error()}
	}

	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}
	if err := c.checkSendWindow(ctx, recipientJID); err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	vcard := vcardOverride
	if vcard == "" {
		vcard = buildVCard(name, phone)
	}

	displayName := name
	if displayName == "" {
		displayName = phone
	}

	msg := &waE2E.Message{
		ContactMessage: &waE2E.ContactMessage{
			DisplayName: proto.String(displayName),
			Vcard:       proto.String(vcard),
		},
	}

	resp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending contact card: %v", err)}
	}

	c.persistSent(ctx, recipientJID, resp.ID, displayName+" (contact card)", "", msg)

	return SendResult{
		Success: true,
		Message: fmt.Sprintf("Contact card sent to %s", recipient),
		ID:      resp.ID,
	}
}
