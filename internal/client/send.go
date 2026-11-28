package client

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/media"
	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/store"
)

const statusEphemeralSecs uint32 = 86400

func applyStatusEphemeral(msg *waProto.Message) {
	expiration := proto.Uint32(statusEphemeralSecs)
	if msg.VideoMessage != nil {
		msg.VideoMessage.ContextInfo = &waProto.ContextInfo{Expiration: expiration}
	}
	if msg.ImageMessage != nil {
		msg.ImageMessage.ContextInfo = &waProto.ContextInfo{Expiration: expiration}
	}
}

type SendResult struct {
	Success       bool
	Message       string
	ID            string
	Warning       string
	PHash         string
	ExpectedPHash string
}

type SendMediaOptions struct {
	Recipient string
	Caption   string
	MediaPath string
	ViewOnce  bool
}

func (c *Client) Send(ctx context.Context, recipient, message string) SendResult {
	return c.send(ctx, recipient, message, "", false)
}

func (c *Client) SendMediaWithOptions(ctx context.Context, opts SendMediaOptions) SendResult {
	return c.send(ctx, opts.Recipient, opts.Caption, opts.MediaPath, opts.ViewOnce)
}

type outboundPlan struct {
	jid     types.JID
	message *waProto.Message
	preview SendPreview
}

func (c *Client) planOutbound(ctx context.Context, recipient, message, mediaPath string, viewOnce, upload bool) (*outboundPlan, error) {
	recipientJID, err := parseRecipient(recipient)
	if err != nil {
		return nil, err
	}

	isStatus := recipientJID.String() == store.StatusBroadcastJID
	msg := &waProto.Message{
		MessageContextInfo: c.ephemeralContextInfo(ctx, recipientJID),
	}
	if isStatus && msg.MessageContextInfo == nil {
		msg.MessageContextInfo = &waProto.MessageContextInfo{
			MessageAddOnDurationInSecs: proto.Uint32(statusEphemeralSecs),
		}
	}

	preview := SendPreview{
		Recipient:   recipient,
		ResolvedJID: recipientJID.String(),
		ChatKind:    chatKind(recipientJID),
		ViewOnce:    viewOnce,
	}

	if mediaPath != "" {
		asset, err := c.loadMedia(ctx, mediaPath, upload)
		if err != nil {
			return nil, err
		}
		c.fillMediaMessage(msg, asset, message, viewOnce)
		if isStatus {
			applyStatusEphemeral(msg)
			preview.EphemeralSecs = statusEphemeralSecs
		}
		preview.Media = asset.preview()
		preview.MessageKind = mediaKindName(asset.kind)
	} else {
		msg.Conversation = proto.String(message)
		preview.MessageKind = "text"
	}
	preview.Text = message
	preview.TextChars = len(message)

	if isStatus && !upload {
		audience, err := c.statusAudience(ctx)
		if err != nil {
			preview.Warnings = append(preview.Warnings, err.Error())
		} else {
			preview.Audience = audience
		}
	}

	return &outboundPlan{jid: recipientJID, message: msg, preview: preview}, nil
}

func (c *Client) send(ctx context.Context, recipient, message, mediaPath string, viewOnce bool) SendResult {
	if !c.socketConnected() {
		return SendResult{Success: false, Message: "Not connected to WhatsApp"}
	}

	plan, err := c.planOutbound(ctx, recipient, message, mediaPath, viewOnce, true)
	if err != nil {
		return SendResult{Success: false, Message: err.Error()}
	}

	rateWasContact := c.IsKnownContact(plan.jid)
	if err := c.checkSendWindow(ctx, plan.jid); err != nil {
		c.incRateLimited()
		return SendResult{Success: false, Message: err.Error()}
	}

	c.incSends()

	resp, err := c.sendMessage(ctx, plan.jid, plan.message)
	if err != nil {
		c.incErrors()
		if !unknownOutcome(err.Error()) && c.limiter != nil {
			c.limiter.RefundSend(rateWasContact)
		}
		return SendResult{Success: false, Message: fmt.Sprintf("Error sending message: %v", err)}
	}

	c.incMessagesOut()

	if err := c.persistSent(ctx, plan.jid, resp.ID, message, mediaPath, plan.message); err != nil {
		c.log.Errorf("sent message not cached: %v", err)
		result := sendResultFor(recipient, resp)
		if result.Warning != "" {
			result.Warning += "; the local cache write also failed, so list_messages will not show it"
		} else {
			result.Warning = "the local cache write failed, so list_messages will not show this message; edit/delete on it will not work"
		}
		return result
	}

	return sendResultFor(recipient, resp)
}

