package didit

import (
	"testing"
)

// finalEvent is the envelope Didit posts for a decision, taken from its webhook
// documentation.
const finalEvent = `{
  "event_id": "9f1c2ba0-1111-2222-3333-444455556666",
  "webhook_type": "status.updated",
  "session_id": "did_0647d5ea",
  "status": "Approved",
  "created_at": 1700000000,
  "timestamp": 1700000000,
  "application_id": "11111111-2222-3333-4444-555555555555",
  "environment": "live",
  "workflow_id": "test-workflow-id",
  "workflow_version": 1,
  "vendor_data": "per_laura",
  "metadata": { "motivo": "retiro" },
  "decision": { "id_verifications": [] }
}`

func TestParseWebhookReadsAFinalDecision(t *testing.T) {
	event, err := ParseWebhook([]byte(finalEvent))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v, want nil", err)
	}

	if want := "9f1c2ba0-1111-2222-3333-444455556666"; event.ID != want {
		t.Errorf("ID = %q, want %q", event.ID, want)
	}
	if want := "status.updated"; event.Type != want {
		t.Errorf("Type = %q, want %q", event.Type, want)
	}
	if want := "did_0647d5ea"; event.SessionID != want {
		t.Errorf("SessionID = %q, want %q", event.SessionID, want)
	}
	if want := "per_laura"; event.VendorData != want {
		t.Errorf("VendorData = %q, want %q", event.VendorData, want)
	}
	if want := "Approved"; event.Status != want {
		t.Errorf("Status = %q, want %q", event.Status, want)
	}
	if !event.Approved() {
		t.Error("Approved() = false, want true")
	}
	if !event.Final() {
		t.Error("Final() = false, want true: the caller has to act on an approval")
	}
}

func TestParseWebhookIgnoresAnIntermediateStatus(t *testing.T) {
	// The contract answers 200 with procesado:false and the status for anything
	// that is not a verdict, so the caller needs Final() to be false and the raw
	// status to report.
	body := `{"event_id":"e1","webhook_type":"status.updated","session_id":"did_1","status":"In Progress","vendor_data":"per_laura"}`

	event, err := ParseWebhook([]byte(body))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v, want nil", err)
	}

	if event.Final() {
		t.Error("Final() = true, want false for an intermediate status")
	}
	if event.Approved() {
		t.Error("Approved() = true, want false for an intermediate status")
	}
	if want := "In Progress"; event.Status != want {
		t.Errorf("Status = %q, want %q", event.Status, want)
	}
}

func TestParseWebhookReadsTheStatusWhateverItsCase(t *testing.T) {
	// Didit's own documentation contradicts itself: the prose says the statuses
	// are case-sensitive title-case strings, and the example payloads answer
	// "APPROVED". Both spellings have to mean the same thing, or the demo breaks
	// on whichever one the sandbox happens to send.
	cases := map[string]struct {
		status       string
		wantApproved bool
		wantFinal    bool
	}{
		"title case approval":    {status: "Approved", wantApproved: true, wantFinal: true},
		"upper case approval":    {status: "APPROVED", wantApproved: true, wantFinal: true},
		"lower case approval":    {status: "approved", wantApproved: true, wantFinal: true},
		"title case decline":     {status: "Declined", wantApproved: false, wantFinal: true},
		"upper case decline":     {status: "DECLINED", wantApproved: false, wantFinal: true},
		"in review is not final": {status: "In Review", wantApproved: false, wantFinal: false},
		"expired is not final":   {status: "Kyc Expired", wantApproved: false, wantFinal: false},
		"abandoned is not final": {status: "Abandoned", wantApproved: false, wantFinal: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := `{"event_id":"e1","session_id":"did_1","status":"` + tc.status + `"}`

			event, err := ParseWebhook([]byte(body))
			if err != nil {
				t.Fatalf("ParseWebhook() error = %v, want nil", err)
			}
			if event.Status != tc.status {
				t.Errorf("Status = %q, want it kept verbatim as %q", event.Status, tc.status)
			}
			if event.Approved() != tc.wantApproved {
				t.Errorf("Approved() = %v, want %v", event.Approved(), tc.wantApproved)
			}
			if event.Final() != tc.wantFinal {
				t.Errorf("Final() = %v, want %v", event.Final(), tc.wantFinal)
			}
		})
	}
}

func TestParseWebhookRejectsABodyThatIsNotAnObject(t *testing.T) {
	for _, body := range []string{``, `null`, `[]`, `"text"`, `{`} {
		if _, err := ParseWebhook([]byte(body)); err == nil {
			t.Errorf("ParseWebhook(%q) error = nil, want a failure", body)
		}
	}
}
