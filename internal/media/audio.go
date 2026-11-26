package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

func AnalyzeOggOpus(data []byte) (duration uint32, waveform []byte, err error) {
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		return 0, nil, fmt.Errorf("not a valid Ogg file (missing OggS signature)")
	}

	var lastGranule uint64
	var sampleRate uint32 = 48000
	var preSkip uint16 = 0
	var foundOpusHead bool

	for i := 0; i < len(data); {
		if i+27 >= len(data) {
			break
		}

		if string(data[i:i+4]) != "OggS" {
			i++
			continue
		}

		granulePos := binary.LittleEndian.Uint64(data[i+6 : i+14])
		pageSeqNum := binary.LittleEndian.Uint32(data[i+18 : i+22])
		numSegments := int(data[i+26])

		if i+27+numSegments >= len(data) {
			break
		}
		segmentTable := data[i+27 : i+27+numSegments]

		pageSize := 27 + numSegments
		for _, segLen := range segmentTable {
			pageSize += int(segLen)
		}
		if i+pageSize > len(data) {
			break
		}

		if !foundOpusHead && pageSeqNum <= 1 {
			pageData := data[i : i+pageSize]
			if headPos := bytes.Index(pageData, []byte("OpusHead")); headPos >= 0 {
				head := headPos + 8
				if head+8 <= len(pageData) {
					preSkip = binary.LittleEndian.Uint16(pageData[head+2 : head+4])
					sampleRate = binary.LittleEndian.Uint32(pageData[head+4 : head+8])
					foundOpusHead = true
				}
			}
		}

		if granulePos != 0 {
			lastGranule = granulePos
		}

		i += pageSize
	}

	if lastGranule > 0 {
		durationSeconds := float64(lastGranule-uint64(preSkip)) / float64(sampleRate)
		duration = uint32(math.Ceil(durationSeconds))
	} else {
		durationEstimate := float64(len(data)) / 2000.0
		duration = uint32(durationEstimate)
	}

	if duration < 1 {
		duration = 1
	} else if duration > 300 {
		duration = 300
	}

	waveform = PlaceholderWaveform(duration)
	return duration, waveform, nil
}

func PlaceholderWaveform(duration uint32) []byte {
	const waveformLength = 64
	waveform := make([]byte, waveformLength)

	r := rand.New(rand.NewPCG(uint64(duration), 0))

	baseAmplitude := 35.0
	d := int(duration)
	if d > 120 {
		d = 120
	}
	frequencyFactor := float64(d) / 30.0

	for i := range waveform {
		pos := float64(i) / float64(waveformLength)

		val := baseAmplitude * math.Sin(pos*math.Pi*frequencyFactor*8)
		val += (baseAmplitude / 2) * math.Sin(pos*math.Pi*frequencyFactor*16)

		val += (r.Float64() - 0.5) * 15

		fadeInOut := math.Sin(pos * math.Pi)
		val = val * (0.7 + 0.3*fadeInOut)

		val = val + 50

		if val < 0 {
			val = 0
		} else if val > 100 {
			val = 100
		}

		waveform[i] = byte(val)
	}

	return waveform
}

func ConvertToOpusOgg(ctx context.Context, inputPath string) (outputPath string, err error) {
	return ConvertToOpusOggIn(ctx, inputPath, "")
}

func ConvertToOpusOggIn(ctx context.Context, inputPath, outputDir string) (outputPath string, err error) {
	if _, lookErr := exec.LookPath("ffmpeg"); lookErr != nil {
		return "", errors.New("ffmpeg not found on PATH; install ffmpeg to use voice messages")
	}

	tmp, err := os.CreateTemp(outputDir, "mcp-whatsapp-audio-*.ogg")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	safeInput := sanitizeFFmpegInputPath(inputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", safeInput,
		"-c:a", "libopus",
		"-b:a", "32k",
		"-vbr", "on",
		"-compression_level", "10",
		"-y",
		tmpPath,
	)
	cmd.Env = minimalEnv()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg conversion failed: %w: %s", err, sanitizeStderr(stderr.String()))
	}

	return tmpPath, nil
}

func sanitizeFFmpegInputPath(p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
		return p
	}
	if strings.HasPrefix(p, "-") {
		return "./" + p
	}
	return p
}

var safeEnvKeys = []string{"PATH", "TMPDIR", "TEMP", "TMP", "HOME"}

func minimalEnv() []string {
	env := make([]string, 0, len(safeEnvKeys))
	for _, k := range safeEnvKeys {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

var absPathRe = regexp.MustCompile(`/[\w/.\-]+`)

func sanitizeStderr(s string) string {
	s = absPathRe.ReplaceAllString(s, "<path>")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
