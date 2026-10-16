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
	"strconv"
	"strings"
	"time"

	openpayments "github.com/interledger/open-payments-go"
	as "github.com/interledger/open-payments-go/generated/authserver"
	rs "github.com/interledger/open-payments-go/generated/resourceserver"

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

// Amount is a monetary amount the way Open Payments expresses it: the value in
// the smallest unit of the asset, as a string, next to the asset and its scale.
// The callers of this package speak whole pesos; SmallestUnit does the conversion.
type Amount struct {
	AssetCode  string
	AssetScale int
	Value      string
}

// SmallestUnit converts a whole-pesos amount into the value Open Payments needs:
// a string with the amount in the smallest unit of the asset, that is the amount
// multiplied by 10^assetScale. 200000 pesos on a COP wallet of scale 2 is
// "20000000".
//
// A negative scale or an amount that overflows once scaled cannot come from a
// caller that speaks whole pesos of a real asset, and both are reported as
// errors: a panic here would take down the running backend.
func SmallestUnit(amount int64, assetScale int) (string, error) {
	if assetScale < 0 {
		return "", fmt.Errorf("opclient: assetScale=%d is negative", assetScale)
	}

	value := amount
	for i := 0; i < assetScale; i++ {
		next := value * 10
		if next/10 != value {
			return "", fmt.Errorf("opclient: amount %d with assetScale %d overflows the smallest unit", amount, assetScale)
		}
		value = next
	}

	return strconv.FormatInt(value, 10), nil
}

// CreateIncomingPaymentsParams names the two collection points of a withdrawal:
// the store charges monto + comision_tendero and the platform charges
// comision_plataforma on its own COP wallet (contract 8.4).
type CreateIncomingPaymentsParams struct {
	StoreWalletAddress    string
	StoreAmountCOP        int64
	PlatformWalletAddress string
	PlatformAmountCOP     int64
}

// IncomingPayment is one collection point: where the money lands and how much
// was asked for.
type IncomingPayment struct {
	URL           string
	WalletAddress string
	Amount        Amount
}

// IncomingPayments are the two collection points of one withdrawal.
type IncomingPayments struct {
	Store    IncomingPayment
	Platform IncomingPayment
}

// CreateIncomingPayments runs the collection half of the withdrawal (contract
// 8.4): for each of the two accounts it asks that account's authorization server
// for a non-interactive incoming-payment grant and creates its incoming payment
// for the exact whole-pesos amount, already converted to the smallest unit of
// the account's asset.
func (c *Client) CreateIncomingPayments(ctx context.Context, params CreateIncomingPaymentsParams) (IncomingPayments, error) {
	points := []collectionPoint{
		{role: "the store", walletAddressParam: "StoreWalletAddress", amountParam: "StoreAmountCOP", walletAddress: params.StoreWalletAddress, amountCOP: params.StoreAmountCOP},
		{role: "the platform", walletAddressParam: "PlatformWalletAddress", amountParam: "PlatformAmountCOP", walletAddress: params.PlatformWalletAddress, amountCOP: params.PlatformAmountCOP},
	}

	var result IncomingPayments
	// Both collection points are validated before the network is touched, so a
	// bad second point cannot leave the first one already collected.
	for _, point := range points {
		if _, err := validateWalletAddressParam(point.walletAddressParam, point.walletAddress); err != nil {
			return IncomingPayments{}, err
		}
		if err := validateAmountParam(point.amountParam, point.amountCOP); err != nil {
			return IncomingPayments{}, err
		}
	}
	for i, point := range points {
		payment, err := c.createIncomingPayment(ctx, point)
		if err != nil {
			return IncomingPayments{}, err
		}
		if i == 0 {
			result.Store = payment
		} else {
			result.Platform = payment
		}
	}
	return result, nil
}

// collectionPoint is one of the two accounts the withdrawal collects from. The
// parameter names travel with the values so every error can name its parameter.
type collectionPoint struct {
	role               string
	walletAddressParam string
	amountParam        string
	walletAddress      string
	amountCOP          int64
}

