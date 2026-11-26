package security

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func ValidateMediaPath(userPath, allowedRoot string) (string, error) {
	if userPath == "" {
		return "", nil
	}
	abs, err := filepath.Abs(userPath)
	if err != nil {
		return "", fmt.Errorf("invalid media_path %q: %w", userPath, err)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("media_path %q not accessible: %w", abs, err)
	}
	rootResolved, err := resolveRoot(allowedRoot)
	if err != nil {
		return "", err
	}
	if err := confine("media_path", resolved, rootResolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func resolveRoot(allowedRoot string) (string, error) {
	rootAbs, err := filepath.Abs(allowedRoot)
	if err != nil {
		return "", fmt.Errorf("invalid allowed root %q: %w", allowedRoot, err)
	}
	rootAbs = filepath.Clean(rootAbs)
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return rootAbs, nil
	}
	return rootResolved, nil
}

func confine(field, resolved, root string) error {
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return fmt.Errorf("%s %q is outside allowed root %q; set WHATSAPP_MCP_MEDIA_ROOT or place the file under that root", field, resolved, root)
	}
	return nil
}

func ValidateOutputPath(userPath, allowedRoot string) (string, error) {
	if userPath == "" {
		return "", fmt.Errorf("output_path is required when provided")
	}
	abs, err := filepath.Abs(userPath)
	if err != nil {
		return "", fmt.Errorf("invalid output_path %q: %w", userPath, err)
	}
	abs = filepath.Clean(abs)
	parent := filepath.Dir(abs)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("output_path parent directory %q not accessible: %w", parent, err)
	}
	resolved := filepath.Join(resolvedParent, filepath.Base(abs))

	rootResolved, err := resolveRoot(allowedRoot)
	if err != nil {
		return "", err
	}
	if err := confine("output_path", resolved, rootResolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func SafeFilename(raw string) string {
	if strings.ContainsRune(raw, '\x00') {
		return fallbackFilename()
	}
	base := filepath.Base(raw)
	switch base {
	case "", ".", "..", "/":
		return fallbackFilename()
	}
	return base
}

func fallbackFilename() string {
	return "document_" + time.Now().UTC().Format("20060102_150405")
}
