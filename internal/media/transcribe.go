// Package media provides audio transcription helpers for WhatsApp voice notes.
package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// ConvertToWAV converts any audio format ffmpeg can read (including Ogg Opus
// WhatsApp voice notes) to 16kHz mono WAV, suitable for speech-to-text
// endpoints. Returns the WAV bytes.
func ConvertToWAV(ctx context.Context, audioData []byte, inputFormat string) ([]byte, error) {
	if len(audioData) == 0 {
		return nil, fmt.Errorf("media: empty audio data")
	}

	// Write input to a temp file.
	inFile, err := os.CreateTemp("", "stt-input.*."+inputFormat)
	if err != nil {
		return nil, fmt.Errorf("media: create input temp file: %w", err)
	}
	inPath := inFile.Name()
	if _, err := inFile.Write(audioData); err != nil {
		inFile.Close()
		os.Remove(inPath)
		return nil, fmt.Errorf("media: write input temp file: %w", err)
	}
	inFile.Close()
	defer os.Remove(inPath)

	outFile, err := os.CreateTemp("", "stt-output.*.wav")
	if err != nil {
		os.Remove(inPath)
		return nil, fmt.Errorf("media: create output temp file: %w", err)
	}
	outPath := outFile.Name()
	outFile.Close()
	defer os.Remove(outPath)

	// ffmpeg: decode input to 16kHz mono WAV (PCM_S16LE).
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inPath,
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-y",
		outPath,
	)
	cmd.Env = minimalEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("media: ffmpeg convert to WAV failed: %w: %s", err, sanitizeStderr(stderr.String()))
	}

	wavData, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("media: read WAV output: %w", err)
	}

	return wavData, nil
}