func (c *Client) createIncomingPayment(ctx context.Context, point collectionPoint) (IncomingPayment, error) {
	address, err := c.ResolveWalletAddress(ctx, point.walletAddress)
	if err != nil {
		return IncomingPayment{}, fmt.Errorf("opclient: %s: %w", point.role, err)
	}

	token, err := c.requestIncomingPaymentGrant(ctx, point.role, address.AuthServer, address.URL)
	if err != nil {
		return IncomingPayment{}, err
	}

	value, err := SmallestUnit(point.amountCOP, address.AssetScale)
	if err != nil {
		return IncomingPayment{}, fmt.Errorf("opclient: %s: %w", point.amountParam, err)
	}
	amount := rs.Amount{AssetCode: address.AssetCode, AssetScale: address.AssetScale, Value: value}

	payment, err := c.sdk.IncomingPayment.Create(ctx, openpayments.IncomingPaymentCreateParams{
		BaseURL:     address.URL,
		AccessToken: token,
		Payload: rs.CreateIncomingPaymentRequest{
			WalletAddressSchema: address.URL,
			IncomingAmount:      &amount,
		},
	})
	if err != nil {
		return IncomingPayment{}, fmt.Errorf("opclient: creating the incoming payment of %s (%s): %w", point.role, address.URL, err)
	}

	if payment.Id == nil || *payment.Id == "" {
		return IncomingPayment{}, fmt.Errorf("opclient: the resource server of %s did not return the URL of the incoming payment", point.role)
	}

	wallet := address.URL
	if payment.WalletAddress != nil && *payment.WalletAddress != "" {
		wallet = *payment.WalletAddress
	}

	return IncomingPayment{
		URL:           *payment.Id,
		WalletAddress: wallet,
		Amount:        Amount{AssetCode: address.AssetCode, AssetScale: address.AssetScale, Value: value},
	}, nil
}

// requestIncomingPaymentGrant asks the authorization server of one collection
// point for a non-interactive grant that can create, read and complete incoming
// payments on that one wallet address, and returns the access token.
func (c *Client) requestIncomingPaymentGrant(ctx context.Context, role, authServer, walletAddress string) (string, error) {
	identifier := walletAddress
	var item as.AccessItem
	if err := item.FromAccessIncoming(as.AccessIncoming{
		Type:       as.IncomingPayment,
		Actions:    []as.AccessIncomingActions{as.AccessIncomingActionsCreate, as.AccessIncomingActionsRead, as.AccessIncomingActionsComplete},
		Identifier: &identifier,
	}); err != nil {
		return "", fmt.Errorf("opclient: building the incoming-payment access of %s: %w", role, err)
	}
	return c.requestGrant(ctx, role, authServer, item)
}

// requestQuoteGrant asks the authorization server of the client wallet for a
// non-interactive grant that can create and read quotes. A quote access has no
// identifier: the wallet address of the client already scopes it.
func (c *Client) requestQuoteGrant(ctx context.Context, role, authServer string) (string, error) {
	var item as.AccessItem
	if err := item.FromAccessQuote(as.AccessQuote{
		Type:    as.Quote,
		Actions: []as.AccessQuoteActions{as.Create, as.Read},
	}); err != nil {
		return "", fmt.Errorf("opclient: building the quote access of %s: %w", role, err)
	}
	return c.requestGrant(ctx, role, authServer, item)
}

func (c *Client) requestGrant(ctx context.Context, role, authServer string, item as.AccessItem) (string, error) {
	var body as.GrantRequest
	if err := body.FromGrantRequestWithAccessToken(as.GrantRequestWithAccessToken{
		AccessToken: as.AccessTokenRequest{Access: as.Access{item}},
	}); err != nil {
		return "", fmt.Errorf("opclient: building the grant request of %s: %w", role, err)
	}

	// The SDK identifies the client instance with the wallet address this client
	// was built with. The collection runs unattended, so the grant has to come
	// back with a token the client can use right away.
	grant, err := c.sdk.Grant.Request(ctx, openpayments.GrantRequestParams{URL: authServer, RequestBody: body})
	if err != nil {
		return "", fmt.Errorf("opclient: requesting the grant of %s at %s: %w", role, authServer, err)
	}
	if grant.Interact != nil {
		return "", fmt.Errorf("opclient: the auth server of %s answered with an interactive grant, and the collection needs a non-interactive one", role)
	}
	if grant.AccessToken == nil || grant.AccessToken.Value == "" {
		return "", fmt.Errorf("opclient: the auth server of %s answered without an access token", role)
	}
	return grant.AccessToken.Value, nil
}

