package didit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Statuses this package acts on. The comparison is case-insensitive on purpose:
// Didit's documentation says the statuses are case-sensitive title-case strings
// and its own example payloads answer "APPROVED", so both spellings have to mean
// the same thing.
const (
	statusApproved = "Approved"
	statusDeclined = "Declined"
)

// Event is a parsed webhook body: only the envelope fields the contract needs.
// The decision object is not kept, because the contract reads the verdict from
// the signed status and the person from vendor_data.
type Event struct {
	// ID is the provider's event identifier. It is stable across the fan-out and
	// across retries, so it is what the caller deduplicates on.
	ID string

	// Type is the event type, for example "status.updated".
	Type string

	// SessionID is the provider's verification session identifier.
	SessionID string

	// VendorData is what this backend sent when it created the session: our own
	// session identifier, which is how the event is traced back to a person.
	VendorData string

	// Status is what the provider sent, kept verbatim so the caller can report
	// an intermediate state without translating it.
	Status string
}

// Approved reports whether the event carries an approval.
func (e Event) Approved() bool { return strings.EqualFold(e.Status, statusApproved) }

// Final reports whether the event carries a verdict the caller has to act on.
// Anything else is an intermediate state the contract answers with
// procesado:false.
func (e Event) Final() bool {
	return strings.EqualFold(e.Status, statusApproved) || strings.EqualFold(e.Status, statusDeclined)
}

// ParseWebhook reads a webhook body that has already passed signature
// verification.
func ParseWebhook(raw []byte) (Event, error) {
	// A pointer to the struct is what makes a body of "null" detectably different
	// from an empty body: unmarshalling null leaves the pointer nil instead of
	// silently producing a zero event that looks like an intermediate status.
	var body *struct {
		EventID    string `json:"event_id"`
		Type       string `json:"webhook_type"`
		SessionID  string `json:"session_id"`
		VendorData string `json:"vendor_data"`
		Status     string `json:"status"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Event{}, fmt.Errorf("didit: decoding the webhook body: %w", err)
	}
	if body == nil {
		return Event{}, errors.New("didit: the webhook body is not a JSON object")
	}

	return Event{
		ID:         body.EventID,
		Type:       body.Type,
		SessionID:  body.SessionID,
		VendorData: body.VendorData,
		Status:     body.Status,
	}, nil
}

// VerifyWebhook checks the signature and the freshness of a webhook delivery.
//
// The signature covers the canonical form of the body rather than its raw bytes,
// so a proxy or framework that re-encodes the JSON on the way through does not
// break the verification. Every rejection wraps ErrInvalidSignature: the caller
// logs the reason and answers one 401 with FIRMA_INVALIDA.
//
// WARNING, and this is a property of the provider's scheme rather than a defect
// here: X-Signature-V2 signs the BODY only. X-Timestamp is not inside the digest,
// so the freshness window below cannot authenticate anything an attacker already
// captured. A valid (body, signature) pair replays indefinitely by rewriting the
// timestamp header to now. The real defence belongs to the caller: deduplicate on
// Event.ID, which the provider keeps stable across its fan-out and its retries,
// BEFORE applying any effect. Treat the window as a filter against background
// noise, never as replay protection.
func (c *Client) VerifyWebhook(rawBody []byte, signature, timestamp string) error {
	if err := c.checkFreshness(timestamp); err != nil {
		return err
	}

	canonical, err := canonicalJSON(rawBody)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSignature, err)
	}

	received, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return fmt.Errorf("%w: the signature is not hexadecimal", ErrInvalidSignature)
	}

	mac := hmac.New(sha256.New, []byte(c.webhookSecret))
	mac.Write(canonical)
	// A length-varying comparison leaks where two digests start to differ, which
	// is enough to forge a signature one byte at a time.
	if !hmac.Equal(mac.Sum(nil), received) {
		return fmt.Errorf("%w: the signature does not cover this body", ErrInvalidSignature)
	}

	return nil
}

// checkFreshness refuses a delivery dispatched too far from now in either
// direction. A timestamp that cannot be read is a rejection, never a pass.
func (c *Client) checkFreshness(timestamp string) error {
	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: the timestamp %q is not unix seconds", ErrInvalidSignature, timestamp)
	}

	// A zero value for Now is what a Client built as a struct literal carries; it
	// is not a clock, so the default is restored instead of reporting a delivery
	// from 1970 as stale.
	now := time.Now
	if c.now != nil {
		now = c.now
	}

	if age := now().Sub(time.Unix(seconds, 0)); age > DefaultMaxSignatureAge || age < -DefaultMaxSignatureAge {
		return fmt.Errorf("%w: the delivery is %v away from now, beyond the %v window",
			ErrInvalidSignature, age.Round(time.Second), DefaultMaxSignatureAge)
	}

	return nil
}
