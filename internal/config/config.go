package config

import (
	"errors"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL     string        `env:"DATABASE_URL,required,notEmpty"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"10s"`
	JWTSecret       string        `env:"JWT_SECRET,required,notEmpty"`
	JWTTTL          time.Duration `env:"JWT_TTL" envDefault:"1h"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	cfg.DatabaseURL = strings.TrimSpace(cfg.DatabaseURL)
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if len(cfg.JWTSecret) < 32 || strings.TrimSpace(cfg.JWTSecret) != cfg.JWTSecret {
		return Config{}, errors.New("JWT_SECRET must contain at least 32 bytes without surrounding whitespace")
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, errors.New("SHUTDOWN_TIMEOUT must be a positive duration")
	}
	if cfg.JWTTTL < time.Second || cfg.JWTTTL > 24*time.Hour {
		return Config{}, errors.New("JWT_TTL must be a duration between 1s and 24h")
	}
	return cfg, nil
}
