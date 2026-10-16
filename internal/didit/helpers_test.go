package didit

import (
	"testing"
	"time"
)

// newTestClient builds a real client with the clock frozen, so a freshness rule
// is testable without sleeping. The base URL is a field rather than a constructor
// parameter, so the production constructor stays the one the surface agreement
// fixed: New(apiKey, workflowID, webhookSecret).
func newTestClient(t *testing.T, webhookSecret string, now time.Time) *Client {
	t.Helper()

	client, err := New("test-api-key", "test-workflow-id", webhookSecret)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	client.now = func() time.Time { return now }

	return client
}
