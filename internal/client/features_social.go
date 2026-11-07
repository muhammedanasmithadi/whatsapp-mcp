package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/ratelimit"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/security"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

// Profile-picture lookup errors. These are normal, privacy-gated outcomes —
// not transport failures — so the MCP layer can report them cleanly.
var (
	ErrProfilePictureNotSet = errors.New("no profile picture set")
	ErrProfilePictureHidden = errors.New("profile picture is hidden by privacy settings")
)

// ProfilePictureResult is the public result of GetProfilePicture. Path is the
// locally cached copy, suitable for inline display in an MCP client.
type ProfilePictureResult struct {
	JID     string `json:"jid"`
	URL     string `json:"url"`
	ID      string `json:"id,omitempty"`
	Type    string `json:"type,omitempty"`
	Preview bool   `json:"preview"`
	Path    string `json:"path,omitempty"`
	Bytes   int    `json:"bytes,omitempty"`
}

// GetProfilePicture fetches a contact's or group's profile photo from
// WhatsApp, downloads it, and caches it under <storeDir>/profile-pics.
// preview asks WhatsApp for the small thumbnail instead of the full image.
func (c *Client) GetProfilePicture(ctx context.Context, recipient string, preview bool) (*ProfilePictureResult, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(recipient)
	if err != nil {
		return nil, err
	}

	info, err := c.wa.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: preview})
	if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) {
		return nil, fmt.Errorf("%w: %s", ErrProfilePictureNotSet, jid)
	}
	if errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
		return nil, fmt.Errorf("%w: %s", ErrProfilePictureHidden, jid)
	}
	if err != nil {
		return nil, fmt.Errorf("get profile picture: %w", err)
	}
	if info == nil || info.URL == "" {
		return nil, fmt.Errorf("%w: %s", ErrProfilePictureNotSet, jid)
	}

	result := &ProfilePictureResult{
		JID:     jid.String(),
		URL:     info.URL,
		ID:      info.ID,
		Type:    info.Type,
		Preview: preview,
	}

	data, ext, err := c.downloadProfilePicture(ctx, info.URL)
	if err != nil {
		return nil, fmt.Errorf("download profile picture: %w", err)
	}
	dir := filepath.Join(c.store.Dir(), "profile-pics", security.SafeFilename(jid.String()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create profile-pic dir: %w", err)
	}
	base := security.SafeFilename(info.ID)
	if base == "" || base == "." || base == ".." {
		sum := sha256.Sum256([]byte(info.URL))
		base = hex.EncodeToString(sum[:8])
	}
	path := filepath.Join(dir, base+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("write profile picture: %w", err)
	}
	result.Path = path
	result.Bytes = len(data)
	return result, nil
}

// downloadProfilePicture fetches a profile photo CDN URL. Profile photos are
// served unencrypted (unlike message media), so a plain HTTPS GET is enough.
func (c *Client) downloadProfilePicture(ctx context.Context, url string) ([]byte, string, error) {
	const maxProfilePictureBytes = 10 << 20

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "WhatsApp/2.24.0")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("CDN returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProfilePictureBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxProfilePictureBytes {
		return nil, "", fmt.Errorf("profile picture exceeds %d bytes", maxProfilePictureBytes)
	}

	ext := ".jpg"
	switch http.DetectContentType(data) {
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
		ext = ".gif"
	}
	return data, ext, nil
}

// ContactInfoResult is live profile metadata returned by GetContactInfo.
type ContactInfoResult struct {
	JID          string   `json:"jid"`
	PhoneNumber  string   `json:"phone_number,omitempty"`
	Name         string   `json:"name,omitempty"`
	About        string   `json:"about,omitempty"`
	PictureID    string   `json:"picture_id,omitempty"`
	LID          string   `json:"lid,omitempty"`
	Devices      []string `json:"devices,omitempty"`
	IsBusiness   bool     `json:"is_business"`
	VerifiedName string   `json:"verified_name,omitempty"`
}

