// Package didit implements the client of Didit's verification API that the
// identity routes use: it creates a hosted verification session and it verifies
// the webhook Didit posts back with the result.
//
// It is a client and nothing else. The three secrets arrive as constructor
// parameters, so this package never reads the environment; it opens no routes, so
// the caller owns the endpoints and their error envelope.
package didit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultBaseURL is the host Didit documents for the sessions API. It is a
	// field of Client so a test can point it at its own server.
	defaultBaseURL = "https://verification.didit.me"

	// sessionPath creates a verification session. The trailing slash is the
	// provider's own spelling.
	sessionPath = "/v3/session/"

	// requestTimeout bounds one call.
	requestTimeout = 15 * time.Second

	// maxResponseBytes keeps a misbehaving endpoint from filling memory.
	maxResponseBytes = 1 << 20

	// summaryLimit bounds how much of a provider error body is kept, because it
	// ends up in logs and in a reply to the agent.
	summaryLimit = 200
)

// Header names of the two headers the signature verification reads.
const (
	// SignatureHeaderV2 is the recommended signature header: it covers the
	// canonical JSON rather than the raw bytes, so it survives a middleware that
	// re-encodes the body.
	SignatureHeaderV2 = "X-Signature-V2"

	// TimestampHeader is when Didit dispatched the webhook, in Unix seconds.
	TimestampHeader = "X-Timestamp"
)

// DefaultMaxSignatureAge bounds how far the dispatch timestamp may be from now in
// either direction. Didit's own guidance is 300 seconds.
const DefaultMaxSignatureAge = 300 * time.Second

// ErrInvalidSignature reports a webhook this package refuses. Every rejection
// reason wraps it, so the caller answers one 401 FIRMA_INVALIDA whichever part
// failed.
var ErrInvalidSignature = errors.New("didit: the webhook signature is not valid")

// Error is a provider failure: transport, authentication, a status the endpoint
// does not define, or a body that cannot be read. The caller maps it to
// PROVEEDOR (502).
type Error struct {
	// StatusCode is the HTTP status the provider answered with, or 0 when the
	// request never completed.
	StatusCode int

	// Message is what the provider said, bounded, so a caller can log it. It
	// never carries the API key: the body comes from the reply, not the request.
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("didit: the provider answered %d: %s", e.StatusCode, e.Message)
}

// Session is a verification session as the provider created it.
type Session struct {
	// ID is the provider's session identifier, used on every later call.
	ID string

	// URL is the hosted verification page the person is sent to. The provider
	// calls this field "url"; there is no "verification_url".
	URL string

	// Status is the session status, "Not Started" on a fresh session.
	Status string

	// VendorData is what the caller sent, echoed back.
	VendorData string
}

// Decision is the verdict of a verification session.
type Decision struct {
	// Status is what the provider reported, kept verbatim.
	Status string

	// Approved is nil while there is no verdict, so a caller cannot read a
	// pending session as a rejection. The contract reports it as null.
	Approved *bool

	// Score is the identity score when the workflow reported one. The contract
	// asks for a single score and the provider returns one per feature: this is
	// the face match score, and the liveness score only when no face match ran.
	Score *float64

	// Pending is true while the person has not finished.
	Pending bool

	// Document is what the ID verification feature read from the person's document,
	// when the workflow ran one (the registration workflow does; the biometric one
	// does not). It is what the registration hands to the civil registry.
	Document Document
}

// Document is the identity document as the provider read it.
type Document struct {
	// Type is what the provider reported, for example "Identity Card".
	Type string

	// Number is the document number, digits only when the document has them.
	Number string

	// FullName is the name printed on the document, as the provider read it.
	FullName string
}

// Client is a Didit client. It is safe for concurrent use.
type Client struct {
	apiKey        string
	workflowID    string
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
	now           func() time.Time
}

// New returns a client with the three settings the provider needs. A missing
// setting fails here, at startup, instead of at the first call.
func New(apiKey, workflowID, webhookSecret string) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("didit: the API key is empty (DIDIT_API_KEY)")
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, errors.New("didit: the workflow ID is empty (DIDIT_WORKFLOW_ID)")
	}
	webhookSecret = strings.TrimSpace(webhookSecret)
	if webhookSecret == "" {
		return nil, errors.New("didit: the webhook secret is empty (DIDIT_WEBHOOK_SECRET)")
	}

	return &Client{
		apiKey:        apiKey,
		workflowID:    workflowID,
		webhookSecret: webhookSecret,
		baseURL:       defaultBaseURL,
		httpClient:    &http.Client{Timeout: requestTimeout},
		now:           time.Now,
	}, nil
}

// CreateSession asks the provider for a new hosted verification session.
//
// vendorData is this backend's own session identifier; the provider echoes it
// back on the webhook, which is how the result is traced to a person.
//
// callbackURL is what the contract's DIDIT_CALLBACK_URL carries: the URL the
// hosted flow sends the person back to, with verificationSessionId and status
// appended. The provider treats it as optional and falls back to the workflow's
// own default, so passing an empty value silently sends the person somewhere this
// backend does not control. Pass the configured value.
func (c *Client) CreateSession(ctx context.Context, vendorData, callbackURL string) (Session, error) {
	return c.CreateSessionWith(ctx, c.workflowID, vendorData, callbackURL)
}

