package httpserver_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/httpserver"
)

func newServer() *httpserver.Server {
	return httpserver.New(config.Config{Addr: "127.0.0.1:0", LogLevel: config.LevelInfo})
}

// Docker and the Rafiki compose healthchecks depend on this endpoint (S3, S7).
func TestHealthEndpointReportsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /health Content-Type = %q, want an application/json media type", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /health body %q is not JSON: %v", rec.Body.String(), err)
	}
	if want := map[string]string{"status": "ok"}; !reflect.DeepEqual(body, want) {
		t.Errorf("GET /health body = %v, want exactly %v", body, want)
	}
}

func TestHealthEndpointRejectsNonGET(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /health status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestUnknownRouteReturns404(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
