package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type AnchorMode string

const (
	AnchorNewest AnchorMode = "newest"
	AnchorOldest AnchorMode = "oldest"
)

const DefaultSyncCount = 50

type LIDMode bool

// WhatsApp resolves an on-demand history-sync cursor by message key (id +
func (c *Client) RequestHistorySync(ctx context.Context, chatJID string, fromTimestamp time.Time, anchor AnchorMode, count int, useLID LIDMode) (string, error) {
	c.log.Infof("[SYNC] Requesting history sync for chat: %s (anchor=%q, from=%v)", chatJID, anchor, fromTimestamp)

	if c.wa == nil || !c.socketConnected() {
		return "", fmt.Errorf("client not connected")
	}
	if c.wa.Store.ID == nil {
		return "", fmt.Errorf("device not paired")
	}

	jid, err := types.ParseJID(chatJID)
	if err != nil {
		return "", fmt.Errorf("invalid JID format: %v", err)
	}

	if count <= 0 {
		count = DefaultSyncCount
	}

	msgID, timestamp, isFromMe, describe, err := c.resolveAnchor(chatJID, fromTimestamp, anchor)
	if err != nil {
		return "", err
	}
	c.log.Infof("[SYNC] Anchored on %s: ID=%s, timestamp=%v, isFromMe=%v", describe, msgID, timestamp, isFromMe)

	requestJID := jid
	if useLID && jid.Server == types.DefaultUserServer {
		if lidJID, lerr := c.wa.Store.LIDs.GetLIDForPN(ctx, jid); lerr == nil && !lidJID.IsEmpty() {
			c.log.Infof("[SYNC] Found LID mapping: %s -> %s", jid.String(), lidJID.String())
			requestJID = lidJID
		} else {
			c.log.Infof("[SYNC] No LID mapping found for %s, using original JID", jid.String())
		}
	} else if !useLID {
		c.log.Infof("[SYNC] LID resolution disabled; requesting with phone-number JID %s", jid.String())
	}

	messageInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     requestJID,
			IsFromMe: isFromMe,
		},
		ID:        msgID,
		Timestamp: timestamp,
	}

	historyMsg := c.wa.BuildHistorySyncRequest(messageInfo, count, 0)
	if historyMsg == nil {
		return "", fmt.Errorf("failed to build history sync request")
	}

	ownJID := c.wa.Store.ID.ToNonAD()
	c.log.Infof("[SYNC] Sending peer message to own JID: %s", ownJID.String())
	c.log.Infof("[SYNC] Request details: ChatJID=%s (request uses %s), MsgID=%s, IsFromMe=%v, Timestamp=%v, Count=%d",
		chatJID, requestJID.String(), msgID, isFromMe, timestamp, count)

	resp, err := c.wa.SendMessage(ctx, ownJID, historyMsg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", fmt.Errorf("failed to send sync request: %v", err)
	}
	c.log.Infof("[SYNC] Send response: ID=%s, Timestamp=%v", resp.ID, resp.Timestamp)

	return fmt.Sprintf(
		"History sync requested for %s (requesting %d messages before %s, anchored on the %s cached message). "+
			"Delivery is asynchronous; re-query list_messages shortly. An empty result means the server "+
			"served nothing for that cursor — usually because the history predates WhatsApp's retention "+
			"for linked devices.",
		chatJID, count, timestamp.Format(time.RFC3339), describe), nil
}

func (c *Client) resolveAnchor(chatJID string, fromTimestamp time.Time, anchor AnchorMode) (id string, ts time.Time, isFromMe bool, describe string, err error) {
	switch {
	case anchor == AnchorOldest:
		id, ts, isFromMe, err = c.store.GetOldestMessage(chatJID)
		describe = "oldest"
	case !fromTimestamp.IsZero():
		id, ts, isFromMe, err = c.store.GetMessageAtOrBefore(chatJID, fromTimestamp)
		describe = "newest at or before the requested timestamp"
		if errors.Is(err, sql.ErrNoRows) {
			c.log.Infof("[SYNC] No cached message at or before %v; falling back to oldest", fromTimestamp)
			id, ts, isFromMe, err = c.store.GetOldestMessage(chatJID)
			describe = "oldest (nothing cached at or before the requested timestamp)"
		}
	default:
		id, ts, isFromMe, err = c.store.GetNewestMessage(chatJID)
		describe = "newest"
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, false, "", fmt.Errorf(
				"no cached messages for chat %s: cannot anchor a history sync without at least one known message", chatJID)
		}
		return "", time.Time{}, false, "", fmt.Errorf("failed to resolve sync anchor for %s: %v", chatJID, err)
	}
	return id, ts, isFromMe, describe, nil
}

func (c *Client) RequestStatusSync(ctx context.Context, count int) (string, error) {
	if c.wa == nil || !c.socketConnected() {
		return "", fmt.Errorf("client not connected")
	}
	if c.wa.Store.ID == nil {
		return "", fmt.Errorf("device not paired")
	}
	if count <= 0 {
		count = DefaultSyncCount
	}

	messageInfo := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     types.StatusBroadcastJID,
			IsFromMe: false,
		},
	}

	historyMsg := c.wa.BuildHistorySyncRequest(messageInfo, count, 1)
	if historyMsg == nil {
		return "", fmt.Errorf("failed to build status sync request")
	}

	ownJID := c.wa.Store.ID.ToNonAD()
	c.log.Infof("[SYNC] Requesting status backfill for %s (mode=INITIAL_STATUS_V3, count=%d)",
		types.StatusBroadcastJID, count)

	resp, err := c.wa.SendMessage(ctx, ownJID, historyMsg, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		return "", fmt.Errorf("failed to send status sync request: %v", err)
	}
	c.log.Infof("[SYNC] Status sync request sent: ID=%s, Timestamp=%v", resp.ID, resp.Timestamp)

	return fmt.Sprintf(
		"Status sync requested for %s (requesting %d status updates, mode=INITIAL_STATUS_V3). "+
			"Delivery is asynchronous; re-query list_status_updates shortly.",
		types.StatusBroadcastJID, count), nil
}
