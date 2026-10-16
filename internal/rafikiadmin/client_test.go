package rafikiadmin_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

const (
	tenantID  = "11111111-1111-4111-8111-111111111111"
	apiSecret = "a-secret"
)

// newTestClient points the client at a fake Admin API.
func newTestClient(t *testing.T, url string) *rafikiadmin.Client {
	t.Helper()

	c, err := rafikiadmin.New(rafikiadmin.Config{
		URL:       url,
		TenantID:  tenantID,
		APISecret: apiSecret,
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}
	return c
}

// verifySignature repeats what Rafiki does in getTenantFromApiSignature: the
// payload is "<timestamp>.<canonical body>" and the digest is its HMAC-SHA256
// hex value, keyed with the tenant API secret.
func verifySignature(t *testing.T, r *http.Request, body []byte) {
	t.Helper()

	if got := r.Header.Get("tenant-id"); got != tenantID {
		t.Errorf("tenant-id header = %q, want %q", got, tenantID)
	}

	timestampPart, digestPart, found := strings.Cut(r.Header.Get("Signature"), ", ")
	if !found {
		t.Errorf("Signature header = %q, want the form %q", r.Header.Get("Signature"), "t=<ms>, v1=<hex>")
		return
	}

	timestamp, found := strings.CutPrefix(timestampPart, "t=")
	if !found {
		t.Errorf("Signature timestamp part = %q, want a t= prefix", timestampPart)
		return
	}
	digest, found := strings.CutPrefix(digestPart, "v1=")
	if !found {
		t.Errorf("Signature digest part = %q, want a v1= prefix", digestPart)
		return
	}

	// Rafiki rejects a signature older than its TTL, so the timestamp has to be
	// the moment of the request.
	ms, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		t.Errorf("Signature timestamp %q is not unix milliseconds: %v", timestamp, err)
		return
	}
	if age := time.Since(time.UnixMilli(ms)); age > 5*time.Second || age < -5*time.Second {
		t.Errorf("Signature timestamp is %v away from now, want a fresh one", age)
	}

	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write([]byte(timestamp + "." + string(body)))
	if want := hex.EncodeToString(mac.Sum(nil)); digest != want {
		t.Errorf("signature digest = %q, want %q over payload %q", digest, want, timestamp+"."+string(body))
	}
}

// adminServer answers like Rafiki's Admin API: it verifies every request first.
func adminServer(t *testing.T, reply string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request body: %v", err)
		}
		verifySignature(t, r, body)

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
}

func TestWhoamiSignsTheRequestAndDecodesTheReply(t *testing.T) {
	srv := adminServer(t, `{"data":{"whoami":{"id":"`+tenantID+`","isOperator":true}}}`)
	defer srv.Close()

	whoami, err := newTestClient(t, srv.URL).Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() returned an error: %v", err)
	}
	if whoami.ID != tenantID {
		t.Errorf("ID = %q, want %q", whoami.ID, tenantID)
	}
	if !whoami.IsOperator {
		t.Error("IsOperator = false, want true")
	}
}

// Rafiki canonicalizes the parsed body before hashing it, so the bytes on the
// wire must already be the canonical form: sorted keys and no whitespace.
func TestDoSendsTheCanonicalBody(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	query := `query Q { whoami { id } }`
	variables := map[string]any{"tenantId": "t-1", "first": 1}
	if err := newTestClient(t, srv.URL).Do(context.Background(), query, variables, nil); err != nil {
		t.Fatalf("Do() returned an error: %v", err)
	}

	want := `{"query":"query Q { whoami { id } }","variables":{"first":1,"tenantId":"t-1"}}`
	if string(got) != want {
		t.Errorf("request body = %s, want %s", got, want)
	}
}

func TestDoReturnsGraphQLErrors(t *testing.T) {
	srv := adminServer(t, `{"errors":[{"message":"asset not found"}]}`)
	defer srv.Close()

	err := newTestClient(t, srv.URL).Do(context.Background(), `query { whoami { id } }`, nil, nil)
	if err == nil {
		t.Fatal("Do() ignored a GraphQL error, want an error")
	}
	if !strings.Contains(err.Error(), "asset not found") {
		t.Errorf("error %q does not carry the GraphQL message", err)
	}
}

