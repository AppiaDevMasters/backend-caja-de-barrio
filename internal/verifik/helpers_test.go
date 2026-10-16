package verifik

import "testing"

// newTestClient points a real client at the caller's test server. The base URL is
// a field rather than a constructor parameter, so the production constructor stays
// the one the surface agreement fixed: New(token).
func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()

	client, err := New("test-token")
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	client.baseURL = baseURL

	return client
}
