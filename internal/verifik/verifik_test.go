package verifik

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// citizenFound is the real shape Verifik answers with, taken from its own
// documentation for GET /v2/co/cedula.
const citizenFound = `{
  "data": {
    "arrayName": ["JULIO", "CESAR", "CABANA", "CONTRERAS"],
    "documentNumber": "1121329662",
    "documentType": "CC",
    "firstName": "JULIO CESAR",
    "fullName": "JULIO CESAR CABANA CONTRERAS",
    "lastName": "CABANA CONTRERAS"
  },
  "signature": { "dateTime": "October 8, 2025 8:21 PM", "message": "Certified by Verifik.co" },
  "id": "I70IL"
}`

func TestVerifyDocumentQueriesTheProviderAndReturnsTheName(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, citizenFound)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)

	document, err := client.VerifyDocument(context.Background(), "CC", "1121329662")
	if err != nil {
		t.Fatalf("VerifyDocument() error = %v, want nil", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodGet)
	}
	if gotPath != "/v2/co/cedula" {
		t.Errorf("path = %q, want %q", gotPath, "/v2/co/cedula")
	}
	// The provider takes both values as query parameters, spelled exactly like this.
	if !strings.Contains(gotQuery, "documentType=CC") {
		t.Errorf("query = %q, want it to carry documentType=CC", gotQuery)
	}
	if !strings.Contains(gotQuery, "documentNumber=1121329662") {
		t.Errorf("query = %q, want it to carry documentNumber=1121329662", gotQuery)
	}
	if want := "Bearer test-token"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want %q", gotAccept, "application/json")
	}

	if !document.Valid {
		t.Error("Valid = false, want true when the provider answers with civil data")
	}
	if want := "JULIO CESAR CABANA CONTRERAS"; document.FullName != want {
		t.Errorf("FullName = %q, want %q", document.FullName, want)
	}
}

func TestVerifyDocumentReportsAMissingDocumentAsNotValid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Verifik answers 404 when the document is not in the registry. That is a
		// business outcome the caller reports as valid:false, not a provider
		// failure it would turn into a 502.
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)

	document, err := client.VerifyDocument(context.Background(), "CC", "0000000000")
	if err != nil {
		t.Fatalf("VerifyDocument() error = %v, want nil for a missing document", err)
	}
	if document.Valid {
		t.Error("Valid = true, want false for a missing document")
	}
	if document.FullName != "" {
		t.Errorf("FullName = %q, want empty", document.FullName)
	}
}

func TestVerifyDocumentRejectsAnInvalidDocumentWithoutCallingTheProvider(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cases := map[string]struct {
		documentType   string
		documentNumber string
	}{
		"document type the provider does not accept": {documentType: "NIT", documentNumber: "1121329662"},
		"empty document type":                        {documentType: "", documentNumber: "1121329662"},
		"number below the accepted length":           {documentType: "CC", documentNumber: "1234"},
		"number above the accepted length":           {documentType: "CC", documentNumber: "12345678901"},
		"number with spaces":                         {documentType: "CC", documentNumber: "10361 23456"},
		"number with periods":                        {documentType: "CC", documentNumber: "103.612"},
		"empty number":                               {documentType: "CC", documentNumber: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := newTestClient(t, srv.URL)

			_, err := client.VerifyDocument(context.Background(), tc.documentType, tc.documentNumber)
			if !errors.Is(err, ErrInvalidDocument) {
				t.Fatalf("VerifyDocument(%q, %q) error = %v, want ErrInvalidDocument",
					tc.documentType, tc.documentNumber, err)
			}
		})
	}

	// The rule the provider documents is a local format rule, so the bad value
	// never leaves the process: no request, no token spent, no rate-limit budget.
	if called {
		t.Error("the provider was called with an invalid document, want zero requests")
	}
}

func TestVerifyDocumentReportsAProviderFailureWithoutLeakingTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"invalid token"}`)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)

	_, err := client.VerifyDocument(context.Background(), "CC", "1121329662")
	if err == nil {
		t.Fatal("VerifyDocument() error = nil, want a provider failure on 401")
	}

	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want a *verifik.Error", err)
	}
	if providerErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want %d", providerErr.StatusCode, http.StatusUnauthorized)
	}
	// The token is the only secret this client holds. An error message that
	// carries it ends up in logs and in the agent's reply.
	if strings.Contains(err.Error(), "test-token") {
		t.Errorf("error %q leaks the token", err.Error())
	}
}

func TestNewRefusesAnEmptyToken(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("New(\"\") error = nil, want a failure naming the missing token")
	}
	if _, err := New("   "); err == nil {
		t.Error("New(\"   \") error = nil, want a failure for a blank token")
	}
}
