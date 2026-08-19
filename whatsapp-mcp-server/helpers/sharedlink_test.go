package helpers

import (
	"os"
	"path/filepath"
	"testing"
)

// files-mcp lists /shared non-recursively, so media downloaded into
// /shared/whatsapp/<jid>/ is invisible to it — the caller gets a path nothing
// downstream can serve. Hardlink it into the flat root: same bytes, one inode,
// and the retention sweep can reclaim the flat name without touching the cache.
func TestLinkIntoSharedRoot(t *testing.T) {
	shared := t.TempDir()
	nested := filepath.Join(shared, "whatsapp", "77077254753@s.whatsapp.net")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(nested, "audio_20260818_064225.ogg")
	if err := os.WriteFile(src, []byte("OggS-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	name, err := linkIntoSharedRoot(shared, src)
	if err != nil {
		t.Fatalf("linkIntoSharedRoot: %v", err)
	}
	if name != "whatsapp_audio_20260818_064225.ogg" {
		t.Errorf("name = %q", name)
	}

	got, err := os.ReadFile(filepath.Join(shared, name))
	if err != nil {
		t.Fatalf("flat copy unreadable: %v", err)
	}
	if string(got) != "OggS-data" {
		t.Errorf("content = %q", got)
	}

	// Idempotent: downloading the same media twice must not pile up copies.
	again, err := linkIntoSharedRoot(shared, src)
	if err != nil || again != name {
		t.Errorf("second call = %q, %v; want same name", again, err)
	}
	entries, _ := os.ReadDir(shared)
	files := 0
	for _, e := range entries {
		if !e.IsDir() {
			files++
		}
	}
	if files != 1 {
		t.Errorf("flat root has %d files, want 1", files)
	}
}

// A path outside the shared space must not be linked into it.
func TestLinkIntoSharedRootRejectsOutsidePaths(t *testing.T) {
	shared := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.ogg")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if name, err := linkIntoSharedRoot(shared, outside); err == nil || name != "" {
		t.Errorf("got (%q, %v), want a rejection", name, err)
	}
}
