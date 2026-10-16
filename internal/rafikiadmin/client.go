// Package rafikiadmin calls the Rafiki Admin API: the GraphQL endpoint on port
// 3001 that creates wallet addresses, registers wallet address keys and reports
// which tenant is acting.
//
// Rafiki authenticates every Admin API request with a shared secret and a
// timestamp instead of a bearer token. The request carries a tenant-id header
// naming the tenant, and a Signature header of the form
//
//	Signature: t=<unix milliseconds>, v1=<hex HMAC-SHA256>
//
// where the digest is computed over "<timestamp>.<canonical JSON body>" keyed
// with that tenant's API secret. Rafiki canonicalizes the body it parsed (JCS,
// sorted keys and no whitespace), refuses a signature older than
// ADMIN_API_SIGNATURE_TTL_SECONDS and remembers every Signature header it has
// seen, so a replayed one is refused too.
package rafikiadmin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// signatureVersion is the version Rafiki expects in the v<version> prefix of
	// the Signature header (ADMIN_API_SIGNATURE_VERSION, 1 by default).
	signatureVersion = 1

	defaultHTTPTimeout = 15 * time.Second

	// maxResponseBytes keeps a misbehaving endpoint from filling memory.
	maxResponseBytes = 1 << 20

	whoamiQuery = `query Whoami { whoami { id isOperator } }`
)

// Config says where the Admin API lives and which tenant acts on it.
type Config struct {
	URL       string
	TenantID  string
	APISecret string

	// HTTPClient and Now are optional; New substitutes the defaults.
	HTTPClient *http.Client
	Now        func() time.Time
}

// Client is an Admin API client. It is safe for concurrent use.
type Client struct {
	url        string
	tenantID   string
	apiSecret  string
	httpClient *http.Client
	now        func() time.Time

	mu           sync.Mutex
	lastSignedAt int64
}

// Whoami identifies the tenant the credentials belong to.
type Whoami struct {
	ID         string `json:"id"`
	IsOperator bool   `json:"isOperator"`
}

// New validates the settings and returns a client.
func New(cfg Config) (*Client, error) {
	if err := validateURL(cfg.URL); err != nil {
		return nil, err
	}
	if cfg.TenantID == "" {
		return nil, errors.New("rafikiadmin: the tenant ID is empty (RAFIKI_TENANT_ID)")
	}
	if cfg.APISecret == "" {
		return nil, errors.New("rafikiadmin: the API secret is empty (RAFIKI_ADMIN_API_SECRET)")
	}

	c := &Client{
		url:        cfg.URL,
		tenantID:   cfg.TenantID,
		apiSecret:  cfg.APISecret,
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
		now:        time.Now,
	}
	if cfg.HTTPClient != nil {
		c.httpClient = cfg.HTTPClient
	}
	if cfg.Now != nil {
		c.now = cfg.Now
	}

	return c, nil
}