func unknownOutcome(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "context canceled") ||
		strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "client is closed")
}

func sendResultFor(recipient string, resp whatsmeow.SendResponse) SendResult {
	result := SendResult{
		Success:       true,
		Message:       fmt.Sprintf("Message sent to %s", recipient),
		ID:            resp.ID,
		PHash:         resp.PHash,
		ExpectedPHash: resp.ExpectedPHash,
	}
	if resp.ExpectedPHash != "" && resp.ExpectedPHash != resp.PHash {
		result.Warning = fmt.Sprintf(
			"WhatsApp accepted the send but did not recognize this fan-out: the server expects participant-list hash %s, this client computed %s, so some recipients may not have received it",
			resp.ExpectedPHash, resp.PHash)
	}
	return result
}

func (c *Client) ConvertAudioForSending(ctx context.Context, mediaPath string) (string, error) {
	safePath, err := c.ValidateMediaPath(mediaPath)
	if err != nil {
		return "", fmt.Errorf("media_path rejected: %w", err)
	}
	return media.ConvertToOpusOggIn(ctx, safePath, c.mediaRootDir())
}

func (c *Client) mediaRootDir() string {
	if c.allowedMediaRoot == "" {
		return ""
	}
	if abs, err := filepath.Abs(c.allowedMediaRoot); err == nil {
		return abs
	}
	return c.allowedMediaRoot
}

func chatKind(jid types.JID) string {
	switch jid.Server {
	case types.GroupServer:
		return "group"
	case types.BroadcastServer:
		return "broadcast"
	case types.NewsletterServer:
		return "newsletter"
	case types.BotServer:
		return "bot"
	default:
		return "contact"
	}
}

func mediaKindName(kind whatsmeow.MediaType) string {
	switch kind {
	case whatsmeow.MediaImage:
		return "image"
	case whatsmeow.MediaAudio:
		return "audio"
	case whatsmeow.MediaVideo:
		return "video"
	default:
		return "document"
	}
}

func (c *Client) ephemeralContextInfo(ctx context.Context, chat types.JID) *waProto.MessageContextInfo {
	if chat.Server != "g.us" {
		return nil
	}
	gi, err := c.wa.GetGroupInfo(ctx, chat)
	if err != nil {
		c.log.Warnf("Read disappearing timer for %s: %v", chat, err)
		return nil
	}
	if gi.DisappearingTimer == 0 {
		return nil
	}
	c.log.Infof("Auto-detected group disappearing timer: %d seconds", gi.DisappearingTimer)
	return &waProto.MessageContextInfo{
		MessageAddOnDurationInSecs: proto.Uint32(gi.DisappearingTimer),
	}
}

func (c *Client) IsKnownContact(jid types.JID) bool {
	if jid.Server == types.GroupServer {
		return true
	}
	known, err := c.store.HasInboundFrom(jid.String())
	if err != nil {
		c.log.Warnf("contact lookup failed for %s, treating as non-contact: %v", c.redactor.JID(jid.String()), err)
		return false
	}
	return known
}

func parseChatJID(chatJID string) (types.JID, error) {
	if !strings.Contains(chatJID, "@") {
		normalized, err := NormalizeRecipient(chatJID)
		if err != nil {
			return types.JID{}, err
		}
		if !strings.Contains(normalized, "@") {
			return types.JID{User: normalized, Server: types.DefaultUserServer}, nil
		}
		chatJID = normalized
	}
	return types.ParseJID(chatJID)
}

func parseRecipient(recipient string) (types.JID, error) {
	canonical, err := NormalizeRecipient(recipient)
	if err != nil {
		return types.JID{}, err
	}
	if strings.Contains(canonical, "@") {
		jid, err := types.ParseJID(canonical)
		if err != nil {
			return types.JID{}, fmt.Errorf("Error parsing JID: %v", err)
		}
		return jid, nil
	}
	return types.JID{User: canonical, Server: types.DefaultUserServer}, nil
}

