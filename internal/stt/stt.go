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

type Client interface {
	Transcribe(ctx context.Context, audioData []byte, filename string) (string, error)
}

type NoOpClient struct{}

func (NoOpClient) Transcribe(ctx context.Context, audioData []byte, filename string) (string, error) {
	return "", nil
}

type HTTPClient struct {
	endpoint string
	apiKey   string
	model    string
	language string
	client   *http.Client
}

func NewHTTPClient() *HTTPClient {
	return &HTTPClient{
		endpoint: os.Getenv("WHATSAPP_MCP_STT_ENDPOINT"),
		apiKey:   os.Getenv("WHATSAPP_MCP_STT_API_KEY"),
		model:    os.Getenv("WHATSAPP_MCP_STT_MODEL"),
		language: os.Getenv("WHATSAPP_MCP_STT_LANGUAGE"),
		client:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *HTTPClient) Configured() bool {
	return c.endpoint != ""
}

func (c *HTTPClient) Transcribe(ctx context.Context, audioData []byte, filename string) (string, error) {
	if c.endpoint == "" {
		return "", fmt.Errorf("stt: no endpoint configured (set WHATSAPP_MCP_STT_ENDPOINT)")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("stt: create form file: %w", err)
	}
	if _, err := fw.Write(audioData); err != nil {
		return "", fmt.Errorf("stt: write form file: %w", err)
	}

	if c.model != "" {
		if err := w.WriteField("model", c.model); err != nil {
			return "", fmt.Errorf("stt: write model field: %w", err)
		}
	}

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

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("stt: parse response: %w", err)
	}

	return result.Text, nil
}

func sanitizeStderr(s string) string {
	if len(s) > 500 {
		s = s[:500] + "..."
	}
	return s
}
