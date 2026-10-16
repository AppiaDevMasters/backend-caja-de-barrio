package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "token-de-servicio-de-prueba"

func newTestServer(t *testing.T) *Server {
	t.Helper()

	// A logger that discards keeps the run readable; the log line for a rejected
	// call is proven where it belongs, in the behaviour of the surface.
	server, err := New(Config{
		ServiceToken: testToken,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	return server
}

// mounted registers one route and wires the surface into a router the same way
// the process does, so the tests exercise the real mounting.
func mounted(t *testing.T, handler http.HandlerFunc) *http.ServeMux {
	t.Helper()

	server := newTestServer(t)
	server.Handle("GET /personas", handler)

	mux := http.NewServeMux()
	server.Register(mux)

	return mux
}

// withToken performs one request against the mounted surface.
func withToken(mux *http.ServeMux, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set(ServiceTokenHeader, token)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)

	return recorder
}

// envelope is the body shape the contract fixes for every failure.
type envelope struct {
	Error struct {
		Codigo  string `json:"codigo"`
		Mensaje string `json:"mensaje"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) envelope {
	t.Helper()

	var body envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not the contract envelope: %v (body %q)", err, recorder.Body.String())
	}

	return body
}

func TestNewRefusesAnEmptyServiceToken(t *testing.T) {
	// Without the token every request would be rejected at runtime, so the
	// process refuses to start instead of serving a surface nobody can call.
	for _, token := range []string{"", "   "} {
		if _, err := New(Config{ServiceToken: token}); err == nil {
			t.Errorf("New(%q) error = nil, want a failure naming SERVICIO_TOKEN", token)
		}
	}
}

func TestTheSurfaceLivesUnderInternalAndNeedsTheToken(t *testing.T) {
	called := false
	mux := mounted(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		JSON(w, http.StatusOK, map[string]string{"nombre": "Laura"})
	})

	recorder := withToken(mux, "/internal/personas", testToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %q)", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !called {
		t.Error("the handler was not reached with the right token")
	}
	if want := `{"nombre":"Laura"}`; !strings.Contains(recorder.Body.String(), want) {
		t.Errorf("body = %q, want it to carry %s", recorder.Body.String(), want)
	}
}

func TestTheSurfaceIsNotReachableOutsideInternal(t *testing.T) {
	// The contract fixes the base as /internal and the surface is not public, so
	// the route must not answer anywhere else.
	mux := mounted(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the handler was reached outside /internal")
	})

	recorder := withToken(mux, "/personas", testToken)
	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d outside /internal", recorder.Code, http.StatusNotFound)
	}
}

func TestEveryRequestNeedsTheServiceToken(t *testing.T) {
	cases := map[string]string{
		"no header":                     "",
		"another token":                 "otro-token-distinto",
		"a prefix of the real one":      testToken[:len(testToken)-1],
		"the real one plus a character": testToken + "x",
		"the token in another case":     strings.ToUpper(testToken),
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			mux := mounted(t, func(w http.ResponseWriter, _ *http.Request) {
				t.Error("the handler was reached without the right token")
			})

			recorder := withToken(mux, "/internal/personas", token)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
			}

			body := decodeEnvelope(t, recorder)
			if body.Error.Codigo != string(CodeNoAutorizado) {
				t.Errorf("codigo = %q, want %q", body.Error.Codigo, CodeNoAutorizado)
			}
			if body.Error.Mensaje == "" {
				t.Error("mensaje is empty, but the agent relays it to a person")
			}
			// The token must never travel back in the reply.
			if strings.Contains(recorder.Body.String(), testToken) {
				t.Errorf("the reply leaks the token: %s", recorder.Body.String())
			}
		})
	}
}

func TestTheRejectionBodyIsExactlyTheContractEnvelope(t *testing.T) {
	mux := mounted(t, func(http.ResponseWriter, *http.Request) {})

	recorder := withToken(mux, "/internal/personas", "")

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	// One key at the top and two inside it: an extra field would break the agent
	// that parses this body.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &top); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if len(top) != 1 {
		t.Errorf("the body carries %d top level keys, want 1: %s", len(top), recorder.Body.String())
	}

	var inner map[string]json.RawMessage
	if err := json.Unmarshal(top["error"], &inner); err != nil {
		t.Fatalf(`the "error" member is not an object: %v`, err)
	}
	if len(inner) != 2 {
		t.Errorf(`"error" carries %d keys, want exactly codigo and mensaje: %s`, len(inner), recorder.Body.String())
	}
	if _, found := inner["codigo"]; !found {
		t.Error(`"error" has no codigo`)
	}
	if _, found := inner["mensaje"]; !found {
		t.Error(`"error" has no mensaje`)
	}
}