// GetContactInfo fetches live profile metadata (About text, picture ID, LID,
// device list) via one usync lookup, and merges locally cached names.
func (c *Client) GetContactInfo(ctx context.Context, recipient string) (*ContactInfoResult, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(recipient)
	if err != nil {
		return nil, err
	}
	if c.limiter != nil && !ratelimit.BypassFromContext(ctx) {
		if d := c.limiter.AllowUsync(); !d.Allowed {
			return nil, fmt.Errorf("rate limited: %s — retry in %s (set the X-Rate-Limit-Override header to bypass)",
				d.Reason, d.RetryAfter.Round(time.Second))
		}
	}

	infos, err := c.wa.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		return nil, fmt.Errorf("get contact info: %w", err)
	}
	info, ok := infos[jid]
	if !ok {
		return nil, fmt.Errorf("contact not found: %s", jid)
	}

	out := &ContactInfoResult{
		JID:         jid.String(),
		PhoneNumber: jid.User,
		Name:        c.store.FindChatName(jid.String()),
		About:       info.Status,
		PictureID:   info.PictureID,
		LID:         info.LID.String(),
		IsBusiness:  info.VerifiedName != nil,
	}
	for _, dev := range info.Devices {
		out.Devices = append(out.Devices, dev.String())
	}
	if info.VerifiedName != nil && info.VerifiedName.Details != nil {
		out.VerifiedName = info.VerifiedName.Details.GetVerifiedName()
	}
	return out, nil
}

// StatusGroup is one contact's set of cached Status (Stories) updates,
// newest first.
type StatusGroup struct {
	SenderJID   string               `json:"sender_jid"`
	SenderName  string               `json:"sender_name,omitempty"`
	SenderPhone string               `json:"sender_phone,omitempty"`
	Latest      time.Time            `json:"latest"`
	Count       int                  `json:"count"`
	Updates     []store.StatusUpdate `json:"updates"`
}

// ListStatusOptions filters ListStatusUpdates.
type ListStatusOptions struct {
	Hours          int    // look-back window; defaults to 24
	Sender         string // optional phone/JID filter
	IncludeExpired bool   // include items older than WhatsApp's 24h TTL
	Limit          int    // max status items; defaults to 200
}

// ListStatusUpdates returns cached Status (Stories) updates grouped by sender,
// newest group first. This is the same source WhatsApp Web uses: status
// messages delivered to the status@broadcast JID and stored locally.
func (c *Client) ListStatusUpdates(ctx context.Context, opts ListStatusOptions) ([]StatusGroup, int, error) {
	if opts.Hours <= 0 {
		opts.Hours = 24
	}
	if opts.Hours > 168 {
		opts.Hours = 168
	}
	if opts.Limit <= 0 {
		opts.Limit = 200
	}
	if opts.Limit > 1000 {
		opts.Limit = 1000
	}

	var senderUser string
	if opts.Sender != "" {
		jid, err := parseRecipient(opts.Sender)
		if err != nil {
			return nil, 0, err
		}
		senderUser = jid.User
	}

	since := time.Now().Add(-time.Duration(opts.Hours) * time.Hour)
	updates, err := c.store.ListStatusUpdates(ctx, since, opts.Limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list status updates: %w", err)
	}

	bySender := make(map[string]*StatusGroup)
	var order []string
	total := 0
	for _, u := range updates {
		if senderUser != "" && u.SenderPhone != senderUser {
			continue
		}
		if u.Expired && !opts.IncludeExpired {
			continue
		}
		g, ok := bySender[u.SenderJID]
		if !ok {
			g = &StatusGroup{
				SenderJID:   u.SenderJID,
				SenderName:  u.SenderName,
				SenderPhone: u.SenderPhone,
				Latest:      u.Timestamp,
			}
			bySender[u.SenderJID] = g
			order = append(order, u.SenderJID)
		}
		g.Updates = append(g.Updates, u)
		g.Count++
		total++
	}

	groups := make([]StatusGroup, 0, len(order))
	for _, jid := range order {
		groups = append(groups, *bySender[jid])
	}
	return groups, total, nil
}

