package ase_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/ase"
)

const (
	testSecret  = "a-signature-secret"
	testVersion = 1
)

// eventBody is the canonical body Rafiki sends. Rafiki canonicalizes
// {id, type, data} before signing it, so this is the byte string it hashes and
// the one a receiver has to rebuild.
const eventBody = `{"data":{"amount":"1000","walletAddressUrl":"https://ase.example/tienda"},"id":"evt-1","type":"incoming_payment.completed"}`

// pinned is the clock the tests freeze, so a signature built here is always
// inside the freshness window.
var pinned = time.Date(2026, time.October, 16, 18, 0, 0, 0, time.UTC)

// signAt repeats what Rafiki does in generateWebhookSignature: the payload is
// "<timestamp>.<canonical body>" and the digest is its HMAC-SHA256 hex value,
// keyed with SIGNATURE_SECRET.
func signAt(timestamp int64, body string) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "." + body))

	return "t=" + strconv.FormatInt(timestamp, 10) + ", v" + strconv.Itoa(testVersion) + "=" + hex.EncodeToString(mac.Sum(nil))
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()

	srv, err := ase.New(ase.Config{
		SignatureSecret:      testSecret,
		IdentityServerSecret: testIdPSecret,
		SignatureVersion:     testVersion,
		Now:                  func() time.Time { return pinned },
	})
	if err != nil {
		t.Fatalf("ase.New() returned an error: %v", err)
	}

	return srv.Handler()
}

func post(t *testing.T, handler http.Handler, body, signature string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/webhooks", strings.NewReader(body))
	if signature != "" {
		req.Header.Set("Rafiki-Signature", signature)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	return rec
}

// Rafiki treats any status but 200 as a failure and retries with backoff, so a
// signed event has to be acknowledged with exactly 200.
func TestWebhookAcceptsASignedEventAndAnswers200(t *testing.T) {
	rec := post(t, newHandler(t), eventBody, signAt(pinned.UnixMilli(), eventBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: body=%q", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want an empty reply", body)
	}
}

// The digest covers the canonical form, not the bytes on the wire: a body whose
// keys arrive in another order, or whose string holds one of the two JavaScript
// line separators JCS keeps literal, still verifies.
func TestWebhookVerifiesTheCanonicalFormOfTheEvent(t *testing.T) {
	tests := map[string]struct {
		body      string
		canonical string
	}{
		"keys out of order": {
			body:      `{"type":"incoming_payment.completed","id":"evt-1","data":{"walletAddressUrl":"https://ase.example/tienda","amount":"1000"}}`,
			canonical: eventBody,
		},
		"line separator kept literal": {
			body:      "{\"data\":{\"note\":\"a\u2028b\"},\"id\":\"evt-2\",\"type\":\"incoming_payment.completed\"}",
			canonical: "{\"data\":{\"note\":\"a\u2028b\"},\"id\":\"evt-2\",\"type\":\"incoming_payment.completed\"}",
		},
		"line separator written as an escape on the wire": {
			body:      `{"data":{"note":"a\u2028b"},"id":"evt-3","type":"incoming_payment.completed"}`,
			canonical: "{\"data\":{\"note\":\"a\u2028b\"},\"id\":\"evt-3\",\"type\":\"incoming_payment.completed\"}",
		},
		"numbers without a decimal point": {
			body:      `{"data":{"amount":1000},"id":"evt-4","type":"incoming_payment.completed"}`,
			canonical: `{"data":{"amount":1000},"id":"evt-4","type":"incoming_payment.completed"}`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := post(t, newHandler(t), tc.body, signAt(pinned.UnixMilli(), tc.canonical))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

// Every way the signature can be wrong is a 401: the event is refused, so the
// caller learns nothing about which part failed.
func TestWebhookRejectsASignatureThatDoesNotCoverTheEvent(t *testing.T) {
	stale := pinned.Add(-time.Hour).UnixMilli()

	tests := map[string]struct {
		body      string
		signature string
	}{
		"tampered body": {
			body:      strings.Replace(eventBody, `"1000"`, `"9999"`, 1),
			signature: signAt(pinned.UnixMilli(), eventBody),
		},
		"digest of another secret": {
			body:      eventBody,
			signature: "t=" + strconv.FormatInt(pinned.UnixMilli(), 10) + ", v1=00",
		},
		"no signature header": {
			body: eventBody,
		},
		"empty signature header": {
			body:      eventBody,
			signature: "",
		},
		"header without the separator Rafiki writes": {
			body:      eventBody,
			signature: strings.Replace(signAt(pinned.UnixMilli(), eventBody), ", ", ",", 1),
		},
		"header without the timestamp": {
			body:      eventBody,
			signature: strings.TrimPrefix(signAt(pinned.UnixMilli(), eventBody), "t="+strconv.FormatInt(pinned.UnixMilli(), 10)+", "),
		},
		"timestamp is not unix milliseconds": {
			body:      eventBody,
			signature: "t=yesterday, v1=" + strings.Split(signAt(pinned.UnixMilli(), eventBody), "=")[2],
		},
		"another signature version": {
			body:      eventBody,
			signature: strings.Replace(signAt(pinned.UnixMilli(), eventBody), " v1=", " v2=", 1),
		},
		"signature older than the window": {
			body:      eventBody,
			signature: signAt(stale, eventBody),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := post(t, newHandler(t), tc.body, tc.signature)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: body=%q", rec.Code, rec.Body.String())
			}
		})
	}
}

// Rafiki re-signs every retry, so a timestamp inside the window is accepted and
// one from the future beyond it is not.
func TestWebhookAcceptsARecentTimestampAndRejectsAFutureOne(t *testing.T) {
	tests := map[string]struct {
		at   time.Time
		want int
	}{
		"a retry one minute old": {at: pinned.Add(-time.Minute), want: http.StatusOK},
		"far in the future":      {at: pinned.Add(time.Hour), want: http.StatusUnauthorized},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := post(t, newHandler(t), eventBody, signAt(tc.at.UnixMilli(), eventBody))
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: body=%q", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// A body that is not JSON cannot be canonicalized, so it is a bad request even
// before the signature matters.
func TestWebhookRejectsABodyThatIsNotJSON(t *testing.T) {
	rec := post(t, newHandler(t), "not json", signAt(pinned.UnixMilli(), "not json"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: body=%q", rec.Code, rec.Body.String())
	}
}

// A body beyond the limit is refused before it is parsed, so it never reaches
// the signature check.
func TestWebhookRejectsABodyBeyondTheLimit(t *testing.T) {
	oversized := `{"id":"evt-4","type":"x","data":{"filler":"` + strings.Repeat("a", 2<<20) + `"}}`

	rec := post(t, newHandler(t), oversized, signAt(pinned.UnixMilli(), oversized))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestWebhookOnlyAnswersPost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)
	rec := httptest.NewRecorder()
	newHandler(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
