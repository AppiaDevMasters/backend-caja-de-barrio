package didit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sessionCreated is the real shape Didit answers with for POST /v3/session/,
// taken from its own documentation. The hosted URL field is "url", not
// "verification_url": the documentation calls that out explicitly.
const sessionCreated = `{
  "session_id": "4c5c7f3a-1f82-4f3b-8d8e-1a8d2d2f9b7a",
  "session_kind": "user",
  "session_number": 1024,
  "session_token": "3FaJ9wLqX2Mz",
  "url": "https://verify.didit.me/session/3FaJ9wLqX2Mz",
  "status": "Not Started",
  "workflow_id": "test-workflow-id",
  "vendor_data": "did_0647d5ea"
}`

func TestNewRefusesMissingSettings(t *testing.T) {
	cases := map[string]struct {
		apiKey        string
		workflowID    string
		webhookSecret string
	}{
		"no api key":        {apiKey: "", workflowID: "wf", webhookSecret: "whsec"},
		"blank api key":     {apiKey: "   ", workflowID: "wf", webhookSecret: "whsec"},
		"no workflow id":    {apiKey: "key", workflowID: "", webhookSecret: "whsec"},
		"no webhook secret": {apiKey: "key", workflowID: "wf", webhookSecret: ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(tc.apiKey, tc.workflowID, tc.webhookSecret); err == nil {
				t.Error("New() error = nil, want a failure naming the missing setting")
			}
		})
	}
}

func TestCreateSessionPostsTheWorkflowAndReadsTheHostedURL(t *testing.T) {
	var gotMethod, gotPath, gotKey string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, sessionCreated)
	}))
	defer srv.Close()

	client := newTestClient(t, "whsec_test", time.Unix(1_700_000_000, 0))
	client.baseURL = srv.URL

	session, err := client.CreateSession(context.Background(), "did_0647d5ea", "https://tunnel.example/webhook/didit")
	if err != nil {
		t.Fatalf("CreateSession() error = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/v3/session/" {
		t.Errorf("path = %q, want %q", gotPath, "/v3/session/")
	}
	if gotKey != "test-api-key" {
		t.Errorf("x-api-key = %q, want %q", gotKey, "test-api-key")
	}

	body := string(gotBody)
	for _, want := range []string{
		`"workflow_id":"test-workflow-id"`,
		`"vendor_data":"did_0647d5ea"`,
		`"callback":"https://tunnel.example/webhook/didit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want it to carry %s", body, want)
		}
	}

	if want := "4c5c7f3a-1f82-4f3b-8d8e-1a8d2d2f9b7a"; session.ID != want {
		t.Errorf("ID = %q, want %q", session.ID, want)
	}
	if want := "https://verify.didit.me/session/3FaJ9wLqX2Mz"; session.URL != want {
		t.Errorf("URL = %q, want %q", session.URL, want)
	}
	if want := "Not Started"; session.Status != want {
		t.Errorf("Status = %q, want %q", session.Status, want)
	}
}

func TestCreateSessionReportsAProviderFailureWithoutLeakingTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"workflow_id":["Invalid workflow_id."]}`)
	}))
	defer srv.Close()

	client := newTestClient(t, "whsec_test", time.Unix(1_700_000_000, 0))
	client.baseURL = srv.URL

	_, err := client.CreateSession(context.Background(), "did_1", "https://tunnel.example/webhook/didit")
	if err == nil {
		t.Fatal("CreateSession() error = nil, want a provider failure on 400")
	}

	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want a *didit.Error", err)
	}
	if providerErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want %d", providerErr.StatusCode, http.StatusBadRequest)
	}
	if strings.Contains(err.Error(), "test-api-key") {
		t.Errorf("error %q leaks the API key", err.Error())
	}
}

func TestDecisionReadsTheScoreAndTheVerdict(t *testing.T) {
	// The decision endpoint returns one array per feature the workflow ran, and
	// the score sits inside the array. Face match is the identity score the
	// contract reports.
	body := `{
	  "session_id": "4c5c7f3a-1f82-4f3b-8d8e-1a8d2d2f9b7a",
	  "session_kind": "user",
	  "status": "Approved",
	  "workflow_id": "test-workflow-id",
	  "vendor_data": "did_0647d5ea",
	  "id_verifications": [ { "node_id": "n1", "status": "Approved" } ],
	  "liveness_checks": [ { "node_id": "n2", "status": "Approved", "score": 0.98 } ],
	  "face_matches": [ { "node_id": "n3", "status": "Approved", "score": 0.94 } ]
	}`

	var gotMethod, gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client := newTestClient(t, "whsec_test", time.Unix(1_700_000_000, 0))
	client.baseURL = srv.URL

	decision, err := client.Decision(context.Background(), "4c5c7f3a-1f82-4f3b-8d8e-1a8d2d2f9b7a")
	if err != nil {
		t.Fatalf("Decision() error = %v, want nil", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodGet)
	}
	if want := "/v3/session/4c5c7f3a-1f82-4f3b-8d8e-1a8d2d2f9b7a/decision/"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotKey != "test-api-key" {
		t.Errorf("x-api-key = %q, want %q", gotKey, "test-api-key")
	}

	if decision.Pending {
		t.Error("Pending = true, want false for a session with a verdict")
	}
	if decision.Approved == nil {
		t.Fatal("Approved = nil, want true for an approved session")
	}
	if !*decision.Approved {
		t.Error("Approved = false, want true")
	}
	if decision.Score == nil {
		t.Fatal("Score = nil, want the face match score")
	}
	if *decision.Score != 0.94 {
		t.Errorf("Score = %v, want 0.94", *decision.Score)
	}
}

func TestDecisionIsPendingWhileThereIsNoVerdict(t *testing.T) {
	// The contract answers {aprobada:null, score:null, pendiente:true} while the
	// person has not finished, and the client must not invent a verdict.
	body := `{"session_id":"did_1","session_kind":"user","status":"In Progress","face_matches":null}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client := newTestClient(t, "whsec_test", time.Unix(1_700_000_000, 0))
	client.baseURL = srv.URL

	decision, err := client.Decision(context.Background(), "did_1")
	if err != nil {
		t.Fatalf("Decision() error = %v, want nil", err)
	}

	if !decision.Pending {
		t.Error("Pending = false, want true while the session has no verdict")
	}
	if decision.Approved != nil {
		t.Errorf("Approved = %v, want nil while there is no verdict", *decision.Approved)
	}
	if decision.Score != nil {
		t.Errorf("Score = %v, want nil when no feature reported one", *decision.Score)
	}
	if want := "In Progress"; decision.Status != want {
		t.Errorf("Status = %q, want %q", decision.Status, want)
	}
}
