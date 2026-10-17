package ase_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/ase"
)

const (
	testIdPSecret        = "an-idp-secret"
	testAuthServerPublic = "https://auth.example"
	testDetailsBody      = `{"grantId":"grant-1","state":"pending","access":[{"type":"outgoing-payment","identifier":"https://ase.example/tienda","limits":{"debitAmount":"50000"}}]}`
)

// authCall is one request the IdP made to the authorization server.
type authCall struct {
	Method    string
	Path      string
	IDPSecret string
}

// interactionStub is the authorization server the IdP talks to, server to server.
func interactionStub(t *testing.T, detailsCode, choiceCode int) (*httptest.Server, func() []authCall) {
	t.Helper()

	var (
		mu    sync.Mutex
		calls []authCall
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, authCall{Method: r.Method, Path: r.URL.Path, IDPSecret: r.Header.Get("x-idp-secret")})
		mu.Unlock()

		// The BACK-CHANNEL API of the authorization server, /grant/{id}/{nonce}: reading the
		// grant is a GET and resolving the choice is a POST.
		if r.Method == http.MethodGet {
			w.WriteHeader(detailsCode)
			if detailsCode == http.StatusOK {
				w.Write([]byte(testDetailsBody))
			}
			return
		}

		// The authorization server answers the choice with 202.
		w.WriteHeader(choiceCode)
	}))
	t.Cleanup(srv.Close)

	return srv, func() []authCall {
		mu.Lock()
		defer mu.Unlock()

		return append([]authCall(nil), calls...)
	}
}

func newIdPHandler(t *testing.T, authServerURL string) http.Handler {
	t.Helper()

	srv, err := ase.New(ase.Config{
		SignatureSecret:      testSecret,
		IdentityServerSecret: testIdPSecret,
		AuthServerURL:        authServerURL,
		AuthServerPublicURL:  testAuthServerPublic,
		// The IdP reads and resolves the interaction on the interaction server: the same stub
		// plays both addresses here.
		InteractionServerURL: authServerURL,
		Now:                  func() time.Time { return pinned },
	})
	if err != nil {
		t.Fatalf("ase.New() returned an error: %v", err)
	}

	return srv.Handler()
}

// consentQuery is what the authorization server puts in the redirect it sends to
// the identity provider.
const consentQuery = "?interactId=i-1&nonce=n-1&clientName=Tienda+Don+Jaime&clientUri=https%3A%2F%2Ftienda.example"

func getIdP(t *testing.T, handler http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/idp/"+query, nil))

	return rec
}

func postIdP(t *testing.T, handler http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/idp/approve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// The screen has to show what is being approved, and it must never carry the
// secret the IdP shares with the authorization server.
func TestIdPPageShowsWhatTheClientAsksFor(t *testing.T) {
	srv, calls := interactionStub(t, http.StatusOK, http.StatusAccepted)

	rec := getIdP(t, newIdPHandler(t, srv.URL), consentQuery)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%q", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{"Tienda Don Jaime", "outgoing-payment", "debitAmount", "50000", `name="interactId"`, "i-1", `name="nonce"`, "n-1"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not show %q", want)
		}
	}
	if strings.Contains(body, testIdPSecret) {
		t.Error("the page carries the IdP secret, which only the authorization server may see")
	}

	got := calls()
	if len(got) != 1 {
		t.Fatalf("the authorization server received %d calls, want 1: %+v", len(got), got)
	}
	if got[0].Method != http.MethodGet || got[0].Path != "/grant/i-1/n-1" {
		t.Errorf("call = %s %s, want GET /grant/i-1/n-1 (la servidor de interaccion, no la ruta del navegador)", got[0].Method, got[0].Path)
	}
	if got[0].IDPSecret != testIdPSecret {
		t.Errorf("x-idp-secret = %q, want the identity server secret", got[0].IDPSecret)
	}
}

// A request without an interaction cannot be answered, and it must not reach the
// authorization server.
func TestIdPPageRejectsARequestWithoutAnInteraction(t *testing.T) {
	tests := map[string]string{
		"sin parametros":   "",
		"sin interactId":   "?nonce=n-1",
		"sin nonce":        "?interactId=i-1",
		"interactId vacio": "?interactId=&nonce=n-1",
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			srv, calls := interactionStub(t, http.StatusOK, http.StatusAccepted)

			rec := getIdP(t, newIdPHandler(t, srv.URL), query)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: body=%q", rec.Code, rec.Body.String())
			}
			if len(calls()) != 0 {
				t.Errorf("the authorization server was called %d times, want none", len(calls()))
			}
		})
	}
}