// CreateSessionWith is CreateSession with an explicit workflow. The contract uses two:
// the registration one (document + liveness) and the biometric one for every
// withdrawal (liveness + face match against the registered face).
func (c *Client) CreateSessionWith(ctx context.Context, workflowID, vendorData, callbackURL string) (Session, error) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		workflowID = c.workflowID
	}
	payload := map[string]string{
		"workflow_id": workflowID,
		"vendor_data": vendorData,
	}
	if callbackURL != "" {
		payload["callback"] = callbackURL
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Session{}, fmt.Errorf("didit: encoding the session request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.baseURL, "/")+sessionPath, bytes.NewReader(body))
	if err != nil {
		return Session{}, fmt.Errorf("didit: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("didit: calling the provider: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Session{}, fmt.Errorf("didit: reading the provider reply: %w", err)
	}

	// The provider answers 201 Created, not 200.
	if resp.StatusCode != http.StatusCreated {
		return Session{}, &Error{StatusCode: resp.StatusCode, Message: summarize(raw)}
	}

	var decoded struct {
		SessionID  string `json:"session_id"`
		URL        string `json:"url"`
		Status     string `json:"status"`
		VendorData string `json:"vendor_data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Session{}, fmt.Errorf("didit: decoding the provider reply: %w", err)
	}

	return Session{
		ID:         decoded.SessionID,
		URL:        decoded.URL,
		Status:     decoded.Status,
		VendorData: decoded.VendorData,
	}, nil
}

// Decision reads the verdict of a verification session.
func (c *Client) Decision(ctx context.Context, sessionID string) (Decision, error) {
	endpoint := strings.TrimSuffix(c.baseURL, "/") +
		"/v3/session/" + url.PathEscape(sessionID) + "/decision/"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Decision{}, fmt.Errorf("didit: building the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("didit: calling the provider: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Decision{}, fmt.Errorf("didit: reading the provider reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Decision{}, &Error{StatusCode: resp.StatusCode, Message: summarize(raw)}
	}

	var decoded struct {
		Status         string          `json:"status"`
		FaceMatches    []featureReport `json:"face_matches"`
		LivenessChecks []featureReport `json:"liveness_checks"`
		// The v3 decision reports the ID verification as an array, like every other
		// feature; the older shape was a single object. Both are read.
		IDVerifications []idVerification `json:"id_verifications"`
		IDVerification  *idVerification  `json:"id_verification"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Decision{}, fmt.Errorf("didit: decoding the provider reply: %w", err)
	}

	decision := Decision{Status: decoded.Status}
	switch {
	case strings.EqualFold(decoded.Status, statusApproved):
		approved := true
		decision.Approved = &approved
	case strings.EqualFold(decoded.Status, statusDeclined):
		approved := false
		decision.Approved = &approved
	default:
		// No verdict yet: the contract reports aprobada as null, not as false.
		decision.Pending = true
	}
	decision.Score = firstScore(decoded.FaceMatches, decoded.LivenessChecks)

	reads := decoded.IDVerifications
	if decoded.IDVerification != nil {
		reads = append(reads, *decoded.IDVerification)
	}
	for _, read := range reads {
		if document := read.document(); document.Number != "" {
			decision.Document = document
			break
		}
	}

	return decision, nil
}

// idVerification is the ID verification block of a decision: only the fields the
// registration needs. Nothing else of the document (photos, address, dates) is kept.
type idVerification struct {
	DocumentType   string `json:"document_type"`
	DocumentNumber string `json:"document_number"`
	PersonalNumber string `json:"personal_number"`
	FirstName      string `json:"first_name"`
	LastName       string `json:"last_name"`
	FullName       string `json:"full_name"`
}

// document normalizes one read. A Colombian cédula is printed with dots, and the
// registry wants digits only.
func (v idVerification) document() Document {
	number := strings.TrimSpace(v.DocumentNumber)
	if number == "" {
		number = strings.TrimSpace(v.PersonalNumber)
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, number)
	if digits != "" {
		number = digits
	}

	name := strings.TrimSpace(v.FullName)
	if name == "" {
		name = strings.TrimSpace(strings.TrimSpace(v.FirstName) + " " + strings.TrimSpace(v.LastName))
	}

	return Document{Type: strings.TrimSpace(v.DocumentType), Number: number, FullName: name}
}

// featureReport is one entry of a feature block of the decision. Each block is an
// array because a workflow can run the same feature more than once.
type featureReport struct {
	Status string   `json:"status"`
	Score  *float64 `json:"score"`
}

// firstScore returns the first score the given blocks report, in the order the
// caller listed them.
func firstScore(blocks ...[]featureReport) *float64 {
	for _, block := range blocks {
		for _, report := range block {
			if report.Score != nil {
				return report.Score
			}
		}
	}

	return nil
}

// summarize bounds a provider error body so it can travel in a log line.
func summarize(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > summaryLimit {
		return text[:summaryLimit] + "..."
	}

	return text
}
