package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
)

// VideoInfo holds basic metadata extracted from a video file.
type VideoInfo struct {
	Duration uint32 // seconds, rounded up
	Width    uint32
	Height   uint32
}

// AnalyzeVideo probes a video file with ffprobe and extracts a JPEG thumbnail
// (a frame near the start) with ffmpeg. Returns the info, the thumbnail bytes
// (nil on failure), and any error from probing.
func AnalyzeVideo(ctx context.Context, path string) (VideoInfo, []byte, error) {
	info := VideoInfo{}

	// Probe metadata.
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-show_entries", "format=duration",
		"-of", "json",
		path,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return info, nil, fmt.Errorf("ffprobe failed: %w", err)
	}

	type stream struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	type format struct {
		Duration string `json:"duration"`
	}
	type probe struct {
		Streams []stream `json:"streams"`
		Format  format   `json:"format"`
	}
	var p probe
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		return info, nil, fmt.Errorf("ffprobe parse: %w", err)
	}
	if len(p.Streams) > 0 {
		info.Width = uint32(p.Streams[0].Width)
		info.Height = uint32(p.Streams[0].Height)
	}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		// round up
		secs := uint32(d)
		if float64(secs) < d {
			secs++
		}
		if secs == 0 {
			secs = 1
		}
		info.Duration = secs
	}
	if info.Duration == 0 {
		info.Duration = 1
	}

	// Extract a thumbnail frame.
	thumbCmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-ss", "1",
		"-i", path,
		"-frames:v", "1",
		"-q:v", "5",
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-",
	)
	var thumbBuf bytes.Buffer
	thumbCmd.Stdout = &thumbBuf
	// discard stderr
	if err := thumbCmd.Run(); err != nil {
		// thumbnail optional; don't fail the whole call
		return info, nil, nil
	}
	thumb := thumbBuf.Bytes()
	if len(thumb) == 0 {
		thumb = nil
	}
	return info, thumb, nil
}