func TestDoReportsTheHTTPStatusWhenItIsNot200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := newTestClient(t, srv.URL).Do(context.Background(), `query { whoami { id } }`, nil, nil)
	if err == nil {
		t.Fatal("Do() accepted a 401, want an error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not report the HTTP status", err)
	}
}

// Rafiki keys its replay protection on the whole Signature header, so two
// requests signed in the same millisecond must not share a timestamp.
func TestDoUsesAUniqueTimestampPerRequest(t *testing.T) {
	pinned := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	var signatures []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signatures = append(signatures, r.Header.Get("Signature"))
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	c, err := rafikiadmin.New(rafikiadmin.Config{
		URL:       srv.URL,
		TenantID:  tenantID,
		APISecret: apiSecret,
		Now:       func() time.Time { return pinned },
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	for range 2 {
		if err := c.Do(context.Background(), `query { whoami { id } }`, nil, nil); err != nil {
			t.Fatalf("Do() returned an error: %v", err)
		}
	}

	if len(signatures) != 2 {
		t.Fatalf("recorded %d signatures, want 2", len(signatures))
	}
	if signatures[0] == signatures[1] {
		t.Errorf("both requests used the signature %q, want two different ones", signatures[0])
	}
}

func TestNewRejectsIncompleteConfiguration(t *testing.T) {
	tests := map[string]rafikiadmin.Config{
		"missing URL":       {TenantID: tenantID, APISecret: apiSecret},
		"missing tenant ID": {URL: "http://localhost:3001/graphql", APISecret: apiSecret},
		"missing secret":    {URL: "http://localhost:3001/graphql", TenantID: tenantID},
		"URL without scheme": {
			URL:       "localhost:3001/graphql",
			TenantID:  tenantID,
			APISecret: apiSecret,
		},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := rafikiadmin.New(cfg); err == nil {
				t.Fatal("New() accepted an incomplete configuration, want an error")
			}
		})
	}
}

// captureBody runs one operation against a server that records the raw request
// body, which is what the signature is computed over.
func captureBody(t *testing.T, variables map[string]any) []byte {
	t.Helper()

	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	if err := newTestClient(t, srv.URL).Do(context.Background(), `query Q { x }`, variables, nil); err != nil {
		t.Fatalf("Do() returned an error: %v", err)
	}

	return got
}

// JCS leaves U+2028 and U+2029 literal, while Go's JSON encoder escapes them for
// JavaScript safety. Rafiki hashes the canonical form of the body it parsed, so
// the escaped form would not match and the signature would be refused with a 401
// that says nothing. The peer session proved a literal U+2028 survives a full
// webhook round trip on the other side.
func TestDoKeepsTheSeparatorsJCSLeavesLiteral(t *testing.T) {
	body := string(captureBody(t, map[string]any{"publicName": "Tienda\u2028de barrio\u2029fin"}))

	if strings.Contains(body, `\u2028`) || strings.Contains(body, `\u2029`) {
		t.Errorf("body %s carries the escaped separators, which Rafiki would not reproduce", body)
	}
	if !strings.Contains(body, "\u2028") || !strings.Contains(body, "\u2029") {
		t.Errorf("body %s does not carry the literal separators JCS leaves alone", body)
	}
}

// A payload that really holds the text `\u2028` has to survive: the escape Go
// wrote for that backslash is not a separator.
func TestDoKeepsAnEscapedBackslashIntact(t *testing.T) {
	const nota = `costo \u2028 exacto`

	body := captureBody(t, map[string]any{"nota": nota})

	if !strings.Contains(string(body), `\\u2028`) {
		t.Errorf("body %s lost the escape of the literal backslash", body)
	}

	var decoded struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body %s is no longer valid JSON: %v", body, err)
	}
	if decoded.Variables["nota"] != nota {
		t.Errorf("the payload came back as %q, want %q", decoded.Variables["nota"], nota)
	}
}
