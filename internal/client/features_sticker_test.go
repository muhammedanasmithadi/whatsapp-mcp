package client

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func riffWebP(chunk string, payload []byte) []byte {
	out := []byte("RIFF")
	size := make([]byte, 4)
	binary.LittleEndian.PutUint32(size, uint32(4+8+len(payload)))
	out = append(out, size...)
	out = append(out, "WEBP"...)
	out = append(out, chunk...)
	length := make([]byte, 4)
	binary.LittleEndian.PutUint32(length, uint32(len(payload)))
	out = append(out, length...)
	return append(out, payload...)
}

func lossyPayload(width, height int) []byte {
	payload := []byte{0x00, 0x00, 0x00, 0x9d, 0x01, 0x2a, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(payload[6:8], uint16(width))
	binary.LittleEndian.PutUint16(payload[8:10], uint16(height))
	return payload
}

func losslessPayload(width, height int) []byte {
	bits := uint32(width-1) | uint32(height-1)<<14
	payload := []byte{0x2f, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(payload[1:5], bits)
	return payload
}

func extendedPayload(width, height int, animated bool) []byte {
	payload := make([]byte, 10)
	if animated {
		payload[0] = 0x40
	}
	payload[4] = byte(width - 1)
	payload[5] = byte((width - 1) >> 8)
	payload[6] = byte((width - 1) >> 16)
	payload[7] = byte(height - 1)
	payload[8] = byte((height - 1) >> 8)
	payload[9] = byte((height - 1) >> 16)
	return payload
}

func TestParseWebP_Lossy(t *testing.T) {
	info, err := parseWebP(riffWebP("VP8 ", lossyPayload(513, 512)))
	if err != nil {
		t.Fatalf("parseWebP(lossy) error: %v", err)
	}
	if info.width != 513 || info.height != 512 {
		t.Fatalf("lossy dimensions = %dx%d, want 513x512", info.width, info.height)
	}
	if info.animated {
		t.Fatalf("lossy single-frame image reported as animated")
	}
}

func TestParseWebP_Lossless(t *testing.T) {
	info, err := parseWebP(riffWebP("VP8L", losslessPayload(513, 512)))
	if err != nil {
		t.Fatalf("parseWebP(lossless) error: %v", err)
	}
	if info.width != 513 || info.height != 512 {
		t.Fatalf("lossless dimensions = %dx%d, want 513x512", info.width, info.height)
	}
}

func TestParseWebP_ExtendedStatic(t *testing.T) {
	info, err := parseWebP(riffWebP("VP8X", extendedPayload(513, 512, false)))
	if err != nil {
		t.Fatalf("parseWebP(extended) error: %v", err)
	}
	if info.width != 513 || info.height != 512 {
		t.Fatalf("extended dimensions = %dx%d, want 513x512", info.width, info.height)
	}
	if info.animated {
		t.Fatalf("extended image without the animation flag reported as animated")
	}
}

func TestParseWebP_ExtendedAnimated(t *testing.T) {
	info, err := parseWebP(riffWebP("VP8X", extendedPayload(513, 512, true)))
	if err != nil {
		t.Fatalf("parseWebP(animated) error: %v", err)
	}
	if !info.animated {
		t.Fatalf("extended image with the animation flag not reported as animated")
	}
}

func TestParseWebP_RejectsNonWebP(t *testing.T) {
	cases := map[string][]byte{
		"png":       {'\x89', 'P', 'N', 'G', '\r', '\n', '\x1a', '\n', 0, 0, 0, 0, 0, 0, 0, 0},
		"jpeg":      {0xff, 0xd8, 0xff, 0xe0, 0, 0, 'J', 'F', 'I', 'F', 0, 0, 0, 0, 0, 0},
		"empty":     {},
		"too small": {'R', 'I', 'F', 'F'},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseWebP(data); err == nil {
				t.Fatalf("parseWebP(%s) accepted a non-WebP payload", name)
			}
		})
	}
}

func TestParseWebP_RejectsBadChunks(t *testing.T) {
	broken := []byte("RIFF")
	broken = append(broken, []byte{0, 0, 0, 0}...)
	broken = append(broken, "WEBP"...)
	broken = append(broken, "VP8 "...)
	broken = append(broken, []byte{0, 0, 0, 0}...)
	if _, err := parseWebP(broken); err == nil {
		t.Fatalf("parseWebP accepted a truncated VP8 chunk")
	}

	lossy := riffWebP("VP8 ", lossyPayload(100, 100))
	lossy[23] = 0x00
	if _, err := parseWebP(lossy); err == nil {
		t.Fatalf("parseWebP accepted a VP8 chunk without the start code")
	}

	if _, err := parseWebP(riffWebP("GIF8", []byte{0, 0, 0, 0})); err == nil {
		t.Fatalf("parseWebP accepted an unsupported chunk")
	}
}

func TestPayloadIsWebP_AcceptsRealisticSticker(t *testing.T) {
	info, err := parseWebP(riffWebP("VP8 ", lossyPayload(512, 512)))
	if err != nil {
		t.Fatalf("parseWebP(sticker) error: %v", err)
	}
	if info.width != 512 || info.height != 512 {
		t.Fatalf("sticker dimensions = %dx%d, want 512x512", info.width, info.height)
	}
}

func TestLoadStickerAsset_RejectsNonWebP(t *testing.T) {
	root := t.TempDir()
	png := filepath.Join(root, "notasticker.png")
	if err := os.WriteFile(png, []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0, 0, 0, 0, 0}, 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	c := &Client{allowedMediaRoot: root}
	_, err := c.loadStickerAsset(png)
	if err == nil {
		t.Fatalf("loadStickerAsset accepted a PNG")
	}
	if !strings.Contains(err.Error(), "WebP") {
		t.Fatalf("error %q does not name the expected type", err.Error())
	}
}

