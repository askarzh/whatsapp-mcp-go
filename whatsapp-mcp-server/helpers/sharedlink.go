package helpers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SharedDir is the space files-mcp serves. The bridge writes decrypted media
// into a per-chat subdirectory of it (MEDIA_DOWNLOAD_DIR), which keeps the
// cache tidy but hides it from files-mcp — that lists the root only.
func sharedDir() string { return ReadEnv("SHARED_DIR", "/shared") }

// linkIntoSharedRoot publishes a file that already lives under `shared` at the
// flat root, where files-mcp can list, link and serve it. It hardlinks rather
// than copies: one set of bytes, and the retention sweep reclaiming the flat
// name leaves the per-chat cache intact.
func linkIntoSharedRoot(shared, path string) (string, error) {
	absShared, err := filepath.Abs(shared)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absShared, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("media path %q is outside the shared space", path)
	}
	if filepath.Dir(rel) == "." {
		return filepath.Base(abs), nil // already flat
	}

	name := "whatsapp_" + filepath.Base(abs)
	dst := filepath.Join(absShared, name)

	switch err := os.Link(abs, dst); {
	case err == nil:
		return name, nil
	case errors.Is(err, os.ErrExist):
		// Same media downloaded twice, or a name collision across chats. If the
		// existing entry is the very same inode we are done; otherwise fall
		// back to a copy under a distinct name rather than serving the wrong file.
		if sameFile(abs, dst) {
			return name, nil
		}
		return copyUnique(abs, absShared, name)
	default:
		// Cross-device or a filesystem without hardlinks — copy instead.
		return copyUnique(abs, absShared, name)
	}
}

func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

func copyUnique(src, dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 100; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s_%d%s", stem, i, ext)
		}
		dst := filepath.Join(dir, candidate)
		if i > 0 && sameFile(src, dst) {
			return candidate, nil
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		defer f.Close()
		data, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", fmt.Errorf("could not find a free name for %q", name)
}
