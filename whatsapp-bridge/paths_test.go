package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeChildPath(t *testing.T) {
	base := t.TempDir()

	t.Run("plain name stays inside base", func(t *testing.T) {
		got, err := safeChildPath(base, "77001234567@s.whatsapp.net")
		if err != nil {
			t.Fatalf("safeChildPath: %v", err)
		}
		if !strings.HasPrefix(got, base+string(os.PathSeparator)) {
			t.Errorf("result %q escapes base %q", got, base)
		}
	})

	t.Run("device-part colon is sanitized like the old code did", func(t *testing.T) {
		got, err := safeChildPath(base, "77001234567:12@s.whatsapp.net")
		if err != nil {
			t.Fatalf("safeChildPath: %v", err)
		}
		if strings.Contains(filepath.Base(got), ":") {
			t.Errorf("result %q still contains a colon", got)
		}
	})

	t.Run("dot-dot traversal is rejected", func(t *testing.T) {
		if got, err := safeChildPath(base, "../../etc/cron.d/evil"); err == nil {
			t.Errorf("traversal accepted: %q", got)
		}
	})

	t.Run("absolute path is rejected", func(t *testing.T) {
		if got, err := safeChildPath(base, "/etc/passwd"); err == nil {
			t.Errorf("absolute path accepted: %q", got)
		}
	})

	t.Run("nested separator collapses to a single level", func(t *testing.T) {
		// a sender-controlled document filename like "a/b" must not create
		// directories outside the chat dir
		got, err := safeChildPath(base, "sub/../../escape.txt")
		if err == nil && !strings.HasPrefix(got, base+string(os.PathSeparator)) {
			t.Errorf("result %q escapes base %q", got, base)
		}
	})
}

func TestAllowedMediaPath(t *testing.T) {
	storeDir := t.TempDir()
	tmpDir := t.TempDir()
	allowed := []string{storeDir, tmpDir}

	inside := filepath.Join(storeDir, "voice.ogg")
	if err := os.WriteFile(inside, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("file inside an allowed dir passes", func(t *testing.T) {
		if err := allowedMediaPath(allowed, inside); err != nil {
			t.Errorf("allowedMediaPath(%q) = %v, want nil", inside, err)
		}
	})

	t.Run("file outside all allowed dirs is rejected", func(t *testing.T) {
		if err := allowedMediaPath(allowed, "/etc/passwd"); err == nil {
			t.Error("/etc/passwd accepted")
		}
	})

	t.Run("traversal out of an allowed dir is rejected", func(t *testing.T) {
		sneaky := filepath.Join(storeDir, "..", "..", "etc", "passwd")
		if err := allowedMediaPath(allowed, sneaky); err == nil {
			t.Errorf("traversal path %q accepted", sneaky)
		}
	})

	t.Run("prefix sibling dir is rejected", func(t *testing.T) {
		// /path/store-evil must not pass because it shares the string prefix
		// of /path/store
		sibling := storeDir + "-evil"
		if err := os.MkdirAll(sibling, 0755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(sibling, "x.jpg")
		if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := allowedMediaPath(allowed, f); err == nil {
			t.Errorf("sibling-dir path %q accepted", f)
		}
	})
}
