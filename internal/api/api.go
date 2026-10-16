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

// The two sentences this package answers when the router, not a handler, is the
// one refusing.
const (
	pathNotFoundMessage = "Esa ruta no existe en la superficie interna."
	methodNotAllowedMsg = "Esa ruta no acepta ese método."
)

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
// relative to the surface, so "/personas" answers at /internal/personas and a
// pattern registered twice makes the process fail at startup, which is what such
// a programming error deserves.
//
// This is the only supported way in. Registering a route like
// "/internal/algo" on the caller's own mux instead would bypass the token check,
// because a more specific pattern wins over the subtree this package mounts.
func (s *Server) Handle(pattern string, handler http.HandlerFunc) {
	s.mux.HandleFunc(pattern, handler)
}

// Register mounts the surface on a mux the caller owns, after the caller's own
// routes, so the backend keeps one router.
func (s *Server) Register(mux *http.ServeMux) {
	// The base without its trailing slash is registered too. ServeMux would
	// otherwise answer its own redirect for it before the guard ran, which is a
	// request that reached the surface without passing the check. It is not a
	// route, so a caller that does carry the token gets the envelope of an unknown
	// path.
	mux.Handle(internalBase, s.authenticate(http.HandlerFunc(s.refuseBase)))
	mux.Handle(internalRoot, s.Handler())
}

// Handler returns the surface ready to mount: the token checked, the base
// stripped, and the router's own failures dressed in the contract envelope.
func (s *Server) Handler() http.Handler {
	// The base is stripped before the surface sees the request, so a route is
	// registered relative to the surface and this package stays the only place that
	// knows /internal. ServeMux does not strip it on its own.
	return s.authenticate(envelopeErrors(http.StripPrefix(internalBase, s.mux)))
}

// Logger returns the logger the surface writes to.
func (s *Server) Logger() *slog.Logger {
	return s.logger
}

// refuseBase answers a request for the base itself, which is not a route.
func (s *Server) refuseBase(w http.ResponseWriter, _ *http.Request) {
	Fail(w, Errorf(CodeNoEncontrado, pathNotFoundMessage))
}

// authenticate refuses every request that does not carry the service token.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A byte by byte comparison stops at the first difference, which leaks how
		// much of a guess was right and lets the token be recovered one byte at a
		// time. ConstantTimeCompare removes that leak. The length of the token stays
		// observable through the comparison, which is inherent and accepted here:
		// the token has a fixed length chosen by us, not by an attacker.
		received := []byte(r.Header.Get(ServiceTokenHeader))
		if subtle.ConstantTimeCompare(received, []byte(s.serviceToken)) != 1 {
			// The token never reaches the log: the path and the caller are what a
			// maintainer needs to see a caller that is misconfigured.
			s.logger.Warn("rejected a call to the internal surface",
				"path", r.URL.Path, "remote", r.RemoteAddr)

			Fail(w, Errorf(CodeNoAutorizado,
				"La llamada no está autorizada: falta el token de servicio o no coincide."))

			return
		}

		next.ServeHTTP(w, r)
	})
}

// envelopeErrors dresses the two failures the router generates on its own -- the
// 404 of a path that is not a route and the 405 of a method a route does not take
// -- in the body the contract fixes. The agent parses that body, and the router's
// own plain text leaves it with nothing to explain to a person.
func envelopeErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&envelopeWriter{ResponseWriter: w}, r)
	})
}

// envelopeWriter replaces the router's own failure responses and passes every
// other response through untouched.
type envelopeWriter struct {
	http.ResponseWriter
	dressed bool
}

func (w *envelopeWriter) WriteHeader(status int) {
	if w.dressed {
		return
	}

	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		w.ResponseWriter.WriteHeader(status)

		return
	}
	// A handler that classified its own failure already set the JSON content type,
	// so it is left alone.
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		w.ResponseWriter.WriteHeader(status)

		return
	}

	w.dressed = true
	if status == http.StatusMethodNotAllowed {
		writeEnvelope(w.ResponseWriter, CodeMetodoNoPermitido, methodNotAllowedMsg)

		return
	}
	writeEnvelope(w.ResponseWriter, CodeNoEncontrado, pathNotFoundMessage)
}

func (w *envelopeWriter) Write(body []byte) (int, error) {
	if w.dressed {
		// The router's own text is dropped: the envelope is already out.
		return len(body), nil
	}

	return w.ResponseWriter.Write(body)
}

// JSON writes a body with its status. The contract fixes snake_case keys and the
// agent parses the reply, so the body is a caller's struct with its own tags.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already written, so the only thing left is a log.
		slog.Error("the internal surface could not encode its reply", "error", err)
	}
}
