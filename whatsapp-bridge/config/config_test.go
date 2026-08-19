package config

import (
	"slices"
	"strings"
	"testing"
)

func TestConnString(t *testing.T) {
	t.Run("builds a postgres URL with sslmode", func(t *testing.T) {
		db := dbConfig{User: "wa", Pass: "secret", Host: "postgres", Port: "5432", SSLMode: "require"}
		got := db.ConnString("whatsapp")
		want := "postgresql://wa:secret@postgres:5432/whatsapp?sslmode=require"
		if got != want {
			t.Errorf("ConnString = %q, want %q", got, want)
		}
	})

	t.Run("escapes special characters in credentials", func(t *testing.T) {
		db := dbConfig{User: "wa", Pass: "p@ss/w:rd?", Host: "postgres", Port: "5432", SSLMode: "disable"}
		got := db.ConnString("whatsapp")
		if strings.Contains(got, "p@ss/w:rd?") {
			t.Errorf("password not escaped in %q", got)
		}
		if !strings.Contains(got, "@postgres:5432/whatsapp") {
			t.Errorf("host/db malformed in %q", got)
		}
	})
}

func TestLoadConfigSSLModeAndMediaDirs(t *testing.T) {
	t.Setenv("IS_POSTGRES", "true")
	t.Setenv("POSTGRES_USER", "wa")
	t.Setenv("POSTGRES_PASS", "0123456789")
	t.Setenv("POSTGRES_HOST", "postgres")
	t.Setenv("POSTGRES_PORT", "5432")
	t.Setenv("WHATSAPP_JWT_SECRET", strings.Repeat("j", 32))
	t.Setenv("WHATSAPP_API_KEY", strings.Repeat("k", 32))

	t.Run("sslmode defaults to disable", func(t *testing.T) {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.DB.SSLMode != "disable" {
			t.Errorf("SSLMode = %q, want %q", cfg.DB.SSLMode, "disable")
		}
	})

	t.Run("sslmode honours POSTGRES_SSLMODE", func(t *testing.T) {
		t.Setenv("POSTGRES_SSLMODE", "verify-full")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.DB.SSLMode != "verify-full" {
			t.Errorf("SSLMode = %q, want %q", cfg.DB.SSLMode, "verify-full")
		}
	})

	t.Run("media dirs default to store and the OS temp dir", func(t *testing.T) {
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfg.MediaDirs) != 2 || cfg.MediaDirs[0] != "store" {
			t.Errorf("MediaDirs = %v, want [store <tempdir>]", cfg.MediaDirs)
		}
	})

	t.Run("media dirs honour MEDIA_ALLOWED_DIRS", func(t *testing.T) {
		t.Setenv("MEDIA_ALLOWED_DIRS", "/data/media:/shared")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(cfg.MediaDirs) != 2 || cfg.MediaDirs[0] != "/data/media" || cfg.MediaDirs[1] != "/shared" {
			t.Errorf("MediaDirs = %v, want [/data/media /shared]", cfg.MediaDirs)
		}
	})
}

// Downloaded media has to be reachable by the OTHER containers (files-mcp, the
// MCP server); "store" is private to the bridge, so the directory is
// configurable. It is also added to MediaDirs so a file that was downloaded can
// be sent back out again without extra configuration.
func TestMediaDownloadDir(t *testing.T) {
	t.Setenv("WHATSAPP_API_KEY", "0123456789012345678901234567890123456789")
	t.Setenv("WHATSAPP_JWT_SECRET", "0123456789012345678901234567890123456789")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MediaDownloadDir != "store" {
		t.Errorf("default MediaDownloadDir = %q, want \"store\"", cfg.MediaDownloadDir)
	}

	t.Setenv("MEDIA_DOWNLOAD_DIR", "/shared/whatsapp")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MediaDownloadDir != "/shared/whatsapp" {
		t.Errorf("MediaDownloadDir = %q, want /shared/whatsapp", cfg.MediaDownloadDir)
	}
	if !slices.Contains(cfg.MediaDirs, "/shared/whatsapp") {
		t.Errorf("MediaDirs = %v, want it to contain the download dir", cfg.MediaDirs)
	}

	// An explicit allowlist stays authoritative — we do not widen it.
	t.Setenv("MEDIA_ALLOWED_DIRS", "/data/media")
	cfg, err = LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !slices.Equal(cfg.MediaDirs, []string{"/data/media"}) {
		t.Errorf("MediaDirs = %v, want [/data/media] verbatim", cfg.MediaDirs)
	}
}
