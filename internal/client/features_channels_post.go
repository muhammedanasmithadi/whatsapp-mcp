package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
)

type ChannelPostResult struct {
	ChannelJID string `json:"channel_jid"`
	MessageID  string `json:"message_id"`
	ServerID   int64  `json:"server_id,omitempty"`
	Kind       string `json:"kind"`
	MediaType  string `json:"media_type,omitempty"`
	MediaBytes int    `json:"media_bytes,omitempty"`
}

func (c *Client) SendChannelMessage(ctx context.Context, input, message, mediaPath string) (*ChannelPostResult, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	message = strings.TrimSpace(message)
	mediaPath = strings.TrimSpace(mediaPath)
	if err := validateChannelPost(message, mediaPath); err != nil {
		return nil, err
	}

	jid, _, err := c.resolveChannelJID(ctx, input)
	if err != nil {
		return nil, err
	}
	if err := c.checkSendRate(ctx, jid); err != nil {
		return nil, err
	}

	result := &ChannelPostResult{ChannelJID: jid.String(), Kind: "text"}
	msg := &waProto.Message{}
	mediaHandle := ""
	if mediaPath == "" {
		msg.Conversation = proto.String(message)
	} else {
		asset, err := c.loadMedia(ctx, mediaPath, false)
		if err != nil {
			return nil, err
		}
		upload, err := c.wa.UploadNewsletter(ctx, asset.data, asset.kind)
		if err != nil {
			return nil, fmt.Errorf("upload channel media: %w", err)
		}
		if upload.Handle == "" {
			return nil, errors.New("WhatsApp returned no media handle for the channel upload")
		}
		msg = buildChannelMediaMessage(asset, &upload, message)
		mediaHandle = upload.Handle
		result.Kind = mediaKindName(asset.kind)
		result.MediaType = asset.mime
		result.MediaBytes = len(asset.data)
	}

	resp, err := c.wa.SendMessage(ctx, jid, msg, whatsmeow.SendRequestExtra{MediaHandle: mediaHandle})
	if err != nil {
		return nil, fmt.Errorf("post to channel: %w", err)
	}
	result.MessageID = resp.ID
	result.ServerID = int64(resp.ServerID)
	return result, nil
}

func validateChannelPost(message, mediaPath string) error {
	if strings.TrimSpace(mediaPath) == "" && strings.TrimSpace(message) == "" {
		return errors.New("message required when no media_path is given")
	}
	return nil
}

func buildChannelMediaMessage(asset *mediaAsset, upload *whatsmeow.UploadResponse, caption string) *waProto.Message {
	msg := &waProto.Message{}
	switch asset.kind {
	case whatsmeow.MediaImage:
		msg.ImageMessage = &waProto.ImageMessage{
			Caption:    proto.String(caption),
			Mimetype:   proto.String(asset.mime),
			URL:        proto.String(upload.URL),
			DirectPath: proto.String(upload.DirectPath),
			FileSHA256: upload.FileSHA256,
			FileLength: proto.Uint64(upload.FileLength),
		}
	case whatsmeow.MediaAudio:
		seconds := asset.seconds
		if seconds == 0 {
			seconds = 30
		}
		msg.AudioMessage = &waProto.AudioMessage{
			Mimetype:   proto.String(asset.mime),
			URL:        proto.String(upload.URL),
			DirectPath: proto.String(upload.DirectPath),
			FileSHA256: upload.FileSHA256,
			FileLength: proto.Uint64(upload.FileLength),
			Seconds:    proto.Uint32(seconds),
			Waveform:   asset.waveform,
		}
	case whatsmeow.MediaVideo:
		vmsg := &waProto.VideoMessage{
			Caption:    proto.String(caption),
			Mimetype:   proto.String(asset.mime),
			URL:        proto.String(upload.URL),
			DirectPath: proto.String(upload.DirectPath),
			FileSHA256: upload.FileSHA256,
			FileLength: proto.Uint64(upload.FileLength),
		}
		if asset.video.Duration > 0 {
			vmsg.Seconds = proto.Uint32(asset.video.Duration)
		}
		if asset.video.Height > 0 {
			vmsg.Height = proto.Uint32(asset.video.Height)
		}
		if asset.video.Width > 0 {
			vmsg.Width = proto.Uint32(asset.video.Width)
		}
		if len(asset.thumb) > 0 {
			vmsg.JPEGThumbnail = asset.thumb
		}
		msg.VideoMessage = vmsg
	case whatsmeow.MediaDocument:
		msg.DocumentMessage = &waProto.DocumentMessage{
			Title:      proto.String(filepath.Base(asset.path)),
			Caption:    proto.String(caption),
			Mimetype:   proto.String(asset.mime),
			URL:        proto.String(upload.URL),
			DirectPath: proto.String(upload.DirectPath),
			FileSHA256: upload.FileSHA256,
			FileLength: proto.Uint64(upload.FileLength),
		}
	}
	return msg
}

func (c *Client) CreateChannel(ctx context.Context, name, description, picturePath string) (*ChannelInfo, error) {
	if !c.wa.IsConnected() {
		return nil, errors.New("not connected to WhatsApp")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("channel name is required")
	}
	params := whatsmeow.CreateNewsletterParams{
		Name:        name,
		Description: strings.TrimSpace(description),
	}
	if picturePath != "" {
		safePath, err := c.ValidateMediaPath(picturePath)
		if err != nil {
			return nil, fmt.Errorf("picture_path rejected: %w", err)
		}
		data, err := os.ReadFile(safePath)
		if err != nil {
			return nil, fmt.Errorf("read picture file: %w", err)
		}
		params.Picture = data
	}

	info, err := c.wa.CreateNewsletter(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create channel: %w", err)
	}
	if info == nil {
		return nil, errors.New("WhatsApp returned no channel metadata")
	}
	return info, nil
}
