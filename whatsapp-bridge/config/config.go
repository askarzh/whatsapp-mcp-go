package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type dbConfig struct {
	User       string
	Pass       string
	Host       string
	Port       string
	IsPostgres bool
	SSLMode    string
}

// ConnString returns a postgres connection URL for dbName with credentials
// URL-escaped, so passwords containing reserved characters don't corrupt it.
func (d dbConfig) ConnString(dbName string) string {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(d.User, d.Pass),
		Host:     d.Host + ":" + d.Port,
		Path:     "/" + dbName,
		RawQuery: "sslmode=" + url.QueryEscape(d.SSLMode),
	}
	return u.String()
}

type Config struct {
	DB            dbConfig
	JWTSecret     []byte
	APIKey        string
	WebhookUrl    string
	Host          string
	Port          int
	AuthLoginRate string
	// MediaDirs are the only directories /api/send may read media files from.
	MediaDirs []string

	// MediaDownloadDir is where /api/download writes decrypted media. It
	// defaults to the bridge's private "store", which no other container can
	// read — point it at a shared mount (e.g. /shared/whatsapp) when the file
	// has to be reachable by files-mcp or the MCP server.
	MediaDownloadDir string

	// MindetBridgeToken is the one static bearer that opens /bridge/v1 for
	// Mindet. Empty disables the contract surface entirely; it is a separate
	// door from the JWT that guards /api, so neither caller can use the
	// other's credential.
	MindetBridgeToken string

	// MediaSharedRoot is the mount both this bridge and Mindet's daemon can
	// see; a contract message's file path is relative to it.
	MediaSharedRoot string

	// PublicURL is where Mindet reaches this bridge.
	PublicURL string
}

func LoadConfig() (*Config, error) {
	isPostgres := os.Getenv("IS_POSTGRES") == "true"

	var user, pass, host, port string
	if isPostgres {
		var ok bool
		user, ok = os.LookupEnv("POSTGRES_USER")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_USER")
		}
		pass, ok = os.LookupEnv("POSTGRES_PASS")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_PASS")
		}
		host, ok = os.LookupEnv("POSTGRES_HOST")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_HOST")
		}
		port, ok = os.LookupEnv("POSTGRES_PORT")
		if !ok {
			return nil, fmt.Errorf("missing POSTGRES_PORT")
		}
	}

	jwtSecret, ok := lookupEither("WHATSAPP_JWT_SECRET", "JWT_SECRET")
	if !ok {
		return nil, fmt.Errorf("missing WHATSAPP_JWT_SECRET")
	}
	apiKey, ok := lookupEither("WHATSAPP_API_KEY", "API_KEY")
	if !ok {
		return nil, fmt.Errorf("missing WHATSAPP_API_KEY")
	}
	if err := validateSecret("WHATSAPP_JWT_SECRET (or deprecated alias JWT_SECRET)", jwtSecret); err != nil {
		return nil, err
	}
	if err := validateSecret("WHATSAPP_API_KEY (or deprecated alias API_KEY)", apiKey); err != nil {
		return nil, err
	}
	webhookUrl := os.Getenv("WEBHOOK_URL")

	serverHost := os.Getenv("HOST")
	serverPort := 8080
	if v, ok := os.LookupEnv("PORT"); ok {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT %q: %w", v, err)
		}
		serverPort = p
	}

	authLoginRate := os.Getenv("AUTH_LOGIN_RATE") // parsed in auth package; empty -> default

	mindetToken := os.Getenv("MINDET_BRIDGE_TOKEN")
	if mindetToken != "" {
		if err := validateSecret("MINDET_BRIDGE_TOKEN", mindetToken); err != nil {
			return nil, err
		}
	} else {
		envWarnFn("MINDET_BRIDGE_TOKEN not set; /bridge/v1 is disabled")
	}
	sharedRoot := os.Getenv("MEDIA_SHARED_ROOT")
	if sharedRoot == "" {
		sharedRoot = "/shared"
	}
	publicURL := os.Getenv("BRIDGE_PUBLIC_URL")
	if publicURL == "" {
		publicURL = fmt.Sprintf("http://localhost:%d", serverPort)
	}

	sslMode := os.Getenv("POSTGRES_SSLMODE")
	if sslMode == "" {
		sslMode = "disable"
	}

	// Default allows the bridge's own store plus the OS temp dir (the MCP
	// server writes converted voice notes there in same-host deployments).
	mediaDownloadDir := os.Getenv("MEDIA_DOWNLOAD_DIR")
	if mediaDownloadDir == "" {
		mediaDownloadDir = "store"
	}

	// Default: the download dir plus the OS temp dir (the MCP server writes
	// converted voice notes there in same-host deployments), so a file the
	// bridge just downloaded can be sent back out. An explicit
	// MEDIA_ALLOWED_DIRS is an allowlist and stays authoritative.
	mediaDirs := []string{"store", mediaDownloadDir, os.TempDir()}
	if v := os.Getenv("MEDIA_ALLOWED_DIRS"); v != "" {
		mediaDirs = strings.Split(v, ":")
	} else if mediaDownloadDir == "store" {
		mediaDirs = []string{"store", os.TempDir()}
	}

	// The contract's file paths are MediaDownloadDir-relative-to-MediaSharedRoot
	// (spec §3.8): if the download dir isn't inside the shared root, every
	// files[].path the bridge would hand Mindet is wrong. The bare /api path
	// has no such requirement, so this only bites when the contract is on.
	if mindetToken != "" {
		absDownload, err := filepath.Abs(mediaDownloadDir)
		if err != nil {
			return nil, fmt.Errorf("resolving MEDIA_DOWNLOAD_DIR %q: %w", mediaDownloadDir, err)
		}
		absShared, err := filepath.Abs(sharedRoot)
		if err != nil {
			return nil, fmt.Errorf("resolving MEDIA_SHARED_ROOT %q: %w", sharedRoot, err)
		}
		rel, err := filepath.Rel(absShared, absDownload)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("MEDIA_DOWNLOAD_DIR (%s) must be inside MEDIA_SHARED_ROOT (%s) when MINDET_BRIDGE_TOKEN is set, "+
				"so /bridge/v1 file paths resolve for Mindet", mediaDownloadDir, sharedRoot)
		}
	}

	return &Config{
		DB: dbConfig{
			User:       user,
			Pass:       pass,
			Host:       host,
			Port:       port,
			IsPostgres: isPostgres,
			SSLMode:    sslMode,
		},
		JWTSecret:         []byte(jwtSecret),
		APIKey:            apiKey,
		WebhookUrl:        webhookUrl,
		Host:              serverHost,
		Port:              serverPort,
		AuthLoginRate:     authLoginRate,
		MediaDirs:         mediaDirs,
		MediaDownloadDir:  mediaDownloadDir,
		MindetBridgeToken: mindetToken,
		MediaSharedRoot:   sharedRoot,
		PublicURL:         publicURL,
	}, nil
}

