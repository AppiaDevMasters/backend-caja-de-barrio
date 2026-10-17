// Package config loads the backend settings from the process environment.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
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
	// ClientWalletAddress is OP_CLIENT_WALLET_ADDRESS: the wallet address this backend
	// acts as. When it is empty it is the published base plus the application's
	// account, which is the name the seed registers the client key on.
	ClientWalletAddress string

	// KeyID is optional. When it is empty the key names itself with its RFC 7638
	// thumbprint.
	KeyID          string
	PrivateKeyPath string
}

// Database holds the data layer settings: where the schema of the contract lives.
type Database struct {
	// URL is the PostgreSQL DSN (DATABASE_URL). It carries a credential, so it is
	// read here and never printed: the store is the component that knows how to
	// name the database without its password.
	URL string
}

// Internal holds the settings of the /internal surface the four MCPs call.
type Internal struct {
	// Token is SERVICIO_TOKEN: what every call to the surface carries in the
	// X-Servicio-Token header. It carries a credential, so it is read here and never
	// printed.
	Token string
}

// Panel holds the settings of the control panel surface (/admin).
type Panel struct {
	// Origenes is PANEL_ORIGINS, comma separated: the exact origins CORS allows.
	Origenes []string

	// Claves are PANEL_CLAVE_SUPERADMIN, PANEL_CLAVE_CUMPLIMIENTO and
	// PANEL_CLAVE_SOPORTE, by role. A role without a password gets no user.
	Claves map[string]string

	// AgenteURL is AGENTE_URL: where the agent receives notices for people.
	AgenteURL string
}

// Verifik holds the settings of the civil registry provider.
type Verifik struct {
	// Token is VERIFIK_TOKEN, the bearer token of the cédula query. It carries a
	// credential, so it is read here and never printed.
	Token string
}

// Didit holds the settings of the identity provider (contract 5.5 to 5.7). They carry
// credentials, so they are read here and never printed.
type Didit struct {
	// APIKey is DIDIT_API_KEY. Empty means Didit is not configured and the
	// verification routes answer so.
	APIKey string

	// WorkflowID is DIDIT_WORKFLOW_ID: registration (document + liveness).
	WorkflowID string

	// BiometricWorkflowID is DIDIT_BIOMETRIC_WORKFLOW_ID: every withdrawal (liveness
	// and face match against the registered face). Empty falls back to WorkflowID.
	BiometricWorkflowID string

	// WebhookSecret is DIDIT_WEBHOOK_SECRET. Optional: the webhook route never trusts
	// the body and always asks the provider for the decision.
	WebhookSecret string

	// CallbackURL is DIDIT_CALLBACK_URL: where the person's browser goes back to
	// when the hosted flow ends. The session id is appended as ?session_id=.
	CallbackURL string

	// SaldoInicialDemo is SALDO_INICIAL_DEMO_COP: what the virtual wallet of a person
	// receives when the registration is approved. Zero loads nothing.
	SaldoInicialDemo int64
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

	// InteractionServerURL is the interaction server of the authorization server
	// (http://rafiki-auth:3009), which is where the identity provider reads and
	// resolves an interaction: /grant/{id}/{nonce}. It is NOT the address a browser
	// opens, which is /interact/{id}/{nonce} on the interaction server, and calling
	// that one from the IdP answered 404.
	InteractionServerURL string

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
	Database Database
	Internal Internal
	Verifik  Verifik
	Didit    Didit
	Panel    Panel
}