// When the authorization server refuses the interaction there is nothing to
// approve, and saying so is a gateway problem, not a client one.
func TestIdPPageReportsAnInteractionTheAuthServerRefuses(t *testing.T) {
	srv, _ := interactionStub(t, http.StatusNotFound, http.StatusAccepted)

	rec := getIdP(t, newIdPHandler(t, srv.URL), consentQuery)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: body=%q", rec.Code, rec.Body.String())
	}
}

// An IdP without an authorization server to talk to cannot approve anything.
func TestIdPPageWithoutAnAuthServerIsUnavailable(t *testing.T) {
	rec := getIdP(t, newIdPHandler(t, ""), consentQuery)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503: body=%q", rec.Code, rec.Body.String())
	}
}

// Approving asks the authorization server for the choice and then sends the
// browser to finish the interaction there: the hash of the callback is the
// authorization server's, not the IdP's.
func TestIdPChoiceAsksTheAuthServerAndSendsTheBrowserToFinish(t *testing.T) {
	tests := map[string]string{"accept": "/grant/i-1/n-1/accept", "reject": "/grant/i-1/n-1/reject"}

	for choice, wantPath := range tests {
		t.Run(choice, func(t *testing.T) {
			srv, calls := interactionStub(t, http.StatusOK, http.StatusAccepted)

			rec := postIdP(t, newIdPHandler(t, srv.URL), url.Values{
				"interactId": {"i-1"},
				"nonce":      {"n-1"},
				"choice":     {choice},
			})

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303: body=%q", rec.Code, rec.Body.String())
			}
			wantLocation := testAuthServerPublic + "/interact/i-1/n-1/finish"
			if got := rec.Header().Get("Location"); got != wantLocation {
				t.Errorf("Location = %q, want %q", got, wantLocation)
			}

			got := calls()
			if len(got) != 1 {
				t.Fatalf("the authorization server received %d calls, want 1: %+v", len(got), got)
			}
			if got[0].Method != http.MethodPost || got[0].Path != wantPath {
				t.Errorf("call = %s %s, want POST %s", got[0].Method, got[0].Path, wantPath)
			}
			if got[0].IDPSecret != testIdPSecret {
				t.Errorf("x-idp-secret = %q, want the identity server secret", got[0].IDPSecret)
			}
		})
	}
}

// The browser reaches the authorization server by its public address, which is not
// the one the IdP uses to talk to it.
func TestIdPChoiceRedirectsToThePublicAuthServerByDefault(t *testing.T) {
	authServer, _ := interactionStub(t, http.StatusOK, http.StatusAccepted)

	srv, err := ase.New(ase.Config{
		SignatureSecret:      testSecret,
		IdentityServerSecret: testIdPSecret,
		AuthServerURL:        authServer.URL,
		// The IdP reads and resolves the interaction on the interaction server, which is a
		// different address than the browser's.
		InteractionServerURL: authServer.URL,
		Now:                  func() time.Time { return pinned },
	})
	if err != nil {
		t.Fatalf("ase.New() returned an error: %v", err)
	}

	rec := postIdP(t, srv.Handler(), url.Values{"interactId": {"i-1"}, "nonce": {"n-1"}, "choice": {"accept"}})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), authServer.URL+"/interact/i-1/n-1/finish"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestIdPChoiceRejectsAnUnknownChoiceAndAMissingInteraction(t *testing.T) {
	tests := map[string]url.Values{
		"otra eleccion":    {"interactId": {"i-1"}, "nonce": {"n-1"}, "choice": {"maybe"}},
		"sin eleccion":     {"interactId": {"i-1"}, "nonce": {"n-1"}},
		"sin interaccion":  {"choice": {"accept"}},
		"interactId vacio": {"interactId": {""}, "nonce": {"n-1"}, "choice": {"accept"}},
	}

	for name, form := range tests {
		t.Run(name, func(t *testing.T) {
			srv, calls := interactionStub(t, http.StatusOK, http.StatusAccepted)

			rec := postIdP(t, newIdPHandler(t, srv.URL), form)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: body=%q", rec.Code, rec.Body.String())
			}
			if len(calls()) != 0 {
				t.Errorf("the authorization server was called %d times, want none", len(calls()))
			}
		})
	}
}

func TestIdPChoiceReportsAnAuthServerThatRefuses(t *testing.T) {
	srv, _ := interactionStub(t, http.StatusOK, http.StatusBadRequest)

	rec := postIdP(t, newIdPHandler(t, srv.URL), url.Values{"interactId": {"i-1"}, "nonce": {"n-1"}, "choice": {"accept"}})

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: body=%q", rec.Code, rec.Body.String())
	}
}
