package mcp

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

var jpegMagic = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}

var oggMagic = []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00")

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("empty content")
	}
	txt, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content[0] should be TextContent, got %T", res.Content[0])
	}
	return txt.Text
}

func assertJSONSummary(t *testing.T, res *mcp.CallToolResult, want client.DownloadResult) {
	t.Helper()
	var got client.DownloadResult
	if err := json.Unmarshal([]byte(firstText(t, res)), &got); err != nil {
		t.Fatalf("summary is not JSON: %v (raw: %q)", err, firstText(t, res))
	}
	if got != want {
		t.Errorf("summary = %+v, want %+v", got, want)
	}
}

func TestDownloadMediaResult_ImageInlined(t *testing.T) {
	path := writeFile(t, "img.jpg", jpegMagic)
	r := client.DownloadResult{
		Success:   true,
		Message:   "Successfully downloaded image media",
		MediaType: "image",
		Filename:  "img.jpg",
		Path:      path,
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(res.Content))
	}
	assertJSONSummary(t, res, r)

	img, ok := res.Content[1].(mcp.ImageContent)
	if !ok {
		t.Fatalf("content[1] should be ImageContent, got %T", res.Content[1])
	}
	if img.MIMEType != "image/jpeg" {
		t.Errorf("MIMEType = %q, want image/jpeg", img.MIMEType)
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("image data is not base64: %v", err)
	}
	if string(decoded) != string(jpegMagic) {
		t.Errorf("decoded bytes = %x, want %x", decoded, jpegMagic)
	}
}

func TestDownloadMediaResult_AudioInlined(t *testing.T) {
	path := writeFile(t, "voice.ogg", oggMagic)
	r := client.DownloadResult{
		Success:   true,
		Message:   "Successfully downloaded audio media",
		MediaType: "audio",
		Filename:  "voice.ogg",
		Path:      path,
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(res.Content))
	}
	aud, ok := res.Content[1].(mcp.AudioContent)
	if !ok {
		t.Fatalf("content[1] should be AudioContent, got %T", res.Content[1])
	}
	if !strings.HasPrefix(aud.MIMEType, "audio/") {
		t.Errorf("MIMEType = %q, want audio/*", aud.MIMEType)
	}
}

func TestDownloadMediaResult_DocumentPathOnly(t *testing.T) {
	path := writeFile(t, "spec.pdf", []byte("%PDF-1.4"))
	r := client.DownloadResult{
		Success:   true,
		Message:   "Successfully downloaded document media",
		MediaType: "document",
		Filename:  "spec.pdf",
		Path:      path,
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block (document is not inlined), got %d", len(res.Content))
	}
	assertJSONSummary(t, res, r)
}

func TestDownloadMediaResult_VideoPathOnly(t *testing.T) {
	path := writeFile(t, "clip.mp4", []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p'})
	r := client.DownloadResult{
		Success:   true,
		Message:   "Successfully downloaded video media",
		MediaType: "video",
		Filename:  "clip.mp4",
		Path:      path,
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block for video, got %d", len(res.Content))
	}
}

func TestDownloadMediaResult_FailurePathOnly(t *testing.T) {
	r := client.DownloadResult{
		Success: false,
		Message: "failed to download media: download failed with status code 403",
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block on failure, got %d", len(res.Content))
	}
	assertJSONSummary(t, res, r)
}

func TestDownloadMediaResult_MissingFileFallsBack(t *testing.T) {
	r := client.DownloadResult{
		Success:   true,
		Message:   "Successfully downloaded image media",
		MediaType: "image",
		Filename:  "gone.jpg",
		Path:      filepath.Join(t.TempDir(), "does-not-exist.jpg"),
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block when file is missing, got %d", len(res.Content))
	}
}

func TestDownloadMediaResult_OversizedNotInlined(t *testing.T) {
	oversized := make([]byte, inlineMediaMaxBytes+1)
	copy(oversized, jpegMagic)
	path := writeFile(t, "huge.jpg", oversized)

	r := client.DownloadResult{
		Success:   true,
		Message:   "Successfully downloaded image media",
		MediaType: "image",
		Filename:  "huge.jpg",
		Path:      path,
	}

	res, err := downloadMediaResult(r)
	if err != nil {
		t.Fatalf("downloadMediaResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected 1 content block for oversized file, got %d", len(res.Content))
	}
}
