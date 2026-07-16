package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// safeChildPath returns base/name, guaranteeing the result stays inside base.
// name is treated as a single path component: separators and drive/root
// prefixes are stripped and colons replaced (JID device parts), so
// sender-controlled values like document filenames or chat JIDs cannot
// traverse out of base.
func safeChildPath(base, name string) (string, error) {
	sanitized := strings.ReplaceAll(name, ":", "_")
	if filepath.IsAbs(sanitized) {
		return "", fmt.Errorf("absolute path %q not allowed", name)
	}
	for part := range strings.FieldsFuncSeq(sanitized, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", fmt.Errorf("path %q attempts directory traversal", name)
		}
	}

	cleaned := filepath.Base(filepath.Clean(sanitized))
	if cleaned == "." || cleaned == string(filepath.Separator) || cleaned == "" {
		return "", fmt.Errorf("invalid path component %q", name)
	}

	joined := filepath.Join(base, cleaned)

	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if absJoined == absBase || !strings.HasPrefix(absJoined, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %q", name, base)
	}
	return joined, nil
}

// allowedMediaPath returns nil when p resolves to a location inside one of
// the allowed directories; otherwise an error describing the rejection.
func allowedMediaPath(allowed []string, p string) error {
	absPath, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return err
	}
	for _, dir := range allowed {
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("media path %q is outside the allowed directories (set MEDIA_ALLOWED_DIRS to change)", p)
}
