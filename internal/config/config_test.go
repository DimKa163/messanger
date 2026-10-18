package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/messanger")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: address=%s, shutdown=%s", cfg.HTTPAddr, cfg.ShutdownTimeout)
	}
	t.Setenv("HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("SHUTDOWN_TIMEOUT", "3s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("overrides ignored: address=%s, shutdown=%s", cfg.HTTPAddr, cfg.ShutdownTimeout)
	}
}

func TestLoadRejectsMissingDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", " ")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for missing DATABASE_URL")
	}
}

func TestLoadRejectsInvalidTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/messanger")
	for _, value := range []string{"0s", "-1s", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SHUTDOWN_TIMEOUT", value)
			if _, err := Load(); err == nil {
				t.Fatal("expected an error for invalid shutdown timeout")
			}
		})
	}
}
