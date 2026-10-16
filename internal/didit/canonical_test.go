package didit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// The canonical form is the whole contract of X-Signature-V2. Didit rebuilds the
// signature from the body it parsed, so the receiver has to rebuild the same
// bytes from the body it parsed. These cases fix the bytes.

func TestCanonicalJSONSortsKeysAndStripsWhitespace(t *testing.T) {
	// The sender may indent, reorder and pad; the signature covers the compact
	// form with sorted keys, which is why V2 survives a middleware that
	// re-encodes the body.
	input := []byte(`{
	  "zebra": 1,
	  "alpha": { "delta": true, "charlie": null },
	  "list": [ 1, "two" ]
	}`)

	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON() error = %v, want nil", err)
	}

	want := `{"alpha":{"charlie":null,"delta":true},"list":[1,"two"],"zebra":1}`
	if string(got) != want {
		t.Errorf("canonicalJSON() =\n  %s\nwant\n  %s", got, want)
	}
}

func TestCanonicalJSONKeepsUnicodeLiteral(t *testing.T) {
	// ensure_ascii=False in Didit's canonicalizer: an accented name stays literal,
	// it is not turned into an escape.
	input := []byte(`{"name":"José Andrés","city":"Medellín"}`)

	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON() error = %v, want nil", err)
	}

	want := `{"city":"Medellín","name":"José Andrés"}`
	if string(got) != want {
		t.Errorf("canonicalJSON() = %q, want %q", got, want)
	}
}

func TestCanonicalJSONKeepsTheLineSeparatorsLiteral(t *testing.T) {
	// Go escapes U+2028 and U+2029 for JavaScript safety and Didit's canonicalizer
	// leaves both literal, so a body carrying either one would otherwise produce a
	// digest the sender never computed: a silent 401.
	input := []byte(`{"note":"a\u2028b\u2029c"}`)

	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON() error = %v, want nil", err)
	}

	want := "{\"note\":\"a\u2028b\u2029c\"}"
	if string(got) != want {
		t.Errorf("canonicalJSON() = %q, want %q", got, want)
	}
}

func TestCanonicalJSONKeepsTheTextOfAnEscape(t *testing.T) {
	// A body whose text is the six characters \u2028 is not the same body as one
	// carrying the character. The backslash is escaped, so the walk must not
	// rewrite the escape inside it.
	input := []byte(`{"note":"\\u2028"}`)

	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON() error = %v, want nil", err)
	}

	want := `{"note":"\\u2028"}`
	if string(got) != want {
		t.Errorf("canonicalJSON() = %q, want %q", got, want)
	}
}

func TestCanonicalJSONShortensWholeNumbers(t *testing.T) {
	// Didit's canonicalizer runs every float through shorten_floats, which turns a
	// whole-valued float into an int before dumping. So 1.0 and 1 must produce the
	// same bytes, and 1e2 must become 100.
	cases := map[string]string{
		`{"score":1.0}`:   `{"score":1}`,
		`{"score":1}`:     `{"score":1}`,
		`{"score":0.94}`:  `{"score":0.94}`,
		`{"score":1e2}`:   `{"score":100}`,
		`{"score":-2.0}`:  `{"score":-2}`,
		`{"score":[1.0]}`: `{"score":[1]}`,
	}

	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := canonicalJSON([]byte(input))
			if err != nil {
				t.Fatalf("canonicalJSON(%s) error = %v, want nil", input, err)
			}
			if string(got) != want {
				t.Errorf("canonicalJSON(%s) = %s, want %s", input, got, want)
			}
		})
	}
}

func TestCanonicalJSONKeepsIntegerPrecision(t *testing.T) {
	// An identifier bigger than a float64 can hold must not be rounded on the way
	// through: the signature is computed over the digits as written.
	input := []byte(`{"event_id":12345678901234567890}`)

	got, err := canonicalJSON(input)
	if err != nil {
		t.Fatalf("canonicalJSON() error = %v, want nil", err)
	}

	want := `{"event_id":12345678901234567890}`
	if string(got) != want {
		t.Errorf("canonicalJSON() = %s, want %s", got, want)
	}
}

func TestCanonicalJSONRejectsABodyThatIsNotAnObject(t *testing.T) {
	for _, input := range []string{``, `null`, `"text"`, `12`, `[1,2]`, `{`} {
		if _, err := canonicalJSON([]byte(input)); err == nil {
			t.Errorf("canonicalJSON(%q) error = nil, want a failure", input)
		}
	}
}

func TestVerifyWebhookAcceptsASignatureOverTheCanonicalBody(t *testing.T) {
	// The canonical string is written out by hand here, not produced by the code
	// under test: otherwise the test would only prove the function agrees with
	// itself.
	canonical := `{"session_id":"did_0647d5ea","status":"Approved","vendor_data":"per_laura"}`
	secret := "whsec_test"

	// The sender ships the same document with different key order and whitespace.
	body := []byte(`{"status": "Approved", "vendor_data": "per_laura", "session_id": "did_0647d5ea"}`)

	client := newTestClient(t, secret, time.Unix(1_700_000_000, 0))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	signature := hex.EncodeToString(mac.Sum(nil))

	if err := client.VerifyWebhook(body, signature, "1700000000"); err != nil {
		t.Errorf("VerifyWebhook() error = %v, want nil for a signature over the canonical body", err)
	}
}

func TestVerifyWebhookRejectsAWrongSignature(t *testing.T) {
	client := newTestClient(t, "whsec_test", time.Unix(1_700_000_000, 0))

	err := client.VerifyWebhook([]byte(`{"a":1}`), "deadbeef", "1700000000")
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("VerifyWebhook() error = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyWebhookRejectsAStaleOrFutureTimestamp(t *testing.T) {
	body := []byte(`{"a":1}`)
	secret := "whsec_test"
	now := time.Unix(1_700_000_000, 0)

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(`{"a":1}`))
	signature := hex.EncodeToString(mac.Sum(nil))

	cases := map[string]struct {
		timestamp string
		wantErr   bool
	}{
		"now":                           {timestamp: "1700000000", wantErr: false},
		"just inside the window behind": {timestamp: "1699999701", wantErr: false},
		"just outside it behind":        {timestamp: "1699999699", wantErr: true},
		"far in the past":               {timestamp: "1600000000", wantErr: true},
		"just inside the window ahead":  {timestamp: "1700000299", wantErr: false},
		"just outside it ahead":         {timestamp: "1700000301", wantErr: true},
		"a value that is not a number":  {timestamp: "not-a-number", wantErr: true},
		"an empty timestamp":            {timestamp: "", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := newTestClient(t, secret, now)

			// A timestamp that cannot be read is a rejection, not a pass: it must
			// never be the reason a forged signature is accepted.
			err := client.VerifyWebhook(body, signature, tc.timestamp)
			if tc.wantErr && !errors.Is(err, ErrInvalidSignature) {
				t.Errorf("VerifyWebhook(timestamp=%q) error = %v, want ErrInvalidSignature", tc.timestamp, err)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("VerifyWebhook(timestamp=%q) error = %v, want nil", tc.timestamp, err)
			}
		})
	}
}
