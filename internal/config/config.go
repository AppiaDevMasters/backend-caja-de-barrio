// Package config loads the backend settings from the process environment.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Level is an accepted LOG_LEVEL value.
type Level string

// Accepted log levels, from most to least verbose.
const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// DefaultAddr is the listening address used when HTTP_ADDR is not set. It
// matches the port rafiki/docker-compose.yml expects from this backend.
const DefaultAddr = ":8000"

// Config holds every setting the backend reads from the environment. It grows
// one task at a time; each field is added together with its consumer.
type Config struct {
	Addr     string
	LogLevel Level
}

// Load reads the environment and validates it. When a value cannot be used it
// returns an error naming the offending variable, so the process fails at start
// instead of at the first request.
func Load() (Config, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:     addr(os.Getenv("HTTP_ADDR")),
		LogLevel: level,
	}, nil
}

func addr(raw string) string {
	if v := strings.TrimSpace(raw); v != "" {
		return v
	}
	return DefaultAddr
}

func parseLevel(raw string) (Level, error) {
	level := Level(strings.ToLower(strings.TrimSpace(raw)))
	switch level {
	case "":
		return LevelInfo, nil
	case LevelDebug, LevelInfo, LevelWarn, LevelError:
		return level, nil
	default:
		return "", fmt.Errorf("LOG_LEVEL=%q is not one of debug, info, warn or error", raw)
	}
}

// Slog maps the level to its log/slog equivalent.
func (l Level) Slog() slog.Level {
	switch l {
	case LevelDebug:
		return slog.LevelDebug
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