type mediaAsset struct {
	path        string
	data        []byte
	kind        whatsmeow.MediaType
	mime        string
	video       media.VideoInfo
	thumb       []byte
	seconds     uint32
	waveform    []byte
	upload      *whatsmeow.UploadResponse
	thumbUpload *whatsmeow.UploadResponse
}

func (a *mediaAsset) preview() *MediaPreview {
	p := &MediaPreview{
		Path:         a.path,
		Bytes:        len(a.data),
		Kind:         mediaKindName(a.kind),
		MimeType:     a.mime,
		Width:        a.video.Width,
		Height:       a.video.Height,
		Seconds:      a.video.Duration,
		HasThumbnail: len(a.thumb) > 0,
	}
	if a.seconds > 0 && p.Seconds == 0 {
		p.Seconds = a.seconds
	}
	return p
}

func (c *Client) loadMedia(ctx context.Context, mediaPath string, upload bool) (*mediaAsset, error) {
	safePath, err := c.ValidateMediaPath(mediaPath)
	if err != nil {
		return nil, fmt.Errorf("media_path rejected: %w", err)
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("read media file: %w", err)
	}

	asset := &mediaAsset{path: safePath, data: data}
	asset.kind, asset.mime = mediaTypeFromExt(safePath)
	if asset.kind == whatsmeow.MediaVideo {
		info, thumb, _ := media.AnalyzeVideo(ctx, safePath)
		asset.video = info
		asset.thumb = thumb
	}
	if asset.kind == whatsmeow.MediaAudio && strings.Contains(asset.mime, "ogg") {
		seconds, waveform, aerr := media.AnalyzeOggOpus(data)
		if aerr != nil {
			return nil, fmt.Errorf("Failed to analyze Ogg Opus file: %v", aerr)
		}
		asset.seconds = seconds
		asset.waveform = waveform
	} else if asset.kind == whatsmeow.MediaAudio {
		c.log.Warnf("Not an Ogg Opus file: %s", asset.mime)
	}

	if !upload {
		return asset, nil
	}

	resp, err := c.uploadMedia(ctx, data, asset.kind)
	if err != nil {
		return nil, fmt.Errorf("Error uploading media: %v", err)
	}
	asset.upload = &resp
	c.log.Infof("Media uploaded: url=%s bytes=%d", c.redactor.URL(resp.URL), resp.FileLength)

	if len(asset.thumb) > 0 {
		thumbResp, thumbErr := c.uploadMedia(ctx, asset.thumb, whatsmeow.MediaImage)
		if thumbErr == nil {
			asset.thumbUpload = &thumbResp
		}
	}
	return asset, nil
}

func (c *Client) fillMediaMessage(msg *waProto.Message, asset *mediaAsset, caption string, viewOnce bool) {
	resp := asset.upload
	if resp == nil {
		resp = &whatsmeow.UploadResponse{}
	}
	switch asset.kind {
	case whatsmeow.MediaImage:
		msg.ImageMessage = &waProto.ImageMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(asset.mime),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
		if viewOnce {
			msg.ImageMessage.ViewOnce = proto.Bool(true)
		}
	case whatsmeow.MediaAudio:
		seconds := asset.seconds
		if seconds == 0 {
			seconds = 30
		}
		msg.AudioMessage = &waProto.AudioMessage{
			Mimetype:      proto.String(asset.mime),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
			Seconds:       proto.Uint32(seconds),
			PTT:           proto.Bool(true),
			Waveform:      asset.waveform,
		}
		if viewOnce {
			msg.AudioMessage.ViewOnce = proto.Bool(true)
		}
	case whatsmeow.MediaVideo:
		vmsg := &waProto.VideoMessage{
			Caption:       proto.String(caption),
			Mimetype:      proto.String(asset.mime),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
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
			if asset.thumbUpload != nil {
				vmsg.ThumbnailDirectPath = proto.String(asset.thumbUpload.DirectPath)
				vmsg.ThumbnailSHA256 = asset.thumbUpload.FileSHA256
				vmsg.ThumbnailEncSHA256 = asset.thumbUpload.FileEncSHA256
			}
		}
		msg.VideoMessage = vmsg
		if viewOnce {
			msg.VideoMessage.ViewOnce = proto.Bool(true)
		}
	case whatsmeow.MediaDocument:
		msg.DocumentMessage = &waProto.DocumentMessage{
			Title:         proto.String(filepath.Base(asset.path)),
			Caption:       proto.String(caption),
			Mimetype:      proto.String(asset.mime),
			URL:           &resp.URL,
			DirectPath:    &resp.DirectPath,
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &resp.FileLength,
		}
	}
}

