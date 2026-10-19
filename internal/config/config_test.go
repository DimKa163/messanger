package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	setValidEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.ShutdownTimeout != 10*time.Second || cfg.JWTTTL != time.Hour {
		t.Fatalf("unexpected defaults: address=%s, shutdown=%s", cfg.HTTPAddr, cfg.ShutdownTimeout)
	}
	t.Setenv("HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("JWT_TTL", "30m")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.ShutdownTimeout != 3*time.Second || cfg.JWTTTL != 30*time.Minute {
		t.Fatalf("overrides ignored: address=%s, shutdown=%s", cfg.HTTPAddr, cfg.ShutdownTimeout)
	}
}

func TestLoadRejectsMissingDatabase(t *testing.T) {
	setValidEnvironment(t)
	for _, value := range []string{"", " ", "\t\r\n"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", value)
			if _, err := Load(); err == nil {
				t.Fatal("expected an error for empty or whitespace-only DATABASE_URL")
			}
		})
	}
	t.Run("unset", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		if err := os.Unsetenv("DATABASE_URL"); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatal("expected an error for unset DATABASE_URL")
		}
	})
}

func TestLoadTrimsDatabaseURL(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("DATABASE_URL", " \tpostgres://localhost/messanger\r\n")
	cfg, err := Load()
	if err != nil || cfg.DatabaseURL != "postgres://localhost/messanger" {
		t.Fatalf("database URL was not normalized: error=%v", err)
	}
}

func TestLoadRejectsInvalidTimeout(t *testing.T) {
	setValidEnvironment(t)
	for _, value := range []string{"0s", "-1s", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SHUTDOWN_TIMEOUT", value)
			if _, err := Load(); err == nil {
				t.Fatal("expected an error for invalid shutdown timeout")
			}
		})
	}
}

func TestLoadRejectsInvalidJWTConfig(t *testing.T) {
	setValidEnvironment(t)
	for _, tc := range []struct{ name, secret string }{
		{"empty", ""},
		{"short", "short"},
		{"spaces", "                                "},
		{"leading space", " test-secret-with-at-least-32-bytes"},
		{"trailing space", "test-secret-with-at-least-32-bytes "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JWT_SECRET", tc.secret)
			if _, err := Load(); err == nil {
				t.Fatal("accepted invalid JWT secret")
			}
		})
	}
	t.Run("unset secret", func(t *testing.T) {
		t.Setenv("JWT_SECRET", "")
		if err := os.Unsetenv("JWT_SECRET"); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatal("accepted unset JWT secret")
		}
	})
	for _, ttl := range []string{"0s", "-1s", "1ns", "999ms", "24h1ns", "25h", "invalid"} {
		t.Run(ttl, func(t *testing.T) {
			t.Setenv("JWT_TTL", ttl)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted invalid JWT_TTL %q", ttl)
			}
		})
	}
}

func TestLoadAcceptsJWTBoundaries(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("JWT_SECRET", "12345678901234567890123456789012")
	for _, ttl := range []string{"1s", "30m", "24h"} {
		t.Run(ttl, func(t *testing.T) {
			t.Setenv("JWT_TTL", ttl)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			want, _ := time.ParseDuration(ttl)
			if cfg.JWTTTL != want {
				t.Fatalf("JWT_TTL = %s; want %s", cfg.JWTTTL, want)
			}
		})
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/messanger")
	t.Setenv("JWT_SECRET", "test-secret-with-at-least-32-bytes")
	t.Setenv("JWT_TTL", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
}
