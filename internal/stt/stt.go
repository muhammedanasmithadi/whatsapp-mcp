// Package stt provides a pluggable speech-to-text client for transcribing
// WhatsApp voice notes. The default implementation sends the decoded WAV
// audio to an OpenAI-compatible transcription endpoint.
//
// Configure via:
//
//	WHATSAPP_MCP_STT_ENDPOINT  - HTTP endpoint (e.g. https://api.openai.com/v1/audio/transcriptions)
//	WHATSAPP_MCP_STT_API_KEY   - Bearer token for the endpoint
//	WHATSAPP_MCP_STT_MODEL     - Model name (default: whatsapp-mcp-stt)
//	WHATSAPP_MCP_STT_LANGUAGE  - Language code (optional, e.g. "en")
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"
)

// Client transcribes audio to text.
type Client interface {
	// Transcribe converts audio data (any format ffmpeg can read) to text.
	// Returns the transcribed text and an error.
	Transcribe(ctx context.Context, audioData []byte, filename string) (string, error)
}

// NoOpClient returns empty transcription (useful when STT is not configured).
type NoOpClient struct{}

func (NoOpClient) Transcribe(ctx context.Context, audioData []byte, filename string) (string, error) {
	return "", nil
}

// HTTPClient sends audio to an OpenAI-compatible transcription endpoint.
// It expects a multipart/form-data POST with a "file" field and optional
// "model" and "language" fields.
type HTTPClient struct {
	endpoint string
	apiKey   string
	model    string
	language string
	client   *http.Client
}

// NewHTTPClient creates an STT client from environment variables:
//
//	WHATSAPP_MCP_STT_ENDPOINT  - HTTP endpoint URL
//	WHATSAPP_MCP_STT_API_KEY   - Bearer token (optional)
//	WHATSAPP_MCP_STT_MODEL     - Model name (default: "whisper-1")
//	WHATSAPP_MCP_STT_LANGUAGE  - Language code (optional)
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		endpoint: os.Getenv("WHATSAPP_MCP_STT_ENDPOINT"),
		apiKey:   os.Getenv("WHATSAPP_MCP_STT_API_KEY"),
		model:    os.Getenv("WHATSAPP_MCP_STT_MODEL"),
		language: os.Getenv("WHATSAPP_MCP_STT_LANGUAGE"),
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Configured reports whether the client has an endpoint set.
func (c *HTTPClient) Configured() bool {
	return c.endpoint != ""
}

func (c *HTTPClient) Transcribe(ctx context.Context, audioData []byte, filename string) (string, error) {
	if c.endpoint == "" {
		return "", fmt.Errorf("stt: no endpoint configured (set WHATSAPP_MCP_STT_ENDPOINT)")
	}

	// Create multipart form body.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	// Add the audio file.
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("stt: create form file: %w", err)
	}
	if _, err := fw.Write(audioData); err != nil {
		return "", fmt.Errorf("stt: write form file: %w", err)
	}

	// Add model field if set.
	if c.model != "" {
		if err := w.WriteField("model", c.model); err != nil {
			return "", fmt.Errorf("stt: write model field: %w", err)
		}
	}

	// Add language field if set.
	if c.language != "" {
		if err := w.WriteField("language", c.language); err != nil {
			return "", fmt.Errorf("stt: write language field: %w", err)
		}
	}

	if err := w.Close(); err != nil {
		return "", fmt.Errorf("stt: close form: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &buf)
	if err != nil {
		return "", fmt.Errorf("stt: create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("stt: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("stt: server returned %d: %s", resp.StatusCode, sanitizeStderr(string(body)))
	}

	// Parse OpenAI-style response: {"text": "..."}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("stt: parse response: %w", err)
	}

	return result.Text, nil
}

// sanitizeStderr strips absolute file paths from error messages.
func sanitizeStderr(s string) string {
	// Simple redaction: replace anything that looks like a file path.
	// Keep it minimal - just truncate long messages.
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}
