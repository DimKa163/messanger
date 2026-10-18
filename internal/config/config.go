package config

import (
	"errors"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:        ":8080",
		DatabaseURL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
		ShutdownTimeout: 10 * time.Second,
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if addr := strings.TrimSpace(os.Getenv("HTTP_ADDR")); addr != "" {
		cfg.HTTPAddr = addr
	}
	if value := strings.TrimSpace(os.Getenv("SHUTDOWN_TIMEOUT")); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return Config{}, errors.New("SHUTDOWN_TIMEOUT must be a positive duration (for example 10s)")
		}
		cfg.ShutdownTimeout = timeout
	}
	return cfg, nil
}
