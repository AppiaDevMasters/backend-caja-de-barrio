package rafikiadmin_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// graphQLRequest is the body the client posts, decoded so a test can assert what
// the Admin API was asked for.
type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// adminStub answers each request with the next queued reply and keeps what it was
// asked. It verifies the signature of every request, so a test fails as soon as
// the credentials stop being part of the contract.
func adminStub(t *testing.T, replies ...string) (*httptest.Server, func() []graphQLRequest) {
	t.Helper()

	var (
		mu       sync.Mutex
		requests []graphQLRequest
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		verifySignature(t, r, body)

		var request graphQLRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("the request body %s is not GraphQL JSON: %v", body, err)
		}

		mu.Lock()
		requests = append(requests, request)
		index := len(requests) - 1
		mu.Unlock()

		if index >= len(replies) {
			t.Errorf("request %d was not expected: %s", index+1, request.Query)
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(replies[index]))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []graphQLRequest {
		mu.Lock()
		defer mu.Unlock()

		return append([]graphQLRequest(nil), requests...)
	}
}

// requireRequests fails the test unless exactly want requests arrived.
func requireRequests(t *testing.T, asked []graphQLRequest, want int) {
	t.Helper()

	if len(asked) != want {
		t.Fatalf("the Admin API received %d requests, want %d", len(asked), want)
	}
}

// requireVariables fails the test unless the recorded call carried them.
func requireVariables(t *testing.T, got, want map[string]any) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("variables = %v, want %v", got, want)
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Errorf("variables %v do not carry %q", got, key)
			continue
		}
		if !jsonEqual(gotValue, wantValue) {
			t.Errorf("variables[%q] = %#v, want %#v", key, gotValue, wantValue)
		}
	}
}

func jsonEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}

	return string(left) == string(right)
}
