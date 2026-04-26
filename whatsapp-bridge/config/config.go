package config

import (
	"fmt"
	"os"
	"strconv"
)

type dbConfig struct {
	User       string
	Pass       string
	Host       string
	Port       string
	IsPostgres bool
}

type Config struct {
	DB         dbConfig
	JWTSecret  []byte
	APIKey     string
	WebhookUrl string
	Host       string
	Port       int
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

	jwtSecret, ok := os.LookupEnv("JWT_SECRET")
	if !ok {
		return nil, fmt.Errorf("missing JWT_SECRET")
	}
	apiKey, ok := os.LookupEnv("API_KEY")
	if !ok {
		return nil, fmt.Errorf("missing API_KEY")
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

	return &Config{
		DB: dbConfig{
			User:       user,
			Pass:       pass,
			Host:       host,
			Port:       port,
			IsPostgres: isPostgres,
		},
		JWTSecret:  []byte(jwtSecret),
		APIKey:     apiKey,
		WebhookUrl: webhookUrl,
		Host:       serverHost,
		Port:       serverPort,
	}, nil
}
