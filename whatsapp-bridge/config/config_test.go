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

// MINDET_BRIDGE_TOKEN gates /bridge/v1: too short is a startup error like any
// other secret, unset is allowed but warns (the surface just stays off).
func TestMindetBridgeToken(t *testing.T) {
	t.Setenv("WHATSAPP_API_KEY", strings.Repeat("k", 32))
	t.Setenv("WHATSAPP_JWT_SECRET", strings.Repeat("j", 32))

	t.Run("too short is an error naming the var", func(t *testing.T) {
		t.Setenv("MINDET_BRIDGE_TOKEN", "short")
		_, err := LoadConfig()
		if err == nil || !strings.Contains(err.Error(), "MINDET_BRIDGE_TOKEN") {
			t.Fatalf("LoadConfig error = %v, want it to mention MINDET_BRIDGE_TOKEN", err)
		}
	})

	t.Run("unset warns but does not fail", func(t *testing.T) {
		t.Setenv("MINDET_BRIDGE_TOKEN", "")
		var warned bool
		old := envWarnFn
		envWarnFn = func(msg string, args ...any) {
			if strings.Contains(msg, "MINDET_BRIDGE_TOKEN") {
				warned = true
			}
		}
		t.Cleanup(func() { envWarnFn = old })

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.MindetBridgeToken != "" {
			t.Errorf("MindetBridgeToken = %q, want empty", cfg.MindetBridgeToken)
		}
		if !warned {
			t.Error("expected a warning naming MINDET_BRIDGE_TOKEN")
		}
	})

	t.Run("valid token passes through and defaults apply", func(t *testing.T) {
		t.Setenv("MINDET_BRIDGE_TOKEN", strings.Repeat("t", 32))
		// The default MediaDownloadDir ("store", relative to the working
		// directory) is not inside the default MediaSharedRoot ("/shared"),
		// so with the token on this needs an explicit download dir that is.
		t.Setenv("MEDIA_DOWNLOAD_DIR", "/shared/whatsapp")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.MindetBridgeToken != strings.Repeat("t", 32) {
			t.Errorf("MindetBridgeToken = %q", cfg.MindetBridgeToken)
		}
		if cfg.MediaSharedRoot != "/shared" {
			t.Errorf("MediaSharedRoot = %q, want /shared", cfg.MediaSharedRoot)
		}
		if cfg.PublicURL != "http://localhost:8080" {
			t.Errorf("PublicURL = %q, want http://localhost:8080", cfg.PublicURL)
		}
	})
}

// The contract's files[].path is MediaDownloadDir made relative to
// MediaSharedRoot; if the download dir isn't inside the shared root, every
// path the bridge hands Mindet would be nonsense. Startup must refuse to
// come up in that state — but only when MINDET_BRIDGE_TOKEN is set, since
// the bare /api path has no such requirement.
func TestMediaDownloadDirMustBeInsideSharedRootWhenContractIsOn(t *testing.T) {
	t.Setenv("WHATSAPP_API_KEY", strings.Repeat("k", 32))
	t.Setenv("WHATSAPP_JWT_SECRET", strings.Repeat("j", 32))

	t.Run("outside the shared root with the token set fails", func(t *testing.T) {
		t.Setenv("MINDET_BRIDGE_TOKEN", strings.Repeat("t", 32))
		t.Setenv("MEDIA_SHARED_ROOT", "/shared")
		t.Setenv("MEDIA_DOWNLOAD_DIR", "/elsewhere/whatsapp")
		_, err := LoadConfig()
		if err == nil {
			t.Fatal("LoadConfig: want an error, got nil")
		}
		if !strings.Contains(err.Error(), "MEDIA_DOWNLOAD_DIR") || !strings.Contains(err.Error(), "MEDIA_SHARED_ROOT") {
			t.Fatalf("LoadConfig error = %v, want it to name both vars", err)
		}
	})

	t.Run("outside the shared root with no token set is fine", func(t *testing.T) {
		t.Setenv("MINDET_BRIDGE_TOKEN", "")
		t.Setenv("MEDIA_SHARED_ROOT", "/shared")
		t.Setenv("MEDIA_DOWNLOAD_DIR", "/elsewhere/whatsapp")
		if _, err := LoadConfig(); err != nil {
			t.Fatalf("LoadConfig without the token should not care about the coupling: %v", err)
		}
	})

	t.Run("inside the shared root with the token set passes", func(t *testing.T) {
		t.Setenv("MINDET_BRIDGE_TOKEN", strings.Repeat("t", 32))
		t.Setenv("MEDIA_SHARED_ROOT", "/shared")
		t.Setenv("MEDIA_DOWNLOAD_DIR", "/shared/whatsapp")
		if _, err := LoadConfig(); err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
	})
}
