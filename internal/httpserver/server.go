// Package httpserver exposes the backend HTTP surface: the health endpoint that
// Docker and the compose healthchecks wait on, plus the endpoints Rafiki calls
// (webhooks, exchange rates and the IdP) added by later tasks.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
)

// shutdownTimeout bounds how long the server waits for in-flight requests.
const shutdownTimeout = 10 * time.Second

// Server owns the routes and the HTTP listener.
type Server struct {
	cfg    config.Config
	logger *slog.Logger
	mux    *http.ServeMux
}

// New builds a Server with its routes registered. It does not open the
// listener; call Run for that.
func New(cfg config.Config) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel.Slog(),
	}))

	s := &Server{cfg: cfg, logger: logger, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /health", s.handleHealth)

	return s
}

// Handler returns the router. Tests exercise the routes through it.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Logger returns the structured logger the handlers write to.
func (s *Server) Logger() *slog.Logger {
	return s.logger
}

// Run serves requests until ctx is cancelled and then drains gracefully.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		s.logger.Info("http server listening", "addr", s.cfg.Addr)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	s.logger.Info("http server stopped")
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already written, so the only thing left is a log.
		slog.Error("could not encode the JSON response", "error", err)
	}
}
