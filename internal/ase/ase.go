// Package ase implements the endpoints Rafiki calls on the Caja de Barrio
// backend: the webhook receiver, the exchange rates the resource server asks for
// and the identity provider (IdP) that approves interactive grants.
//
// Each one is authenticated with a different shared secret, and none of them is
// the Admin API signature scheme: a webhook carries a Rafiki-Signature header
// computed over the canonical event body.
package ase

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// DefaultMaxSignatureAge bounds how old a webhook signature may be. Rafiki signs
// every retry again, so the window only has to absorb the delay of one delivery.
const DefaultMaxSignatureAge = 5 * time.Minute

// DefaultSignatureVersion is the SIGNATURE_VERSION of the ASE when the caller
// does not set one (rafiki/docker-compose.yml pins it to '1').
const DefaultSignatureVersion = 1

// requestTimeout bounds one call to the authorization server.
const requestTimeout = 10 * time.Second

// Config holds what the ASE endpoints need.
type Config struct {
	// SignatureSecret is SIGNATURE_SECRET, the key of the Rafiki-Signature
	// header of every webhook. Without it the receiver refuses every event.
	SignatureSecret string

	// SignatureVersion is the SIGNATURE_VERSION of the ASE.
	SignatureVersion int

	// IdentityServerSecret is IDENTITY_SERVER_SECRET, the secret the IdP shares
	// with the authorization server through the x-idp-secret header.
	IdentityServerSecret string

	// AuthServerURL is the authorization server as the IdP reaches it, server to
	// server: inside the stack network that is the service name (rafiki-auth:3006),
	// not the address a browser opens.
	AuthServerURL string

	// AuthServerPublicURL is the address of that same server a browser can open. It
	// is only used for the redirect that finishes the interaction. Empty means
	// AuthServerURL, which is what a backend running on the host wants.
	AuthServerPublicURL string

	// MaxSignatureAge overrides DefaultMaxSignatureAge when it is positive.
	MaxSignatureAge time.Duration

	// Now and Logger are optional; New substitutes the defaults.
	Now    func() time.Time
	Logger *slog.Logger
}

// Server owns the ASE routes.
type Server struct {
	cfg        Config
	logger     *slog.Logger
	mux        *http.ServeMux
	httpClient *http.Client
}

// New builds a Server with its routes registered.
func New(cfg Config) *Server {
	if cfg.SignatureVersion == 0 {
		cfg.SignatureVersion = DefaultSignatureVersion
	}
	if cfg.MaxSignatureAge <= 0 {
		cfg.MaxSignatureAge = DefaultMaxSignatureAge
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}

	s := &Server{
		cfg:        cfg,
		logger:     logger,
		mux:        http.NewServeMux(),
		httpClient: &http.Client{Timeout: requestTimeout},
	}
	s.Register(s.mux)

	return s
}

// Register adds the ASE routes to a mux the caller owns, so the backend keeps a
// single router and the caller keeps the health endpoint.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /webhooks", s.handleWebhook)
	mux.HandleFunc("GET /rates", s.handleRates)
	mux.HandleFunc("GET /idp/", s.handleIDPPage)
	mux.HandleFunc("POST /idp/approve", s.handleIDPChoice)
}

// writeJSON answers with a JSON body, and reports the encoding failure the only
// way left once the status line is written: a log line.
func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Error("could not encode the JSON response", "error", err)
	}
}

// Handler returns the router. Tests exercise the routes through it.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Logger returns the structured logger the handlers write to.
func (s *Server) Logger() *slog.Logger {
	return s.logger
}
