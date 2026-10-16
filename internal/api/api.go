// Package api owns the /internal surface the four MCPs call: the router, the
// service token check every request passes, and the error envelope the agent
// parses.
//
// It is a library, not a service: the caller mounts it on its own router and the
// domain packages register their routes here, so this package holds no knowledge
// of personas, tiendas, reservas or pagos.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// ServiceTokenHeader is the header the contract fixes for service to service
// authentication.
const ServiceTokenHeader = "X-Servicio-Token"

// internalBase is the path the contract fixes. The surface is not public: it is
// reachable only from inside the compose network.
const internalBase = "/internal"

// internalRoot is the subtree pattern that captures everything under the base.
const internalRoot = internalBase + "/"

// Config holds what the surface needs.
type Config struct {
	// ServiceToken is SERVICIO_TOKEN. Every request under /internal carries it in
	// the ServiceTokenHeader, and without it the answer is 401.
	ServiceToken string

	// Logger is optional; New substitutes the default.
	Logger *slog.Logger
}

// Server owns the /internal surface.
type Server struct {
	serviceToken string
	logger       *slog.Logger
	mux          *http.ServeMux
}

// New validates the settings and returns a Server with its surface ready. An
// empty token fails here, at startup, instead of leaving a surface that refuses
// every call.
func New(cfg Config) (*Server, error) {
	// The configured value is trimmed so a stray space in the environment does
	// not silently change the token; the value that arrives is compared as it
	// arrives, because a token with padding is not the token.
	token := strings.TrimSpace(cfg.ServiceToken)
	if token == "" {
		return nil, errors.New("api: the service token is empty (SERVICIO_TOKEN)")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}

	return &Server{serviceToken: token, logger: logger, mux: http.NewServeMux()}, nil
}

// Handle registers one route of the surface. The pattern is a ServeMux pattern
// relative to /internal, so "/personas" answers at /internal/personas and a
// pattern registered twice makes the process fail at startup, which is what such
// a programming error deserves.
func (s *Server) Handle(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// Register mounts the surface on a mux the caller owns, after the caller's own
// routes, so the backend keeps one router.
func (s *Server) Register(mux *http.ServeMux) {
	// The base is stripped before the surface sees the request, so a route is
	// registered relative to the surface -- the domain packages write "/personas",
	// not "/internal/personas" -- and this function stays the only place that
	// knows the base. ServeMux does not strip it on its own.
	mux.Handle(internalRoot, s.authenticate(http.StripPrefix(internalBase, s.mux)))
}

// Handler returns the surface with the service token check in front of it.
func (s *Server) Handler() http.Handler {
	return s.authenticate(s.mux)
}

// Logger returns the logger the surface writes to.
func (s *Server) Logger() *slog.Logger {
	return s.logger
}

// authenticate refuses every request that does not carry the service token.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A byte by byte comparison stops at the first difference, which leaks how
		// much of a guess was right and lets the token be recovered one byte at a
		// time.
		received := []byte(r.Header.Get(ServiceTokenHeader))
		if subtle.ConstantTimeCompare(received, []byte(s.serviceToken)) != 1 {
			// The token never reaches the log: the path and the caller are what a
			// maintainer needs to see a caller that is misconfigured.
			s.logger.Warn("rejected a call to the internal surface",
				"path", r.URL.Path, "remote", r.RemoteAddr)

			Fail(w, Errorf(CodeNoAutorizado,
				"La llamada no esta autorizada: falta el token de servicio o no coincide."))

			return
		}

		next.ServeHTTP(w, r)
	})
}

// JSON writes a body with its status. The contract fixes snake_case keys and the
// agent parses the reply, so the body is the caller's struct with its own tags.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already written, so the only thing left is a log.
		slog.Error("the internal surface could not encode its reply", "error", err)
	}
}