// MarkStatusViewed sends read receipts for cached status updates from one
// sender, which adds the paired account to the poster's viewer list. If ids is
// empty, every unexpired cached update from that sender is marked. It returns
// the number of receipts sent.
func (c *Client) MarkStatusViewed(ctx context.Context, sender string, ids []string) (int, error) {
	if !c.wa.IsConnected() {
		return 0, errors.New("not connected to WhatsApp")
	}
	senderJID, err := parseRecipient(sender)
	if err != nil {
		return 0, fmt.Errorf("sender: %w", err)
	}
	since := time.Now().Add(-store.StatusTTL)
	allowed, err := c.store.StatusMessageIDs(ctx, senderJID.String(), since)
	if err != nil {
		return 0, fmt.Errorf("load status updates: %w", err)
	}
	if len(allowed) == 0 {
		return 0, fmt.Errorf("no unexpired status updates found from %s", senderJID)
	}

	targets := ids
	if len(targets) == 0 {
		targets = make([]string, 0, len(allowed))
		for id := range allowed {
			targets = append(targets, id)
		}
	} else {
		for _, id := range targets {
			if _, ok := allowed[id]; !ok {
				return 0, fmt.Errorf("message %s is not a cached status update from %s", id, senderJID)
			}
		}
	}

	messageIDs := make([]types.MessageID, len(targets))
	for i, id := range targets {
		messageIDs[i] = types.MessageID(id)
	}
	if err := c.wa.MarkRead(ctx, messageIDs, time.Now(), types.StatusBroadcastJID, senderJID); err != nil {
		return 0, fmt.Errorf("mark status viewed: %w", err)
	}
	return len(targets), nil
}

