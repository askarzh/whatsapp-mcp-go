package config

import (
	"fmt"
	"os"
)

type dbConfig struct {
	User       string
	Pass       string
	Host       string
	Port       string
	IsPostgres bool
}

type Config struct {
	DB        dbConfig
	JWTSecret []byte
	APIKey    string
}

func LoadConfig() (*Config, error) {
	user, ok := os.LookupEnv("POSTGRES_USER")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_USER")
	}
	pass, ok := os.LookupEnv("POSTGRES_PASS")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_PASS")
	}
	host, ok := os.LookupEnv("POSTGRES_HOST")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_HOST")
	}
	port, ok := os.LookupEnv("POSTGRES_PORT")
	if !ok {
		return nil, fmt.Errorf("missing POSTGRES_PORT")
	}

	jwtSecret, ok := os.LookupEnv("JWT_SECRET")
	if !ok {
		return nil, fmt.Errorf("missing JWT_SECRET")
	}
	apiKey, ok := os.LookupEnv("API_KEY")
	if !ok {
		return nil, fmt.Errorf("missing API_KEY")
	}

	isPostgres := os.Getenv("IS_POSTGRES") == "true"

	return &Config{
		DB: dbConfig{
			User:       user,
			Pass:       pass,
			Host:       host,
			Port:       port,
			IsPostgres: isPostgres,
		},
		JWTSecret: []byte(jwtSecret),
		APIKey:    apiKey,
	}, nil
}