func validateURL(raw string) error {
	if raw == "" {
		return errors.New("rafikiadmin: the Admin API URL is empty (RAFIKI_ADMIN_URL)")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("rafikiadmin: the Admin API URL %q cannot be parsed: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("rafikiadmin: the Admin API URL %q must start with http:// or https://", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("rafikiadmin: the Admin API URL %q has no host", raw)
	}

	return nil
}

// Do performs one GraphQL operation. When out is not nil it receives the "data"
// object of the reply.
func (c *Client) Do(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := canonicalBody(query, variables)
	if err != nil {
		return err
	}

	timestamp := c.nextTimestamp()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("rafikiadmin: building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("tenant-id", c.tenantID)
	req.Header.Set("Signature", c.signatureHeader(timestamp, body))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("rafikiadmin: calling the Admin API: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("rafikiadmin: reading the Admin API reply: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rafikiadmin: the Admin API answered %d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), summarize(raw))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("rafikiadmin: the Admin API reply is not GraphQL JSON: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			messages = append(messages, e.Message)
		}
		return fmt.Errorf("rafikiadmin: the Admin API rejected the operation: %s", strings.Join(messages, "; "))
	}

	if out == nil {
		return nil
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("rafikiadmin: the Admin API replied without data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("rafikiadmin: decoding the data object: %w", err)
	}

	return nil
}

// Whoami asks the Admin API which tenant the credentials identify.
func (c *Client) Whoami(ctx context.Context) (Whoami, error) {
	var data struct {
		Whoami Whoami `json:"whoami"`
	}
	if err := c.Do(ctx, whoamiQuery, nil, &data); err != nil {
		return Whoami{}, err
	}

	return data.Whoami, nil
}

// canonicalBody renders the body in the canonical JSON form Rafiki hashes:
// sorted keys at every level, no whitespace, no HTML escaping and the two
// separators JCS leaves literal.
//
// It normalizes through generic values on purpose. Go sorts the keys of a map but
// keeps the declaration order of a struct, so encoding a nested struct straight
// to JSON would produce a body that differs from the canonical form Rafiki
// re-computes over the body it parsed, and the signature would not match.
//
// Normalizing goes through JSON scalars, so a payload must not carry an integer
// beyond 2^53. Amounts travel as strings (the UInt64 type encodes them that way)
// and everything else here is a small integer or a string.
func canonicalBody(query string, variables map[string]any) ([]byte, error) {
	if variables == nil {
		variables = map[string]any{}
	}

	direct, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, fmt.Errorf("rafikiadmin: encoding the request body: %w", err)
	}

	var generic any
	if err := json.Unmarshal(direct, &generic); err != nil {
		return nil, fmt.Errorf("rafikiadmin: normalizing the request body: %w", err)
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(generic); err != nil {
		return nil, fmt.Errorf("rafikiadmin: encoding the request body: %w", err)
	}

	// Encode appends a newline; the canonical form has no trailing whitespace.
	encoded := bytes.TrimRight(buf.Bytes(), "\n")

	// Go escapes U+2028 and U+2029 for JavaScript safety, and JCS leaves them
	// literal, so the escaped form would not match the canonical form Rafiki
	// recomputes before hashing.
	return stripNonJCSescaping(encoded), nil
}

// stripNonJCSescaping turns the two escapes JCS does not produce back into the
// characters they hold.
//
// It walks the encoded JSON instead of replacing text blindly: two backslashes
// escape each other, so the escape that follows an odd one is the only real
// candidate. A payload that really contains the text "\u2028" keeps its escape
// and stays valid JSON with the same value.
func stripNonJCSescaping(encoded []byte) []byte {
	out := make([]byte, 0, len(encoded))

	for i := 0; i < len(encoded); {
		if encoded[i] != '\\' {
			out = append(out, encoded[i])
			i++
			continue
		}

		if i+1 < len(encoded) && encoded[i+1] == '\\' {
			out = append(out, '\\', '\\')
			i += 2
			continue
		}

		rest := encoded[i:]
		if len(rest) >= 6 && bytes.HasPrefix(rest, []byte(`\u2028`)) {
			out = append(out, "\u2028"...)
			i += 6
			continue
		}
		if len(rest) >= 6 && bytes.HasPrefix(rest, []byte(`\u2029`)) {
			out = append(out, "\u2029"...)
			i += 6
			continue
		}

		out = append(out, encoded[i])
		i++
	}

	return out
}

// signatureHeader builds the header Rafiki verifies.
func (c *Client) signatureHeader(timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)

	return fmt.Sprintf("t=%d, v%d=%s", timestamp, signatureVersion, hex.EncodeToString(mac.Sum(nil)))
}

// nextTimestamp returns the current time in milliseconds, never repeating one
// and never going backwards: Rafiki stores the whole Signature header, so a
// repeated one inside its TTL window is refused as a replay.
func (c *Client) nextTimestamp() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	timestamp := c.now().UnixMilli()
	if timestamp <= c.lastSignedAt {
		timestamp = c.lastSignedAt + 1
	}
	c.lastSignedAt = timestamp

	return timestamp
}

func summarize(raw []byte) string {
	const limit = 200

	text := strings.TrimSpace(string(raw))
	if len(text) > limit {
		return text[:limit] + "..."
	}

	return text
}
