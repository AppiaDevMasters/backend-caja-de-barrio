package config_test

import (
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
)

// The service must start with sane defaults when the environment is empty, so a
// local `go run ./cmd/backend` works before anyone writes a full .env (S1, S3).
func TestLoadUsesDefaultsWhenEnvironmentIsEmpty(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("LOG_LEVEL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() with an empty environment returned an error: %v", err)
	}
	if cfg.Addr != ":8000" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, ":8000")
	}
	if cfg.LogLevel != config.LevelInfo {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, config.LevelInfo)
	}
}

func TestLoadReadsAddrFromEnvironment(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:9090")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9090" {
		t.Errorf("Addr = %q, want %q", cfg.Addr, "127.0.0.1:9090")
	}
}

func TestLoadRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() accepted LOG_LEVEL=verbose, want an error")
	}
	if !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Errorf("error %q does not name the offending variable LOG_LEVEL", err)
	}
}