func mediaTypeFromExt(path string) (whatsmeow.MediaType, string) {
	ext := ""
	if idx := strings.LastIndex(path, "."); idx >= 0 {
		ext = strings.ToLower(path[idx+1:])
	}
	switch ext {
	case "jpg", "jpeg":
		return whatsmeow.MediaImage, "image/jpeg"
	case "png":
		return whatsmeow.MediaImage, "image/png"
	case "gif":
		return whatsmeow.MediaImage, "image/gif"
	case "webp":
		return whatsmeow.MediaImage, "image/webp"
	case "ogg":
		return whatsmeow.MediaAudio, "audio/ogg; codecs=opus"
	case "mp4":
		return whatsmeow.MediaVideo, "video/mp4"
	case "avi":
		return whatsmeow.MediaVideo, "video/avi"
	case "mov":
		return whatsmeow.MediaVideo, "video/quicktime"
	default:
		return whatsmeow.MediaDocument, documentMimeType(ext)
	}
}

var documentMimes = map[string]string{
	"pdf":  "application/pdf",
	"doc":  "application/msword",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xls":  "application/vnd.ms-excel",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"ppt":  "application/vnd.ms-powerpoint",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"odt":  "application/vnd.oasis.opendocument.text",
	"ods":  "application/vnd.oasis.opendocument.spreadsheet",
	"odp":  "application/vnd.oasis.opendocument.presentation",
	"txt":  "text/plain",
	"csv":  "text/csv",
	"json": "application/json",
	"rtf":  "application/rtf",
	"zip":  "application/zip",
	"mp3":  "audio/mpeg",
	"m4a":  "audio/mp4",
	"apk":  "application/vnd.android.package-archive",
}

func documentMimeType(ext string) string {
	if m, ok := documentMimes[ext]; ok {
		return m
	}
	if ext != "" {
		if m := mime.TypeByExtension("." + ext); m != "" {
			return m
		}
	}
	return "application/octet-stream"
}

func (c *Client) persistSent(ctx context.Context, recipientJID types.JID, msgID, message, mediaPath string, msg *waProto.Message) error {
	chatJID := recipientJID.String()
	if !strings.HasSuffix(chatJID, "@g.us") {
		chatJID = c.store.ResolveLIDToJID(chatJID)
	}

	chatName := c.resolveChatNameFallback(ctx, recipientJID, "")

	now := time.Now()
	if err := c.store.StoreChat(chatJID, chatName, now); err != nil {
		c.log.Warnf("Failed to store chat for sent message: %v", err)
	}

	ourJID := ""
	if c.wa != nil && c.wa.Store != nil && c.wa.Store.ID != nil {
		ourJID = c.wa.Store.ID.User
	}

	var (
		storedMediaType, storedFilename, storedURL, storedDirectPath string
		storedMediaKey, storedFileSHA256, storedFileEnc              []byte
		storedFileLength                                             uint64
	)
	if mediaPath != "" {
		storedMediaType, storedFilename, storedURL, storedDirectPath,
			storedMediaKey, storedFileSHA256, storedFileEnc, storedFileLength = extractMediaInfo(msg)
	}

	err := c.store.StoreMessage(ctx, store.Message{
		ID:         msgID,
		ChatJID:    chatJID,
		Sender:     ourJID,
		Content:    message,
		Timestamp:  now,
		IsFromMe:   true,
		MediaType:  storedMediaType,
		Filename:   storedFilename,
		URL:        storedURL,
		DirectPath: storedDirectPath,
	}, storedMediaKey, storedFileSHA256, storedFileEnc, storedFileLength)
	if err != nil {
		return fmt.Errorf("cache sent message: %w", err)
	}
	c.log.Infof("[%s] -> %s [%s]: %s", now.Format("2006-01-02 15:04:05"), c.redactor.JID(chatJID), c.redactor.MsgID(msgID), c.redactor.Body(message))
	return nil
}