// CreateQuotesParams names the client wallet that pays and the two incoming
// payments the two quotes pay into (contract 8.5). The amounts are the same
// whole pesos of the collection points.
type CreateQuotesParams struct {
	ClientWalletAddress string
	StoreReceiver       string
	StoreAmountCOP      int64
	PlatformReceiver    string
	PlatformAmountCOP   int64
}

// Quote is one quote of the withdrawal: the client pays this much, in its own
// asset, into this incoming payment, until this expiry.
type Quote struct {
	URL           string
	DebitAmount   Amount
	ReceiveAmount Amount
	ExpiresAt     string
}

// Quotes are the two quotes of one withdrawal with the total debit in the
// client's own asset and the earliest of the two expiries (contract 8.5:
// debito and vence_en).
type Quotes struct {
	Store     Quote
	Platform  Quote
	Debit     Amount
	ExpiresAt string
}

// CreateQuotes runs the quotation half of the withdrawal (contract 8.5): it asks
// the client's authorization server for a non-interactive quote grant and
// creates two quotes from the client's wallet, one to each incoming payment,
// both with method ilp and the debit amount in the smallest unit of the
// client's asset.
func (c *Client) CreateQuotes(ctx context.Context, params CreateQuotesParams) (Quotes, error) {
	clientWallet, err := validateWalletAddressParam("ClientWalletAddress", params.ClientWalletAddress)
	if err != nil {
		return Quotes{}, err
	}
	if err := validateAmountParam("StoreAmountCOP", params.StoreAmountCOP); err != nil {
		return Quotes{}, err
	}
	if err := validateAmountParam("PlatformAmountCOP", params.PlatformAmountCOP); err != nil {
		return Quotes{}, err
	}
	if err := validateReceiverParam("StoreReceiver", params.StoreReceiver); err != nil {
		return Quotes{}, err
	}
	if err := validateReceiverParam("PlatformReceiver", params.PlatformReceiver); err != nil {
		return Quotes{}, err
	}

	address, err := c.ResolveWalletAddress(ctx, clientWallet)
	if err != nil {
		return Quotes{}, fmt.Errorf("opclient: the client: %w", err)
	}

	token, err := c.requestQuoteGrant(ctx, "the client", address.AuthServer)
	if err != nil {
		return Quotes{}, err
	}

	store, err := c.createQuote(ctx, address, token, params.StoreReceiver, "StoreAmountCOP", params.StoreAmountCOP)
	if err != nil {
		return Quotes{}, err
	}
	platform, err := c.createQuote(ctx, address, token, params.PlatformReceiver, "PlatformAmountCOP", params.PlatformAmountCOP)
	if err != nil {
		return Quotes{}, err
	}

	debit, err := addAmounts(store.DebitAmount, platform.DebitAmount, address.AssetCode, address.AssetScale)
	if err != nil {
		return Quotes{}, err
	}
	expiresAt, err := earliestExpiry(store.ExpiresAt, platform.ExpiresAt)
	if err != nil {
		return Quotes{}, err
	}

	return Quotes{Store: store, Platform: platform, Debit: debit, ExpiresAt: expiresAt}, nil
}