func TestLoadStickerAsset_RejectsOutsideMediaRoot(t *testing.T) {
	c := &Client{allowedMediaRoot: t.TempDir()}
	if _, err := c.loadStickerAsset(filepath.Join(t.TempDir(), "sticker.webp")); err == nil {
		t.Fatalf("loadStickerAsset accepted a path outside the media root")
	}
}

func TestLoadStickerAsset_RejectsEmptyPath(t *testing.T) {
	c := &Client{allowedMediaRoot: t.TempDir()}
	if _, err := c.loadStickerAsset(""); err == nil {
		t.Fatalf("loadStickerAsset accepted an empty path")
	}
}

func TestLoadStickerAsset_AcceptsWebP(t *testing.T) {
	root := t.TempDir()
	webp := filepath.Join(root, "sticker.webp")
	if err := os.WriteFile(webp, riffWebP("VP8 ", lossyPayload(512, 512)), 0o600); err != nil {
		t.Fatalf("write webp: %v", err)
	}
	c := &Client{allowedMediaRoot: root}
	asset, err := c.loadStickerAsset(webp)
	if err != nil {
		t.Fatalf("loadStickerAsset error: %v", err)
	}
	if asset.info.width != 512 || asset.info.height != 512 {
		t.Fatalf("asset dimensions = %dx%d, want 512x512", asset.info.width, asset.info.height)
	}
	if len(asset.data) == 0 {
		t.Fatalf("asset holds no bytes")
	}
}

func TestSendSticker_NotConnected(t *testing.T) {
	c := newDisconnectedClient()
	r := c.SendSticker(t.Context(), "447700000001", "sticker.webp")
	if r.Success {
		t.Fatalf("expected Success=false, got %+v", r)
	}
	if r.Message != "Not connected to WhatsApp" {
		t.Fatalf("expected not-connected message, got %q", r.Message)
	}
}

func TestGetStickerPack_EmptyPackID(t *testing.T) {
	c := newDisconnectedClient()
	if _, err := c.GetStickerPack(t.Context(), "   "); err == nil {
		t.Fatalf("expected an error for a whitespace pack_id, got nil")
	}
}

func TestGetStickerPack_NilClient(t *testing.T) {
	c := &Client{}
	if _, err := c.GetStickerPack(t.Context(), "pack-1"); err == nil {
		t.Fatalf("expected an error for an uninitialised client, got nil")
	}
}

func TestNewStickerPackResult(t *testing.T) {
	pack := &types.StickerPack{
		StickerPackID: "pack-1",
		Name:          "Grumpy Cats",
		Publisher:     "Cat Studio",
		Stickers: []*types.StickerPackItem{
			{
				MimeType:          "image/webp",
				URL:               "https://example.invalid/1.webp",
				DirectPath:        "/mms/1",
				Width:             512,
				Height:            512,
				FileSize:          12345,
				Emojis:            []string{"🐱", "😾"},
				AccessibilityText: "A grumpy cat",
			},
			{
				MimeType:   "image/webp",
				URL:        "https://example.invalid/2.webp",
				DirectPath: "/mms/2",
				Width:      384,
				Height:     384,
				FileSize:   999,
			},
		},
	}

	got := newStickerPackResult(pack)
	if got.PackID != "pack-1" || got.Name != "Grumpy Cats" || got.Publisher != "Cat Studio" {
		t.Fatalf("pack metadata = %+v", got)
	}
	if len(got.Stickers) != 2 {
		t.Fatalf("mapped %d stickers, want 2", len(got.Stickers))
	}
	first := got.Stickers[0]
	if first.MimeType != "image/webp" || first.URL != "https://example.invalid/1.webp" || first.DirectPath != "/mms/1" {
		t.Fatalf("first sticker identity fields wrong: %+v", first)
	}
	if first.Width != 512 || first.Height != 512 || first.FileSize != 12345 {
		t.Fatalf("first sticker shape fields wrong: %+v", first)
	}
	if first.AccessibilityText != "A grumpy cat" {
		t.Fatalf("first sticker accessibility text = %q", first.AccessibilityText)
	}
	if len(first.Emojis) != 2 || first.Emojis[0] != "🐱" {
		t.Fatalf("first sticker emojis = %v", first.Emojis)
	}
	if second := got.Stickers[1]; len(second.Emojis) != 0 {
		t.Fatalf("sticker without emojis mapped to %v", second.Emojis)
	}
}

func TestNewStickerPackResult_NilAndEmpty(t *testing.T) {
	if got := newStickerPackResult(nil); got == nil || got.Stickers == nil {
		t.Fatalf("nil pack must map to an empty result with a non-nil sticker list, got %+v", got)
	}
	empty := newStickerPackResult(&types.StickerPack{StickerPackID: "pack-2"})
	if len(empty.Stickers) != 0 {
		t.Fatalf("empty pack mapped to %d stickers", len(empty.Stickers))
	}
}

func TestNewStickerPackResult_DoesNotAliasEmojis(t *testing.T) {
	emojis := []string{"🔥"}
	pack := &types.StickerPack{Stickers: []*types.StickerPackItem{{Emojis: emojis}}}
	got := newStickerPackResult(pack)
	got.Stickers[0].Emojis[0] = "mutated"
	if emojis[0] != "🔥" {
		t.Fatalf("mapping aliased the source emoji slice: %v", emojis)
	}
}
