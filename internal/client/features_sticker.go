package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

const stickerMimeType = "image/webp"

type webpInfo struct {
	width    uint32
	height   uint32
	animated bool
}

func le24(data []byte, off int) uint32 {
	return uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16
}

func parseWebP(data []byte) (*webpInfo, error) {
	if len(data) < 12 {
		return nil, errors.New("file is too small to be a WebP image")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, errors.New("not a RIFF/WEBP container")
	}
	switch chunk := string(data[12:16]); chunk {
	case "VP8X":
		if len(data) < 30 {
			return nil, errors.New("truncated VP8X chunk")
		}
		return &webpInfo{
			width:    le24(data, 24) + 1,
			height:   le24(data, 27) + 1,
			animated: data[20]&0x40 != 0,
		}, nil
	case "VP8 ":
		if len(data) < 30 {
			return nil, errors.New("truncated VP8 chunk")
		}
		if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return nil, errors.New("missing VP8 lossy start code")
		}
		return &webpInfo{
			width:  uint32(binary.LittleEndian.Uint16(data[26:28])) & 0x3fff,
			height: uint32(binary.LittleEndian.Uint16(data[28:30])) & 0x3fff,
		}, nil
	case "VP8L":
		if len(data) < 25 {
			return nil, errors.New("truncated VP8L chunk")
		}
		if data[20] != 0x2f {
			return nil, errors.New("missing VP8L signature byte")
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		return &webpInfo{
			width:  (bits & 0x3fff) + 1,
			height: ((bits >> 14) & 0x3fff) + 1,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported WebP chunk %q", chunk)
	}
}

type stickerAsset struct {
	path string
	data []byte
	info *webpInfo
}

func (c *Client) loadStickerAsset(mediaPath string) (*stickerAsset, error) {
	safePath, err := c.ValidateMediaPath(mediaPath)
	if err != nil {
		return nil, fmt.Errorf("media_path rejected: %w", err)
	}
	if safePath == "" {
		return nil, errors.New("media_path is required")
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("read sticker file: %w", err)
	}
	info, err := parseWebP(data)
	if err != nil {
		return nil, fmt.Errorf("media_path must be a WebP image: %w", err)
	}
	return &stickerAsset{path: safePath, data: data, info: info}, nil
}

func (c *Client) SendSticker(ctx context.Context, recipient, mediaPath string) SendResult {
	if !c.wa.IsConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}
	asset, err := c.loadStickerAsset(mediaPath)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	if err := c.checkSendRate(ctx, recipientJID); err != nil {
		if c.metricSink != nil {
			c.metricSink.IncRateLimited()
		}
		return SendResult{Success: false, Message: err.Error()}
	}

	resp, err := c.wa.Upload(ctx, asset.data, whatsmeow.MediaImage)
	if err != nil {
		if c.metricSink != nil {
			c.metricSink.IncErrors()
		}
		return SendResult{Success: false, Message: fmt.Sprintf("Error uploading sticker: %v", err)}
	}
	if c.metricSink != nil {
		c.metricSink.IncSends()
	}

	msg := &waE2E.Message{
		MessageContextInfo: c.ephemeralContextInfo(ctx, recipientJID),
		StickerMessage: &waE2E.StickerMessage{
			Mimetype:      proto.String(stickerMimeType),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
			IsAnimated:    proto.Bool(asset.info.animated),
			StickerSentTS: proto.Int64(time.Now().UnixMilli()),
		},
	}
	if asset.info.width > 0 {
		msg.StickerMessage.Width = proto.Uint32(asset.info.width)
	}
	if asset.info.height > 0 {
		msg.StickerMessage.Height = proto.Uint32(asset.info.height)
	}

	sendResp, err := c.wa.SendMessage(ctx, recipientJID, msg)
	if err != nil {
		if c.metricSink != nil {
			c.metricSink.IncErrors()
		}
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending sticker: %v", err)}
	}
	if c.metricSink != nil {
		c.metricSink.IncMessagesOut()
	}

	if err := c.persistSent(ctx, recipientJID, sendResp.ID, "[sticker]", asset.path, msg); err != nil {
		c.log.Errorf("sent sticker not cached: %v", err)
		result := sendResultFor(recipient, sendResp)
		result.Warning = "the local cache write failed, so list_messages will not show this sticker"
		return result
	}

	return sendResultFor(recipient, sendResp)
}

type StickerPackResult struct {
	PackID    string             `json:"pack_id"`
	Name      string             `json:"name"`
	Publisher string             `json:"publisher"`
	Stickers  []StickerPackEntry `json:"stickers"`
}

type StickerPackEntry struct {
	MimeType          string   `json:"mimetype"`
	URL               string   `json:"url"`
	DirectPath        string   `json:"direct_path"`
	Width             int      `json:"width"`
	Height            int      `json:"height"`
	FileSize          int64    `json:"file_size"`
	Emojis            []string `json:"emojis,omitempty"`
	AccessibilityText string   `json:"accessibility_text,omitempty"`
}

func (c *Client) GetStickerPack(ctx context.Context, packID string) (*StickerPackResult, error) {
	if strings.TrimSpace(packID) == "" {
		return nil, errors.New("pack_id is required")
	}
	if c.wa == nil {
		return nil, errors.New("whatsapp client not initialised")
	}
	pack, err := c.wa.FetchStickerPack(ctx, packID)
	if err != nil {
		return nil, fmt.Errorf("fetch sticker pack %s: %w", packID, err)
	}
	result := newStickerPackResult(pack)
	if result.PackID == "" {
		result.PackID = packID
	}
	return result, nil
}

func newStickerPackResult(pack *types.StickerPack) *StickerPackResult {
	result := &StickerPackResult{Stickers: []StickerPackEntry{}}
	if pack == nil {
		return result
	}
	result.PackID = pack.StickerPackID
	result.Name = pack.Name
	result.Publisher = pack.Publisher
	for _, item := range pack.Stickers {
		result.Stickers = append(result.Stickers, newStickerPackEntry(item))
	}
	return result
}

func newStickerPackEntry(item *types.StickerPackItem) StickerPackEntry {
	entry := StickerPackEntry{
		MimeType:          item.MimeType,
		URL:               item.URL,
		DirectPath:        item.DirectPath,
		Width:             item.Width,
		Height:            item.Height,
		FileSize:          item.FileSize,
		AccessibilityText: item.AccessibilityText,
	}
	if len(item.Emojis) > 0 {
		entry.Emojis = append([]string(nil), item.Emojis...)
	}
	return entry
}
