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

// The Rafiki settings are read but not required here: /health must answer before
// Rafiki is configured, and the component that needs the secret validates it.
func TestLoadLeavesRafikiUnsetWhenEnvironmentIsEmpty(t *testing.T) {
	t.Setenv("RAFIKI_ADMIN_URL", "")
	t.Setenv("RAFIKI_OPEN_PAYMENTS_URL", "")
	t.Setenv("RAFIKI_TENANT_ID", "")
	t.Setenv("RAFIKI_ADMIN_API_SECRET", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	want := config.Rafiki{
		AdminURL:        config.DefaultRafikiAdminURL,
		OpenPaymentsURL: config.DefaultOpenPaymentsURL,
	}
	if cfg.Rafiki != want {
		t.Errorf("Rafiki = %+v, want %+v", cfg.Rafiki, want)
	}
}

func TestLoadReadsRafikiSettingsFromEnvironment(t *testing.T) {
	t.Setenv("RAFIKI_ADMIN_URL", "http://rafiki.example:3001/graphql")
	t.Setenv("RAFIKI_OPEN_PAYMENTS_URL", "https://pay.example")
	t.Setenv("RAFIKI_TENANT_ID", "11111111-1111-4111-8111-111111111111")
	t.Setenv("RAFIKI_ADMIN_API_SECRET", "a-secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	want := config.Rafiki{
		AdminURL:        "http://rafiki.example:3001/graphql",
		OpenPaymentsURL: "https://pay.example",
		TenantID:        "11111111-1111-4111-8111-111111111111",
		AdminAPISecret:  "a-secret",
	}
	if cfg.Rafiki != want {
		t.Errorf("Rafiki = %+v, want %+v", cfg.Rafiki, want)
	}
}

func TestLoadDefaultsTheRafikiAdminURL(t *testing.T) {
	t.Setenv("RAFIKI_ADMIN_URL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if cfg.Rafiki.AdminURL != config.DefaultRafikiAdminURL {
		t.Errorf("AdminURL = %q, want %q", cfg.Rafiki.AdminURL, config.DefaultRafikiAdminURL)
	}
}

func TestLoadDefaultsTheOpenPaymentsURL(t *testing.T) {
	t.Setenv("RAFIKI_OPEN_PAYMENTS_URL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if cfg.Rafiki.OpenPaymentsURL != config.DefaultOpenPaymentsURL {
		t.Errorf("OpenPaymentsURL = %q, want %q", cfg.Rafiki.OpenPaymentsURL, config.DefaultOpenPaymentsURL)
	}
}

func TestLoadReadsTheOpenPaymentsClientSettings(t *testing.T) {
	t.Setenv("OP_CLIENT_WALLET_ADDRESS", "https://ase.example:3000/laura-cajadebarrio")
	t.Setenv("OP_KEY_ID", "mi-llave")
	t.Setenv("OP_PRIVATE_KEY_PATH", "/ruta/clave.pem")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	want := config.OP{ClientWalletAddress: "https://ase.example:3000/laura-cajadebarrio", KeyID: "mi-llave", PrivateKeyPath: "/ruta/clave.pem"}
	if cfg.OP != want {
		t.Errorf("OP = %+v, want %+v", cfg.OP, want)
	}
}

// The wallet address of the client is the published base plus the application's account
// when nobody names it, because that is the name the seed registers the key on:
// deriving it keeps the two from drifting apart when the base changes.
func TestLoadDerivesTheClientWalletAddressFromThePublishedBase(t *testing.T) {
	t.Setenv("OP_CLIENT_WALLET_ADDRESS", "")
	t.Setenv("RAFIKI_OPEN_PAYMENTS_URL", "https://ase.example:3000/")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if want := "https://ase.example:3000/app-cajadebarrio"; cfg.OP.ClientWalletAddress != want {
		t.Errorf("ClientWalletAddress = %q, want %q", cfg.OP.ClientWalletAddress, want)
	}
}

func TestLoadDefaultsTheOpenPaymentsPrivateKeyPath(t *testing.T) {
	t.Setenv("OP_PRIVATE_KEY_PATH", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if cfg.OP.PrivateKeyPath != config.DefaultOPPrivateKeyPath {
		t.Errorf("PrivateKeyPath = %q, want %q", cfg.OP.PrivateKeyPath, config.DefaultOPPrivateKeyPath)
	}
}

// The ASE side needs two more secrets: one to verify the signature Rafiki puts on
// every webhook, and one shared with the identity provider that approves
// interactive grants.
func TestLoadReadsTheAseSecrets(t *testing.T) {
	t.Setenv("RAFIKI_SIGNATURE_SECRET", "firma")
	t.Setenv("RAFIKI_IDENTITY_SERVER_SECRET", "identidad")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if cfg.Rafiki.SignatureSecret != "firma" {
		t.Errorf("SignatureSecret = %q, want %q", cfg.Rafiki.SignatureSecret, "firma")
	}
	if cfg.Rafiki.IdentityServerSecret != "identidad" {
		t.Errorf("IdentityServerSecret = %q, want %q", cfg.Rafiki.IdentityServerSecret, "identidad")
	}
}

// The identity provider needs the authorization server twice and the two are
// deliberately different: the address that resolves inside the compose network,
// and the one the user's browser opens. Neither works for the other's purpose,
// because the host's ports are unreachable from the containers.
func TestLoadReadsTheTwoAuthServerURLs(t *testing.T) {
	t.Setenv("RAFIKI_AUTH_SERVER_URL", "http://rafiki-auth:3006")
	t.Setenv("RAFIKI_AUTH_SERVER_PUBLIC_URL", "http://100.76.141.47:3006")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}
	if cfg.Rafiki.AuthServerURL != "http://rafiki-auth:3006" {
		t.Errorf("AuthServerURL = %q, want the address inside the network", cfg.Rafiki.AuthServerURL)
	}
	if cfg.Rafiki.AuthServerPublicURL != "http://100.76.141.47:3006" {
		t.Errorf("AuthServerPublicURL = %q, want the address the browser opens", cfg.Rafiki.AuthServerPublicURL)
	}
}
