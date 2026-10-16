// Package opclient is the Open Payments client of Caja de Barrio: the role that
// resolves wallet addresses and runs the withdrawal against the ASE with the
// official Go SDK.
//
// It exists because the ASE has to publish its wallet addresses in https, which is
// the only scheme Rafiki accepts, while the local stack has no TLS on that port.
// The client calls that one host over http; see downgradeTransport.
package opclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	openpayments "github.com/interledger/open-payments-go"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opkeys"
)

// Config is what this client needs to identify itself at the ASE.
type Config struct {
	// WalletAddressURL is the wallet address this backend acts as. It is the full
	// URL, not the $ilp form: the SDK refuses an address that starts with '$'.
	WalletAddressURL string

	// PrivateKeyPath is the PKCS#8 PEM file of the Ed25519 key registered on that
	// wallet address.
	PrivateKeyPath string

	// KeyID is the kid the key is registered under. Empty means the RFC 7638
	// thumbprint of the key, which is the name the seed registers it with.
	KeyID string
}

// WalletAddress is the document the ASE publishes for one wallet address, reduced
// to what the withdrawal needs: where to ask for grants, where to call the
// resources and which asset the amounts are expressed in.
type WalletAddress struct {
	URL            string
	AuthServer     string
	ResourceServer string
	AssetCode      string
	AssetScale     int
}

// Client is the authenticated Open Payments client: it signs every request with
// the key of the wallet address it acts as.
type Client struct {
	sdk *openpayments.AuthenticatedClient
}

// New builds the authenticated client from the configured key.
//
// It reports anything it cannot use before the first request: a missing wallet
// address, a key file that is not there or one that is not an Ed25519 PEM.
func New(cfg Config) (*Client, error) {
	addressURL, parsed, err := parseWalletAddressURL(cfg.WalletAddressURL)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(cfg.PrivateKeyPath) == "" {
		return nil, errors.New("opclient: PrivateKeyPath is empty")
	}

	// The key is read here, and not only by the SDK, for two reasons: it reports a
	// file that is not there by its name, and it names an empty KeyID after the
	// thumbprint of the key, which is the name the seed registers it under.
	key, err := opkeys.LoadFile(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}

	kid := strings.TrimSpace(cfg.KeyID)
	if kid == "" {
		kid = key.Kid()
	}

	httpClient := &http.Client{
		Transport: downgradeTransport{host: parsed.Host, next: http.DefaultTransport},
	}

	sdk, err := openpayments.NewAuthenticatedClient(addressURL, cfg.PrivateKeyPath, kid, openpayments.WithHTTPClientAuthed(httpClient))
	if err != nil {
		return nil, fmt.Errorf("opclient: building the authenticated client: %w", err)
	}

	return &Client{sdk: sdk}, nil
}

// ResolveWalletAddress reads the document the ASE publishes for a wallet address.
//
// It is the first step of every flow: the document carries the authorization and
// resource servers of that account, and the asset its amounts are expressed in.
func (c *Client) ResolveWalletAddress(ctx context.Context, walletAddressURL string) (WalletAddress, error) {
	document, err := c.sdk.WalletAddress.Get(ctx, openpayments.WalletAddressGetParams{URL: walletAddressURL})
	if err != nil {
		return WalletAddress{}, fmt.Errorf("opclient: resolving the wallet address %s: %w", walletAddressURL, err)
	}

	address := WalletAddress{
		URL:        walletAddressURL,
		AssetCode:  document.AssetCode,
		AssetScale: document.AssetScale,
	}
	// The document is the authority on how the wallet address names itself, so its
	// id wins over the URL the caller asked for.
	if document.Id != nil && *document.Id != "" {
		address.URL = *document.Id
	}
	if document.AuthServer != nil {
		address.AuthServer = *document.AuthServer
	}
	if document.ResourceServer != nil {
		address.ResourceServer = *document.ResourceServer
	}

	// Without both servers there is no flow to run, and the caller would otherwise
	// find out inside the authorization server with a much worse message.
	if address.AuthServer == "" || address.ResourceServer == "" {
		return WalletAddress{}, fmt.Errorf("opclient: the wallet address %s has no authorization or resource server", walletAddressURL)
	}
	if address.AssetCode == "" {
		return WalletAddress{}, fmt.Errorf("opclient: the wallet address %s has no asset code", walletAddressURL)
	}

	return address, nil
}

// parseWalletAddressURL validates what this client identifies itself by and
// returns it unchanged plus its parsed form.
func parseWalletAddressURL(raw string) (string, *url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, errors.New("opclient: WalletAddressURL is empty")
	}
	if strings.HasPrefix(trimmed, "$") {
		return "", nil, fmt.Errorf("opclient: WalletAddressURL=%q is in the $ilp form, and the SDK needs the full URL", raw)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", nil, fmt.Errorf("opclient: WalletAddressURL=%q is not a URL: %w", raw, err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", nil, fmt.Errorf("opclient: WalletAddressURL=%q has no http or https scheme", raw)
	}
	if parsed.Host == "" {
		return "", nil, fmt.Errorf("opclient: WalletAddressURL=%q has no host", raw)
	}

	return trimmed, parsed, nil
}

// downgradeTransport sends over http the requests addressed in https to the host of
// the wallet address this client acts as, and leaves every other request alone.
//
// Rafiki refuses any wallet address URL that is not https, so the ASE has to
// publish its wallet addresses in https; but the local stack has no TLS on that
// port, so the document is unreachable in https (see L12 and L15 of the plan). Only
// that one host loses its scheme: the servers the document announces are already
// http, and the wallet address of another ASE keeps the https it published.
type downgradeTransport struct {
	host string
	next http.RoundTripper
}

func (t downgradeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" && req.URL.Host == t.host {
		// The request is cloned because a RoundTripper must not modify the request
		// it was handed, and the caller may retry it.
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = "http"
		req = cloned
	}

	return t.next.RoundTrip(req)
}