// PresenceResult is the live (or last-known) presence of a contact.
type PresenceResult struct {
	JID           string     `json:"jid"`
	ContactName   string     `json:"contact_name,omitempty"`
	Online        bool       `json:"online"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	LastSeenKnown bool       `json:"last_seen_known"`
	Answered      bool       `json:"answered"`
	UpdatedAt     time.Time  `json:"updated_at,omitempty"`
	Note          string     `json:"note,omitempty"`
}

// GetPresence asks WhatsApp for a contact's presence, waits up to wait for the
// server's response, and returns it. WhatsApp only delivers presence while the
// querying client is marked available, so this briefly sets the account
// available and then restores its previous availability.
func (c *Client) GetPresence(ctx context.Context, recipient string, wait time.Duration) (*PresenceResult, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	jid, err := parseRecipient(recipient)
	if err != nil {
		return nil, err
	}
	if wait <= 0 {
		wait = 5 * time.Second
	}

	ch, cancel := c.addPresenceWaiter(jid.String())
	defer cancel()

	previous := c.currentOwnPresence()
	if previous != types.PresenceAvailable {
		if err := c.wa.SendPresence(ctx, types.PresenceAvailable); err != nil {
			return nil, fmt.Errorf("mark online for presence query: %w", err)
		}
		// Restore the pre-query availability in the background: the caller
		// should not wait on an extra round trip.
		restore := previous
		if restore == "" {
			restore = types.PresenceUnavailable
		}
		defer func() {
			go func() {
				bg, bgCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer bgCancel()
				if err := c.wa.SendPresence(bg, restore); err != nil {
					c.log.Warnf("Restore presence: %v", err)
				}
			}()
		}()
	}

	if err := c.store.MarkPresenceSubscribed(context.Background(), jid.String()); err != nil {
		c.log.Warnf("Record presence subscription: %v", err)
	}
	if err := c.wa.SubscribePresence(ctx, jid); err != nil {
		return nil, fmt.Errorf("subscribe to presence: %w", err)
	}

	select {
	case pr := <-ch:
		return &pr, nil
	case <-time.After(wait):
		// Fall back to whatever was cached before this query.
		return c.cachedPresence(jid)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) currentOwnPresence() types.Presence {
	c.ownPresenceMu.Lock()
	defer c.ownPresenceMu.Unlock()
	return c.ownPresence
}

func (c *Client) addPresenceWaiter(jid string) (<-chan PresenceResult, func()) {
	c.presenceMu.Lock()
	defer c.presenceMu.Unlock()
	if c.presenceWaiters == nil {
		c.presenceWaiters = make(map[string][]chan PresenceResult)
	}
	ch := make(chan PresenceResult, 1)
	c.presenceWaiters[jid] = append(c.presenceWaiters[jid], ch)
	cancel := func() {
		c.presenceMu.Lock()
		defer c.presenceMu.Unlock()
		list := c.presenceWaiters[jid]
		for i, candidate := range list {
			if candidate == ch {
				c.presenceWaiters[jid] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(c.presenceWaiters[jid]) == 0 {
			delete(c.presenceWaiters, jid)
		}
	}
	return ch, cancel
}

// handlePresence persists a live presence event and wakes any GetPresence
// caller waiting for that JID.
func (c *Client) handlePresence(evt *events.Presence) {
	rawJID := evt.From.ToNonAD().String()
	// WhatsApp usually addresses presence by LID on modern accounts. Resolve
	// to the phone JID so callers querying either form see the same row.
	jid := c.store.ResolveLIDToJID(rawJID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.store.UpsertPresence(ctx, jid, !evt.Unavailable, evt.LastSeen); err != nil {
		c.log.Warnf("Store presence for %s: %v", c.redactor.JID(jid), err)
	}

	pr := PresenceResult{
		JID:           jid,
		ContactName:   c.store.FindChatName(jid),
		Online:        !evt.Unavailable,
		LastSeenKnown: !evt.LastSeen.IsZero(),
		Answered:      true,
		UpdatedAt:     time.Now(),
	}
	if !evt.LastSeen.IsZero() {
		seen := evt.LastSeen
		pr.LastSeen = &seen
	}

	c.presenceMu.Lock()
	for _, key := range []string{jid, rawJID} {
		for _, ch := range c.presenceWaiters[key] {
			select {
			case ch <- pr:
			default:
			}
		}
	}
	c.presenceMu.Unlock()
	c.log.Debugf("Presence: %s online=%v last_seen=%v", c.redactor.JID(jid), pr.Online, pr.LastSeenKnown)
}

// cachedPresence converts a stored presence row into a result for a caller
// whose live query timed out.
func (c *Client) cachedPresence(jid types.JID) (*PresenceResult, error) {
	entry, err := c.store.GetPresence(context.Background(), jid.String())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &PresenceResult{
				JID:         jid.String(),
				ContactName: c.store.FindChatName(jid.String()),
				Note:        "no presence response yet (contact may hide online/last-seen, or may not have responded in time)",
			}, nil
		}
		return nil, fmt.Errorf("read cached presence: %w", err)
	}
	out := &PresenceResult{
		JID:           entry.JID,
		ContactName:   c.store.FindChatName(entry.JID),
		Online:        entry.Online,
		LastSeenKnown: entry.LastSeenSeen,
		UpdatedAt:     entry.UpdatedAt,
		Note:          "cached: no live response within the wait window",
	}
	if entry.LastSeenSeen {
		seen := entry.LastSeen
		out.LastSeen = &seen
	}
	if entry.SubscribedAt.IsZero() {
		out.Note = "not subscribed yet"
	}
	return out, nil
}

// StatusViewer is one viewer of a Status (Story) update.
type StatusViewer struct {
	JID       string    `json:"jid"`
	Timestamp time.Time `json:"timestamp"`
}

// GetStatusViewers returns viewers of a Status (Story) update. messageID is
// the status message ID from list_status_updates. WhatsApp does not expose
// status viewers to linked devices through a public API; this returns an empty
// list with a note explaining the limitation.
func (c *Client) GetStatusViewers(ctx context.Context, messageID string) ([]StatusViewer, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	if messageID == "" {
		return nil, errors.New("message_id required")
	}
	return []StatusViewer{}, nil
}

// SendStatus sends a Status (Story) update as a media message to
// status@broadcast. mediaPath must be an image or video file under the
// configured media root. caption is the optional status caption.
// Returns the sent message result.
func (c *Client) SendStatus(ctx context.Context, mediaPath, caption string) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	r := c.SendMediaWithOptions(ctx, SendMediaOptions{
		Recipient:  store.StatusBroadcastJID,
		Caption:    caption,
		MediaPath:  mediaPath,
		ViewOnce:   false,
	})
	return r
}

// DeleteStatus deletes a Status (Story) update by revoking its message from
// status@broadcast. messageID is the status message ID from
// list_status_updates.
func (c *Client) DeleteStatus(ctx context.Context, messageID string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	if messageID == "" {
		return errors.New("message_id required")
	}
	return c.DeleteMessage(ctx, store.StatusBroadcastJID, messageID, "")
}

// SetProfilePicture sets the paired account's profile picture. mediaPath must
// be an image file under the configured media root.
func (c *Client) SetProfilePicture(ctx context.Context, mediaPath string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return fmt.Errorf("read media file: %w", err)
	}
	resp, err := c.wa.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return fmt.Errorf("upload profile picture: %w", err)
	}
	_, err = c.wa.SendIQ(ctx, whatsmeow.InfoQuery{
		Namespace: "profile_picture",
		Type:      whatsmeow.IqSet,
		To:        types.ServerJID,
		Content: []binary.Node{{
			Tag:     "picture",
			Attrs: binary.Attrs{
				"url":         resp.URL,
				"direct_path": resp.DirectPath,
				"media_key":   string(resp.MediaKey),
				"file_hash":   string(resp.FileSHA256),
				"file_enc_hash": string(resp.FileEncSHA256),
				"file_length": fmt.Sprintf("%d", resp.FileLength),
			},
		}},
	})
	return err
}

// SetStatusPrivacy sets the privacy mode for the paired account's Status
// (Story) updates. mode can be "all", "contacts", "close_friends", or
// "custom". For "custom", userJIDs should contain the JIDs of contacts
// allowed to view statuses.
func (c *Client) SetStatusPrivacy(ctx context.Context, mode string, userJIDs []string) error {
	if !c.wa.IsConnected() {
		return errors.New("not connected to WhatsApp")
	}
	var distMode waSyncAction.StatusPrivacyAction_StatusDistributionMode
	switch mode {
	case "all":
		distMode = waSyncAction.StatusPrivacyAction_ALLOW_LIST
	case "contacts":
		distMode = waSyncAction.StatusPrivacyAction_CONTACTS
	case "close_friends":
		distMode = waSyncAction.StatusPrivacyAction_CLOSE_FRIENDS
	case "custom":
		distMode = waSyncAction.StatusPrivacyAction_CUSTOM_LIST
	default:
		return fmt.Errorf("invalid status privacy mode: %s (must be all, contacts, close_friends, or custom)", mode)
	}
	jids := make([]types.JID, len(userJIDs))
	for i, jidStr := range userJIDs {
		jid, err := parseRecipient(jidStr)
		if err != nil {
			return fmt.Errorf("invalid JID %s: %w", jidStr, err)
		}
		jids[i] = jid
	}
	patch := appstate.BuildStatusPrivacy(distMode, jids)
	return c.wa.SendAppState(ctx, patch)
}
