package client

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

type MediaPreview struct {
	Path         string `json:"path"`
	Bytes        int    `json:"bytes"`
	Kind         string `json:"kind"`
	MimeType     string `json:"mime_type"`
	Width        uint32 `json:"width,omitempty"`
	Height       uint32 `json:"height,omitempty"`
	Seconds      uint32 `json:"seconds,omitempty"`
	HasThumbnail bool   `json:"has_thumbnail,omitempty"`
}

type StatusAudience struct {
	PrivacyType     string `json:"privacy_type"`
	Recipients      int    `json:"recipients"`
	Devices         int    `json:"devices"`
	ParticipantHash string `json:"participant_hash"`
}

type SendPreview struct {
	Recipient     string          `json:"recipient"`
	ResolvedJID   string          `json:"resolved_jid"`
	ChatKind      string          `json:"chat_kind"`
	MessageKind   string          `json:"message_kind"`
	Text          string          `json:"text,omitempty"`
	TextChars     int             `json:"text_chars,omitempty"`
	Media         *MediaPreview   `json:"media,omitempty"`
	ViewOnce      bool            `json:"view_once,omitempty"`
	EphemeralSecs uint32          `json:"ephemeral_seconds,omitempty"`
	Audience      *StatusAudience `json:"audience,omitempty"`
	Warnings      []string        `json:"warnings,omitempty"`
}

type SendPreviewOptions struct {
	Recipient string
	Message   string
	MediaPath string
	ViewOnce  bool
}

func (c *Client) PreviewSend(ctx context.Context, opts SendPreviewOptions) (*SendPreview, error) {
	if !c.socketConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	plan, err := c.planOutbound(ctx, opts.Recipient, opts.Message, opts.MediaPath, opts.ViewOnce, false)
	if err != nil {
		return nil, err
	}
	return &plan.preview, nil
}

func (c *Client) statusAudience(ctx context.Context) (*StatusAudience, error) {
	if c.wa == nil || !c.socketConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	privacy, err := c.wa.GetStatusPrivacy(ctx)
	if err != nil {
		return nil, fmt.Errorf("get status privacy: %w", err)
	}
	recipients := []types.JID{}
	privacyType := ""
	if len(privacy) > 0 {
		privacyType = string(privacy[0].Type)
		if privacy[0].Type == types.StatusPrivacyTypeWhitelist && len(privacy[0].List) > 0 {
			recipients = privacy[0].List
		}
	}
	if len(recipients) == 0 {
		contacts, err := c.wa.Store.Contacts.GetAllContacts(ctx)
		if err != nil {
			return nil, fmt.Errorf("list contacts: %w", err)
		}
		for jid, info := range contacts {
			if len(info.FullName) > 0 {
				recipients = append(recipients, jid)
			}
		}
	}
	own := c.wa.Store.ID.ToNonAD()
	recipients = appendUnique(recipients, own)

	devices, err := c.wa.GetUserDevices(ctx, recipients)
	if err != nil {
		return nil, fmt.Errorf("resolve devices: %w", err)
	}
	return &StatusAudience{
		PrivacyType:     privacyType,
		Recipients:      len(recipients),
		Devices:         len(devices),
		ParticipantHash: participantListHash(devices),
	}, nil
}

func appendUnique(jids []types.JID, extra types.JID) []types.JID {
	for _, jid := range jids {
		if jid == extra {
			return jids
		}
	}
	return append(jids, extra)
}

// participantListHash reproduces whatsmeow's unexported participant-list hash:
func participantListHash(participants []types.JID) string {
	strs := make([]string, len(participants))
	for i, p := range participants {
		strs[i] = p.ADString()
	}
	sort.Strings(strs)
	sum := sha256.Sum256([]byte(strings.Join(strs, "")))
	return fmt.Sprintf("2:%s", base64.RawStdEncoding.EncodeToString(sum[:6]))
}