const minSecretLen = 32

// knownPlaceholders are the example values shipped in docker-compose.yaml.
// Operators who forget to override them must see startup fail loudly.
var knownPlaceholders = []string{
	"c3VwZXItbG9uZy1yYW5kb20tc3RyaW5nLW1pbmltdW0tb2YtNjQtY2hhcmFjdGVycy15b3UtbmVlZC10by1wYXN0ZS1oZXJl",
	"YW5vdGhlci1zdXBlci1sb25nLXJhbmRvbS1zdHJpbmctbWluaW11bS1vZi02NC1jaGFyYWN0ZXJzLXlvdS1uZWVkLXRvLXBhc3RlLWhlcmU=",
}

func validateSecret(name, value string) error {
	for _, ph := range knownPlaceholders {
		if value == ph {
			return fmt.Errorf("%s is set to a placeholder value; generate a real one with `openssl rand -base64 48`", name)
		}
	}
	if len(value) < minSecretLen {
		return fmt.Errorf("%s is too short (%d chars, need ≥%d)", name, len(value), minSecretLen)
	}
	return nil
}

// envWarnFn is replaced in tests; in production it logs via slog.
var envWarnFn = func(msg string, args ...any) {
	slog.Warn(msg, args...)
}

// lookupEither returns the value for `primary` if set, otherwise falls back
// to `deprecated` and emits a deprecation warning. Returns ("", false) only
// when neither is set.
func lookupEither(primary, deprecated string) (string, bool) {
	if v, ok := os.LookupEnv(primary); ok {
		return v, true
	}
	if v, ok := os.LookupEnv(deprecated); ok {
		envWarnFn("env var is deprecated, use the new name",
			"deprecated", deprecated,
			"use_instead", primary)
		return v, true
	}
	return "", false
}
