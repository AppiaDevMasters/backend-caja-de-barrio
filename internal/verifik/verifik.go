// Package verifik implements the client of Verifik's Colombian citizen endpoint
// that the identity routes use to validate a document against the civil registry.
//
// It is a client and nothing else: it holds no configuration of its own, opens no
// routes and mutates no shared state. The token arrives as a constructor
// parameter, so this package never reads the environment.
package verifik

import (
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
	// defaultBaseURL is the host Verifik documents for the civilian registry
	// endpoint. It is a field of Client so a test can point it at its own server.
	defaultBaseURL = "https://api.verifik.co"

	// citizenPath is the endpoint of the Colombian citizen query. The provider
	// spells it with the Spanish word for the document it looks up.
	citizenPath = "/v2/co/cedula"

	// requestTimeout bounds one query.
	requestTimeout = 15 * time.Second

	// maxResponseBytes keeps a misbehaving endpoint from filling memory.
	maxResponseBytes = 1 << 20

	// summaryLimit bounds how much of a provider error body is kept, because it
	// ends up in logs and in a reply to the agent.
	summaryLimit = 200
)

// Accepted document types. The provider takes these two on this endpoint and
// rejects NIT there.
const (
	DocumentTypeCC  = "CC"
	DocumentTypePPT = "PPT"
)

// Document is the outcome of one query against the civil registry.
type Document struct {
	// Valid is true only when the provider returned civil data for the document.
	// A missing document is not an error: it is this value being false.
	Valid bool

	// FullName is the full name the registry holds for the document.
	FullName string
}

// ErrInvalidDocument reports a document the provider cannot accept. It is a
// local format rule, so it never reaches the network. The caller maps it to
// DATOS_INVALIDOS (400).
var ErrInvalidDocument = errors.New("verifik: the document type or number is not valid")

// Error is a provider failure: transport, authentication, a status the endpoint
// does not define, or a body that cannot be read. The caller maps it to
// PROVEEDOR (502).
type Error struct {
	// StatusCode is the HTTP status the provider answered with, or 0 when the
	// request never completed.
	StatusCode int

	// Message is what the provider said, bounded, so a caller can log it. It
	// never carries the token: the body comes from the reply, not the request.
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("verifik: the provider answered %d: %s", e.StatusCode, e.Message)
}

// Client is a Verifik client. It is safe for concurrent use.
type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

// New returns a client that authenticates with the given token. An empty token
// fails here, at startup, instead of at the first query.
func New(token string) (*Client, error) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return nil, errors.New("verifik: the token is empty (VERIFIK_TOKEN)")
	}

	return &Client{
		token:      trimmed,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: requestTimeout},
	}, nil
}

// VerifyDocument queries the civil registry for one document.
//
// A document the registry does not hold comes back as Valid false and a nil
// error: that is an answer, not a failure, and the contract reports it as a 200.
// Only a provider problem returns an error.
func (c *Client) VerifyDocument(ctx context.Context, documentType, documentNumber string) (Document, error) {
	documentType = strings.ToUpper(strings.TrimSpace(documentType))
	documentNumber = strings.TrimSpace(documentNumber)
	if err := validateDocument(documentType, documentNumber); err != nil {
		return Document{}, err
	}

	endpoint, err := url.Parse(strings.TrimSuffix(c.baseURL, "/") + citizenPath)
	if err != nil {
		return Document{}, fmt.Errorf("verifik: building the request URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("documentType", documentType)
	query.Set("documentNumber", documentNumber)
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Document{}, fmt.Errorf("verifik: building the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Document{}, fmt.Errorf("verifik: calling the provider: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Document{}, fmt.Errorf("verifik: reading the provider reply: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// The registry does not hold that document. The caller reports
		// valid:false with a 200.
		return Document{}, nil
	default:
		return Document{}, &Error{StatusCode: resp.StatusCode, Message: summarize(raw)}
	}

	var body struct {
		Data *struct {
			DocumentNumber string `json:"documentNumber"`
			DocumentType   string `json:"documentType"`
			FullName       string `json:"fullName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Document{}, fmt.Errorf("verifik: decoding the provider reply: %w", err)
	}

	// A 200 without civil data is still not a valid document: the caller must not
	// treat a shaped-but-empty reply as a match.
	if body.Data == nil || strings.TrimSpace(body.Data.FullName) == "" {
		return Document{}, nil
	}

	return Document{Valid: true, FullName: strings.TrimSpace(body.Data.FullName)}, nil
}

// validateDocument applies the format rule the provider documents: CC or PPT, and
// a number of 5 to 10 digits with no spaces or periods.
func validateDocument(documentType, documentNumber string) error {
	if documentType != DocumentTypeCC && documentType != DocumentTypePPT {
		return fmt.Errorf("%w: documentType %q is neither %s nor %s",
			ErrInvalidDocument, documentType, DocumentTypeCC, DocumentTypePPT)
	}
	if len(documentNumber) < 5 || len(documentNumber) > 10 {
		// The number is never echoed: the contract requires a document to be shown
		// masked, and this message reaches the log and the agent's reply.
		return fmt.Errorf("%w: documentNumber must be 5 to 10 characters", ErrInvalidDocument)
	}
	for _, digit := range documentNumber {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("%w: documentNumber must be digits only", ErrInvalidDocument)
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
