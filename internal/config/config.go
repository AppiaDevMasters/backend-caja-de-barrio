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

// DefaultRafikiAdminURL is the Admin API endpoint of the local Rafiki stack
// (rafiki/docker-compose.yml publishes it on port 3001).
const DefaultRafikiAdminURL = "http://localhost:3001/graphql"

// DefaultOpenPaymentsURL is the base URL the ASE publishes for its wallet
// addresses (rafiki/docker-compose.yml publishes the resource server on port
// 3000).
//
// It is https because Rafiki refuses to create a wallet address whose URL is not
// https. A local client downgrades the scheme to http when it calls the resource
// server, which listens on plain http.
const DefaultOpenPaymentsURL = "https://localhost:3000"

// DefaultOPPrivateKeyPath is where the Open Payments client key is stored when
// OP_PRIVATE_KEY_PATH is not set. The path ends in .pem, which the repository
// .gitignore covers, so the key never reaches the history.
const DefaultOPPrivateKeyPath = "keys/op-private-key.pem"

// OP holds the Open Payments client settings: the key this backend signs its
// requests with and the name that key is registered under.
type OP struct {
	// KeyID is optional. When it is empty the key names itself with its RFC 7638
	// thumbprint.
	KeyID          string
	PrivateKeyPath string
}

// Rafiki holds the ASE side of the settings: the Admin API, the public base of
// the wallet addresses and the two secrets of the endpoints Rafiki and the
// identity provider call back. Load reads them but does not require them:
// /health has to answer before Rafiki is configured, and the component that
// needs a secret is the one that reports it missing.
type Rafiki struct {
	AdminURL        string
	OpenPaymentsURL string
	TenantID        string
	AdminAPISecret  string

	// SignatureSecret verifies the signature of every webhook Rafiki posts to
	// this backend.
	SignatureSecret string

	// IdentityServerSecret is shared with the identity provider that approves
	// interactive grants it opens in the user's browser.
	IdentityServerSecret string

	// AuthServerURL is the server to server address of the authorization server:
	// the one that resolves inside the compose network, http://rafiki-auth:3006.
	// The public address does not work here, because a container cannot reach the
	// host's published ports.
	AuthServerURL string

	// AuthServerPublicURL is the address the user's browser opens for an
	// interactive grant (the AUTH_SERVER_URL of rafiki/.env).
	AuthServerPublicURL string
}

// Config holds every setting the backend reads from the environment. It grows
// one task at a time; each field is added together with its consumer.
type Config struct {
	Addr     string
	LogLevel Level
	Rafiki   Rafiki
	OP       OP
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
		Addr:     defaultString(os.Getenv("HTTP_ADDR"), DefaultAddr),
		LogLevel: level,
		Rafiki: Rafiki{
			AdminURL:             defaultString(os.Getenv("RAFIKI_ADMIN_URL"), DefaultRafikiAdminURL),
			OpenPaymentsURL:      defaultString(os.Getenv("RAFIKI_OPEN_PAYMENTS_URL"), DefaultOpenPaymentsURL),
			TenantID:             strings.TrimSpace(os.Getenv("RAFIKI_TENANT_ID")),
			AdminAPISecret:       strings.TrimSpace(os.Getenv("RAFIKI_ADMIN_API_SECRET")),
			SignatureSecret:      strings.TrimSpace(os.Getenv("RAFIKI_SIGNATURE_SECRET")),
			IdentityServerSecret: strings.TrimSpace(os.Getenv("RAFIKI_IDENTITY_SERVER_SECRET")),
			AuthServerURL:        strings.TrimSpace(os.Getenv("RAFIKI_AUTH_SERVER_URL")),
			AuthServerPublicURL:  strings.TrimSpace(os.Getenv("RAFIKI_AUTH_SERVER_PUBLIC_URL")),
		},
		OP: OP{
			KeyID:          strings.TrimSpace(os.Getenv("OP_KEY_ID")),
			PrivateKeyPath: defaultString(os.Getenv("OP_PRIVATE_KEY_PATH"), DefaultOPPrivateKeyPath),
		},
	}, nil
}

func defaultString(raw, fallback string) string {
	if v := strings.TrimSpace(raw); v != "" {
		return v
	}
	return fallback
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