func (c *Client) createQuote(ctx context.Context, address WalletAddress, token, receiver, amountParam string, amountCOP int64) (Quote, error) {
	value, err := SmallestUnit(amountCOP, address.AssetScale)
	if err != nil {
		return Quote{}, fmt.Errorf("opclient: %s: %w", amountParam, err)
	}

	// A named variant travels directly: a quote by receiver with a debit amount
	// fixed up front, so the caller knows the total before approving it.
	quote, err := c.sdk.Quote.Create(ctx, openpayments.QuoteCreateParams{
		BaseURL:     address.URL,
		AccessToken: token,
		Payload: rs.CreateQuoteRequestWithDebitAmount{
			WalletAddressSchema: address.URL,
			Method:              rs.PaymentMethod("ilp"),
			Receiver:            rs.Receiver(receiver),
			DebitAmount:         rs.Amount{AssetCode: address.AssetCode, AssetScale: address.AssetScale, Value: value},
		},
	})
	if err != nil {
		return Quote{}, fmt.Errorf("opclient: creating the quote from %s to %s: %w", address.URL, receiver, err)
	}

	if quote.Id == nil || *quote.Id == "" {
		return Quote{}, fmt.Errorf("opclient: the resource server of the client did not return the URL of the quote to %s", receiver)
	}
	if quote.ExpiresAt == nil || *quote.ExpiresAt == "" {
		return Quote{}, fmt.Errorf("opclient: the quote from %s to %s has no expiry", address.URL, receiver)
	}

	return Quote{
		URL:           *quote.Id,
		DebitAmount:   toAmount(quote.DebitAmount),
		ReceiveAmount: toAmount(quote.ReceiveAmount),
		ExpiresAt:     *quote.ExpiresAt,
	}, nil
}

// addAmounts adds the debit of the two quotes. Both come from the same client
// wallet, so they share the asset, and their values are integers as strings.
func addAmounts(a, b Amount, assetCode string, assetScale int) (Amount, error) {
	left, err := strconv.ParseUint(a.Value, 10, 64)
	if err != nil {
		return Amount{}, fmt.Errorf("opclient: adding the debits of the quotes: %q is not an unsigned amount: %w", a.Value, err)
	}
	right, err := strconv.ParseUint(b.Value, 10, 64)
	if err != nil {
		return Amount{}, fmt.Errorf("opclient: adding the debits of the quotes: %q is not an unsigned amount: %w", b.Value, err)
	}
	sum := left + right
	if sum < left {
		return Amount{}, fmt.Errorf("opclient: adding the debits of the quotes: %d + %d overflows", left, right)
	}
	return Amount{AssetCode: assetCode, AssetScale: assetScale, Value: strconv.FormatUint(sum, 10)}, nil
}

// earliestExpiry returns the earliest of the two expiries: that is how long the
// caller has before one of the two quotes becomes invalid.
func earliestExpiry(a, b string) (string, error) {
	aTime, err := time.Parse(time.RFC3339, a)
	if err != nil {
		return "", fmt.Errorf("opclient: the expiry %q of a quote is not ISO 8601: %w", a, err)
	}
	bTime, err := time.Parse(time.RFC3339, b)
	if err != nil {
		return "", fmt.Errorf("opclient: the expiry %q of a quote is not ISO 8601: %w", b, err)
	}
	if aTime.Before(bTime) {
		return a, nil
	}
	return b, nil
}

// validateWalletAddressParam checks a wallet address the caller names and returns
// it trimmed; the error names the parameter, not the internal helper.
func validateWalletAddressParam(name, walletAddress string) (string, error) {
	trimmed, _, err := parseWalletAddressURL(walletAddress)
	if err != nil {
		return "", fmt.Errorf("opclient: %s: %w", name, err)
	}
	return trimmed, nil
}

func validateAmountParam(name string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("opclient: %s must be a positive amount in whole pesos, got %d", name, amount)
	}
	return nil
}

// validateReceiverParam checks that a receiver names a URL an incoming payment
// can be paid into.
func validateReceiverParam(name, receiver string) error {
	trimmed := strings.TrimSpace(receiver)
	if trimmed == "" {
		return fmt.Errorf("opclient: %s is empty", name)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("opclient: %s=%q is not the URL of an incoming payment", name, receiver)
	}
	return nil
}

func toAmount(a rs.Amount) Amount {
	return Amount{AssetCode: a.AssetCode, AssetScale: a.AssetScale, Value: a.Value}
}
