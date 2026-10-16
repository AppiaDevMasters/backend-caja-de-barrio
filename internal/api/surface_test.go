package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// assertEnvelope pins the pair the contract fixes: one status and the body that
// goes with it, with both fields filled.
func assertEnvelope(t *testing.T, recorder *httptest.ResponseRecorder, status int, code Code) {
	t.Helper()

	if recorder.Code != status {
		t.Errorf("status = %d, want %d (body %q)", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	body := decodeEnvelope(t, recorder)
	if body.Error.Codigo != string(code) {
		t.Errorf("codigo = %q, want %q", body.Error.Codigo, code)
	}
	if strings.TrimSpace(body.Error.Mensaje) == "" {
		t.Error("mensaje is empty, but the agent reads it to a person")
	}
}

func request(t *testing.T, mux *http.ServeMux, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set(ServiceTokenHeader, token)
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)

	return recorder
}

func TestTheBareBaseNeedsTheTokenToo(t *testing.T) {
	// The contract has no exception: a request without the token is 401. ServeMux
	// would answer its own redirect for the bare base before the guard runs, which
	// is a request that reached the surface without passing the check.
	mux := mounted(t, func(http.ResponseWriter, *http.Request) {})

	assertEnvelope(t, request(t, mux, http.MethodGet, "/internal", ""),
		http.StatusUnauthorized, CodeNoAutorizado)
}

func TestTheBareBaseIsNotARoute(t *testing.T) {
	mux := mounted(t, func(http.ResponseWriter, *http.Request) {})

	assertEnvelope(t, request(t, mux, http.MethodGet, "/internal", testToken),
		http.StatusNotFound, CodeNoEncontrado)
}

func TestAnUnknownRouteInsideTheSurfaceAnswersTheEnvelope(t *testing.T) {
	// Four MCPs will call this surface and one of them will get a path wrong. The
	// contract fixes the body of every 4xx, and the router's own plain text 404 is
	// not that body: the agent would have nothing to explain.
	mux := mounted(t, func(http.ResponseWriter, *http.Request) {})

	assertEnvelope(t, request(t, mux, http.MethodGet, "/internal/no-existe", testToken),
		http.StatusNotFound, CodeNoEncontrado)
}

func TestAWrongMethodInsideTheSurfaceAnswersTheEnvelope(t *testing.T) {
	mux := mounted(t, func(http.ResponseWriter, *http.Request) {})

	recorder := request(t, mux, http.MethodPost, "/internal/personas", testToken)
	if recorder.Code < 400 {
		t.Fatalf("status = %d, want a failure for a method the route does not take", recorder.Code)
	}

	// The contract fixes no status for a method that does not belong to a route,
	// so what this pins is that the body is still the envelope the agent parses.
	body := decodeEnvelope(t, recorder)
	if body.Error.Codigo == "" || body.Error.Mensaje == "" {
		t.Errorf("the method failure carries no envelope: %q", recorder.Body.String())
	}
}

func TestTheTokenIsComparedAsItArrives(t *testing.T) {
	// A token with padding is not the token. Without this the configured value
	// could be trimmed while the received one is not, and the two would disagree
	// in a way nobody could see.
	cases := map[string]string{
		"a padded token":         " " + testToken + " ",
		"a token with a newline": testToken + "\n",
		"a token with a tab":     "\t" + testToken,
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			mux := mounted(t, func(http.ResponseWriter, *http.Request) {
				t.Error("the handler was reached with a token that is not the token")
			})

			assertEnvelope(t, request(t, mux, http.MethodGet, "/internal/personas", token),
				http.StatusUnauthorized, CodeNoAutorizado)
		})
	}
}

func TestARouteRegisteredAfterRegisterIsStillGuarded(t *testing.T) {
	// The domain packages register while the process wires itself up, and the
	// order of that wiring must not decide whether a route is protected.
	server := newTestServer(t)
	mux := http.NewServeMux()
	server.Register(mux)

	server.Handle("GET /tarde", func(http.ResponseWriter, *http.Request) {
		t.Error("the handler was reached without the token")
	})

	assertEnvelope(t, request(t, mux, http.MethodGet, "/internal/tarde", ""),
		http.StatusUnauthorized, CodeNoAutorizado)
}

func TestAnInventedCodeNeverReachesTheAgent(t *testing.T) {
	// A typo in a caller would otherwise travel to the agent verbatim, and from
	// there to a person, as a code nobody can look up.
	recorder := httptest.NewRecorder()

	Fail(recorder, Errorf(Code("CODIGO_QUE_NO_EXISTE"), "detalle interno de la ruta"))

	body := decodeEnvelope(t, recorder)
	if body.Error.Codigo == "CODIGO_QUE_NO_EXISTE" {
		t.Error("an invented code reached the body the agent reads")
	}
	if !strings.Contains(recorder.Body.String(), string(CodeInterno)) {
		t.Errorf("codigo = %q, want the class this package answers when it cannot classify", body.Error.Codigo)
	}
	if strings.Contains(recorder.Body.String(), "detalle interno de la ruta") {
		t.Errorf("the invented code's message reached the body: %s", recorder.Body.String())
	}
}

func TestFailSurvivesATypedNilError(t *testing.T) {
	// A (*Error)(nil) satisfies errors.As, and dereferencing it would crash the
	// process that answers the whole surface.
	recorder := httptest.NewRecorder()

	Fail(recorder, (*Error)(nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	body := decodeEnvelope(t, recorder)
	if body.Error.Mensaje == "" {
		t.Error("mensaje is empty after a typed nil error")
	}
}

func TestATypedNilErrorCanBeFormatted(t *testing.T) {
	// The same nil receiver is reachable from any caller that formats the error,
	// not only from Fail.
	var err error = (*Error)(nil)

	if got := err.Error(); got != "" {
		t.Errorf("(*Error)(nil).Error() = %q, want the empty string", got)
	}
	if got := errorText(err); got != "" {
		t.Errorf("errorText((*Error)(nil)) = %q, want the empty string", got)
	}
	if err == nil {
		t.Error("a typed nil compares equal to nil, which is the trap this guards")
	}
}