// Load reads the environment and validates it. When a value cannot be used it
// returns an error naming the offending variable, so the process fails at start
// instead of at the first request.
func Load() (Config, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return Config{}, err
	}

	var saldoInicial int64
	if raw := strings.TrimSpace(os.Getenv("SALDO_INICIAL_DEMO_COP")); raw != "" {
		saldoInicial, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || saldoInicial < 0 {
			return Config{}, fmt.Errorf("config: SALDO_INICIAL_DEMO_COP must be a whole number of pesos, got %q", raw)
		}
	}

	rafiki := Rafiki{
		AdminURL:             defaultString(os.Getenv("RAFIKI_ADMIN_URL"), DefaultRafikiAdminURL),
		OpenPaymentsURL:      defaultString(os.Getenv("RAFIKI_OPEN_PAYMENTS_URL"), DefaultOpenPaymentsURL),
		TenantID:             strings.TrimSpace(os.Getenv("RAFIKI_TENANT_ID")),
		AdminAPISecret:       strings.TrimSpace(os.Getenv("RAFIKI_ADMIN_API_SECRET")),
		SignatureSecret:      strings.TrimSpace(os.Getenv("RAFIKI_SIGNATURE_SECRET")),
		IdentityServerSecret: strings.TrimSpace(os.Getenv("RAFIKI_IDENTITY_SERVER_SECRET")),
		AuthServerURL:        strings.TrimSpace(os.Getenv("RAFIKI_AUTH_SERVER_URL")),
		AuthServerPublicURL:  strings.TrimSpace(os.Getenv("RAFIKI_AUTH_SERVER_PUBLIC_URL")),
		InteractionServerURL: strings.TrimSpace(os.Getenv("RAFIKI_INTERACTION_SERVER_URL")),
	}

	// The wallet address of the client is the published base plus the application's
	// account, which is the name the seed registers the key on: deriving it keeps the
	// two from drifting apart when the base changes.
	clientWallet := strings.TrimSpace(os.Getenv("OP_CLIENT_WALLET_ADDRESS"))
	if clientWallet == "" {
		clientWallet = strings.TrimSuffix(rafiki.OpenPaymentsURL, "/") + "/app-cajadebarrio"
	}

	return Config{
		Addr:     defaultString(os.Getenv("HTTP_ADDR"), DefaultAddr),
		LogLevel: level,
		Rafiki:   rafiki,
		OP: OP{
			ClientWalletAddress: clientWallet,
			KeyID:               strings.TrimSpace(os.Getenv("OP_KEY_ID")),
			PrivateKeyPath:      defaultString(os.Getenv("OP_PRIVATE_KEY_PATH"), DefaultOPPrivateKeyPath),
		},
		Database: Database{
			URL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		},
		Internal: Internal{
			Token: strings.TrimSpace(os.Getenv("SERVICIO_TOKEN")),
		},
		Panel: Panel{
			Origenes:  listaDe(os.Getenv("PANEL_ORIGINS")),
			AgenteURL: strings.TrimSpace(os.Getenv("AGENTE_URL")),
			Claves: map[string]string{
				"superadmin":   strings.TrimSpace(os.Getenv("PANEL_CLAVE_SUPERADMIN")),
				"cumplimiento": strings.TrimSpace(os.Getenv("PANEL_CLAVE_CUMPLIMIENTO")),
				"soporte":      strings.TrimSpace(os.Getenv("PANEL_CLAVE_SOPORTE")),
			},
		},
		Verifik: Verifik{
			Token: strings.TrimSpace(os.Getenv("VERIFIK_TOKEN")),
		},
		Didit: Didit{
			APIKey:              strings.TrimSpace(os.Getenv("DIDIT_API_KEY")),
			WorkflowID:          strings.TrimSpace(os.Getenv("DIDIT_WORKFLOW_ID")),
			BiometricWorkflowID: strings.TrimSpace(os.Getenv("DIDIT_BIOMETRIC_WORKFLOW_ID")),
			WebhookSecret:       strings.TrimSpace(os.Getenv("DIDIT_WEBHOOK_SECRET")),
			CallbackURL:         strings.TrimSpace(os.Getenv("DIDIT_CALLBACK_URL")),
			SaldoInicialDemo:    saldoInicial,
		},
	}, nil
}

// HostRewrites is the map a client needs to make the authorization server reachable from inside
// the network: its continuation and token management URLs come back with the host the stack
// publishes for a browser, and a container cannot reach the host's published ports.
func (c Config) HostRewrites() map[string]string {
	publica, err := url.Parse(c.Rafiki.AuthServerPublicURL)
	if err != nil || publica.Host == "" {
		return nil
	}
	interna, err := url.Parse(c.Rafiki.AuthServerURL)
	if err != nil || interna.Host == "" || interna.Host == publica.Host {
		return nil
	}

	return map[string]string{publica.Host: interna.Host}
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

// listaDe splits a comma separated variable, dropping blanks and trailing slashes.
func listaDe(raw string) []string {
	var lista []string
	for _, valor := range strings.Split(raw, ",") {
		if valor = strings.TrimSuffix(strings.TrimSpace(valor), "/"); valor != "" {
			lista = append(lista, valor)
		}
	}

	return lista
}
