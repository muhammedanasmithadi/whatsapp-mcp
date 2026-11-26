package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/muhammedanasmithadi/whatsapp-mcp/internal/client"
)

func resultJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

const inlineMediaMaxBytes = 5 * 1024 * 1024

func downloadMediaResult(r client.DownloadResult) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
	}
	contents := []mcp.Content{mcp.NewTextContent(string(b))}

	if r.Success && (r.MediaType == "image" || r.MediaType == "audio") && r.Path != "" {
		if inlined, ok := readInlineMedia(r.Path, r.MediaType); ok {
			contents = append(contents, inlined)
		}
	}

	return &mcp.CallToolResult{Content: contents}, nil
}

func readInlineMedia(path, mediaType string) (mcp.Content, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	if info.Size() > inlineMediaMaxBytes {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	mimeType := sniffResourceMIME(data, mediaType)
	encoded := base64.StdEncoding.EncodeToString(data)
	switch mediaType {
	case "image":
		return mcp.NewImageContent(encoded, mimeType), true
	case "audio":
		return mcp.NewAudioContent(encoded, mimeType), true
	}
	return nil, false
}
