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
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

// validateQuoteURLParam checks that a parameter names a URL a quote lives at,
// the same shape a receiver has but named for what it points to.
func validateQuoteURLParam(name, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("opclient: %s is empty", name)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("opclient: %s=%q is not the URL of a quote", name, raw)
	}
	return nil
}

// OutgoingPaymentGrantParams names the interactive grant of the cupo (contract
// 8.2): one grant of outgoing-payment that caps what the client wallet can
// debit, in whole units of the client's own asset, so the caller picks the
// ceiling the contract fixes (COP 1.000.000 or USD 300).
type OutgoingPaymentGrantParams struct {
	// ClientWalletAddress is the wallet the cupo is granted on. It scopes the
	// access item and names the asset the debit limit is converted into.
	ClientWalletAddress string

	// DebitLimit is the ceiling of the cupo in whole units of the client's own
	// asset: the caller decides which contract ceiling applies.
	DebitLimit int64

	// Interval is the GNAP repeating interval of the cupo, for example
	// R/2026-10-16T00:00:00Z/P7D. The caller builds it, because it depends on
	// the day the cupo is granted.
	Interval string

	// FinishURL is where the wallet sends the user back. The grant id of the
	// caller is appended as a query parameter, so the callback can be matched
	// to the cupo it belongs to.
	FinishURL string

	// GrantID is the caller's own identifier of the cupo; the finish URI carries
	// it so the callback can find the grant it continues.
	GrantID string
}

// OutgoingPaymentGrant is everything the callback needs to finish an interactive
// grant (contract 8.3): where the user approves, how to continue the grant, and
// the two nonces the callback hash binds.
type OutgoingPaymentGrant struct {
	// ID is the caller's grant id, echoed back so the cupo can be stored under
	// it before the callback arrives.
	ID string

	// RedirectURL is where the caller sends the user to approve the grant.
	RedirectURL string

	// ContinueURI and ContinueToken identify and authorize the continuation
	// request the callback makes with the interact_ref of the wallet.
	ContinueURI   string
	ContinueToken string

	// ClientNonce is the nonce this client generated for the finish block; the
	// caller keeps it to verify the callback hash. ServerNonce is the finish key
	// the authorization server answered with; both go into the hash.
	ClientNonce string
	ServerNonce string

	// AuthServer is the authorization server the grant was asked of; the hash
	// binds it, and the callback verifies against the same URL.
	AuthServer string
}

// RequestOutgoingPaymentGrant asks the client wallet's authorization server for
// an interactive grant of outgoing-payment (contract 8.2): the access item is
// scoped to the client's wallet address, the debit limit travels in the
// smallest unit of its asset next to the interval of the cupo, and the finish
// block sends the user back to the caller's finish URL with the grant id.
func (c *Client) RequestOutgoingPaymentGrant(ctx context.Context, params OutgoingPaymentGrantParams) (OutgoingPaymentGrant, error) {
	// Everything is validated before the wallet address is even resolved, so a
	// bad parameter cannot leave a half-asked grant behind.
	clientWallet, err := validateWalletAddressParam("ClientWalletAddress", params.ClientWalletAddress)
	if err != nil {
		return OutgoingPaymentGrant{}, err
	}
	if params.DebitLimit <= 0 {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: DebitLimit must be a positive amount in whole units of the client's asset, got %d", params.DebitLimit)
	}
	if strings.TrimSpace(params.Interval) == "" {
		return OutgoingPaymentGrant{}, errors.New("opclient: Interval is empty")
	}
	finishURL, err := validateFinishURLParam(params.FinishURL)
	if err != nil {
		return OutgoingPaymentGrant{}, err
	}
	grantID, err := validateGrantIDParam(params.GrantID)
	if err != nil {
		return OutgoingPaymentGrant{}, err
	}

	address, err := c.ResolveWalletAddress(ctx, clientWallet)
	if err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: the client: %w", err)
	}

	value, err := SmallestUnit(params.DebitLimit, address.AssetScale)
	if err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: DebitLimit: %w", err)
	}

	// The client nonce is generated here and returned to the caller, because the
	// callback hash (contract 8.3) binds it and nobody else has it.
	clientNonce, err := NewNonce()
	if err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: generating the client nonce: %w", err)
	}

	// The grant id of the caller travels in the finish URI, so the wallet's
	// callback can be matched to the cupo it belongs to (contract 8.2).
	finishURI, err := addGrantIDToURL(finishURL, grantID)
	if err != nil {
		return OutgoingPaymentGrant{}, err
	}

	identifier := clientWallet
	interval := params.Interval
	var limits as.LimitsOutgoing
	if err := limits.FromLimitsOutgoingDebitAmount(as.LimitsOutgoingDebitAmount{
		DebitAmount: as.Amount{AssetCode: address.AssetCode, AssetScale: address.AssetScale, Value: value},
		Interval:    &interval,
	}); err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: building the debit limit of the cupo: %w", err)
	}

	var item as.AccessItem
	if err := item.FromAccessOutgoing(as.AccessOutgoing{
		Type:       as.OutgoingPayment,
		Actions:    []as.AccessOutgoingActions{as.AccessOutgoingActionsCreate, as.AccessOutgoingActionsRead},
		Identifier: identifier,
		Limits:     &limits,
	}); err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: building the outgoing-payment access of the client: %w", err)
	}

	var body as.GrantRequest
	if err := body.FromGrantRequestWithAccessToken(as.GrantRequestWithAccessToken{
		AccessToken: as.AccessTokenRequest{Access: as.Access{item}},
		Interact: &as.InteractRequest{
			Finish: &struct {
				Method as.InteractRequestFinishMethod `json:"method"`
				Nonce  string                         `json:"nonce"`
				Uri    string                         `json:"uri"`
			}{Method: as.InteractRequestFinishMethodRedirect, Nonce: clientNonce, Uri: finishURI},
			Start: []as.InteractRequestStart{as.InteractRequestStartRedirect},
		},
	}); err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: building the interactive grant request of the client: %w", err)
	}

	grant, err := c.sdk.Grant.Request(ctx, openpayments.GrantRequestParams{URL: address.AuthServer, RequestBody: body})
	if err != nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: requesting the interactive grant of the client at %s: %w", address.AuthServer, err)
	}

	// The mirror of requestGrant: the collection needs a non-interactive grant,
	// and the cupo needs an interactive one. Without an interact block there is
	// nothing to send the user to and nothing to continue.
	if grant.Interact == nil {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: the auth server of the client (%s) answered without an interactive grant, and the cupo needs one", clientWallet)
	}
	if grant.Interact.Redirect == "" || grant.Interact.Finish == "" {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: the auth server of the client (%s) answered an interactive grant without a redirect or finish key", clientWallet)
	}
	if grant.Continue.Uri == "" || grant.Continue.AccessToken.Value == "" {
		return OutgoingPaymentGrant{}, fmt.Errorf("opclient: the auth server of the client (%s) answered an interactive grant without a continuation", clientWallet)
	}

	return OutgoingPaymentGrant{
		ID:            grantID,
		RedirectURL:   grant.Interact.Redirect,
		ContinueURI:   grant.Continue.Uri,
		ContinueToken: grant.Continue.AccessToken.Value,
		ClientNonce:   clientNonce,
		ServerNonce:   grant.Interact.Finish,
		AuthServer:    address.AuthServer,
	}, nil
}

// ContinueOutgoingPaymentGrantParams names the continuation of one interactive
// grant (contract 8.3): what the callback received from the wallet, plus the
// client wallet the grant was asked for.
type ContinueOutgoingPaymentGrantParams struct {
	ClientWalletAddress string
	ContinueURI         string
	ContinueToken       string
	InteractRef         string
}

// AccessToken is one access token of a grant: the value the resource server
// accepts, the management URL it rotates around, and how long it lives.
type AccessToken struct {
	Value     string
	ManageURL string
	ExpiresIn int
}

// ContinueOutgoingPaymentGrant continues an interactive grant with the
// interact_ref the wallet sent back (contract 8.3) and returns the approved
// access token, with its management URL for the rotations that follow.
func (c *Client) ContinueOutgoingPaymentGrant(ctx context.Context, params ContinueOutgoingPaymentGrantParams) (AccessToken, error) {
	if _, err := validateWalletAddressParam("ClientWalletAddress", params.ClientWalletAddress); err != nil {
		return AccessToken{}, err
	}
	if err := validateContinueURIParam("ContinueURI", params.ContinueURI); err != nil {
		return AccessToken{}, err
	}
	if strings.TrimSpace(params.ContinueToken) == "" {
		return AccessToken{}, errors.New("opclient: ContinueToken is empty")
	}
	if strings.TrimSpace(params.InteractRef) == "" {
		return AccessToken{}, errors.New("opclient: InteractRef is empty")
	}

	grant, err := c.sdk.Grant.Continue(ctx, openpayments.GrantContinueParams{URL: params.ContinueURI, AccessToken: params.ContinueToken, InteractRef: params.InteractRef})
	if err != nil {
		return AccessToken{}, fmt.Errorf("opclient: continuing the grant of the client at %s: %w", params.ContinueURI, err)
	}
	if grant.AccessToken == nil || grant.AccessToken.Value == "" {
		return AccessToken{}, errors.New("opclient: the auth server of the client answered the continuation without an access token")
	}

	return toAccessToken(*grant.AccessToken), nil
}

// RotateAccessToken rotates an approved access token around the management URL
// its grant returned (contract 8.7 rotates after every payment). The old token
// is dead once this returns, so the caller stores the new one.
func (c *Client) RotateAccessToken(ctx context.Context, manageURL, accessToken string) (AccessToken, error) {
	if err := validateTokenURLParam("manageURL", manageURL); err != nil {
		return AccessToken{}, err
	}
	if strings.TrimSpace(accessToken) == "" {
		return AccessToken{}, errors.New("opclient: accessToken is empty")
	}

	rotated, err := c.sdk.Token.Rotate(ctx, openpayments.TokenRotateParams{URL: manageURL, AccessToken: accessToken})
	if err != nil {
		return AccessToken{}, fmt.Errorf("opclient: rotating the access token at %s: %w", manageURL, err)
	}
	if rotated.Value == "" {
		return AccessToken{}, fmt.Errorf("opclient: the auth server at %s answered the rotation without an access token", manageURL)
	}

	return toAccessToken(rotated), nil
}

// toAccessToken reduces the token of the auth server to what the callers keep:
// the value, where it rotates and for how long. A token without an expiry is
// kept with zero, because the servers of the ASE always send one and the
// callers treat zero as unknown rather than eternal.
func toAccessToken(token as.AccessToken) AccessToken {
	expiresIn := 0
	if token.ExpiresIn != nil {
		expiresIn = *token.ExpiresIn
	}
	return AccessToken{Value: token.Value, ManageURL: token.Manage, ExpiresIn: expiresIn}
}

// CreateOutgoingPaymentsParams names the last half of the withdrawal (contract
// 8.7 step 2): the client wallet that pays, the two quotes it pays, and the
// approved access token of the interactive grant of the cupo.
type CreateOutgoingPaymentsParams struct {
	// ClientWalletAddress is the wallet the payments leave from, the one the
	// interactive grant was asked for.
	ClientWalletAddress string

	// StoreQuoteURL and PlatformQuoteURL are the two quotes of the withdrawal,
	// the store's and the platform's, in that order.
	StoreQuoteURL    string
	PlatformQuoteURL string

	// Token is the approved access token of the grant. It rotates after each
	// payment, so the value that arrives is dead by the time this returns.
	Token AccessToken
}

// OutgoingPayment is one payment the client wallet made: where it lives on the
// resource server, how much it debited and received, and whether the resource
// server reported it as failed.
type OutgoingPayment struct {
	URL           string
	DebitAmount   Amount
	ReceiveAmount Amount
}

// OutgoingPayments are the two payments of one withdrawal, with the access
// token as it ended: contract 8.7 rotates the token after each use, so every
// value that paid is dead and the caller stores Token, which is the one the
// last rotation produced.
type OutgoingPayments struct {
	Store    OutgoingPayment
	Platform OutgoingPayment
	Token    AccessToken
}

// CreateOutgoingPayments runs the payment half of the withdrawal (contract 8.7
// step 2): it creates the store's outgoing payment with the approved token,
// rotates the token, creates the platform's outgoing payment with the rotated
// value, and rotates it once more, so the caller stores a token that has not
// paid anything yet.
// When a rotation fails after a payment was made, the returned value carries the
// payments that were made and the error says which one: the money moved, and a
// caller that could not see it could not record it either.
func (c *Client) CreateOutgoingPayments(ctx context.Context, params CreateOutgoingPaymentsParams) (OutgoingPayments, error) {
	// Everything is validated before the first payment, so a bad parameter
	// cannot leave one payment made and one missing.
	clientWallet, err := validateWalletAddressParam("ClientWalletAddress", params.ClientWalletAddress)
	if err != nil {
		return OutgoingPayments{}, err
	}
	if err := validateQuoteURLParam("StoreQuoteURL", params.StoreQuoteURL); err != nil {
		return OutgoingPayments{}, err
	}
	if err := validateQuoteURLParam("PlatformQuoteURL", params.PlatformQuoteURL); err != nil {
		return OutgoingPayments{}, err
	}
	if strings.TrimSpace(params.Token.Value) == "" {
		return OutgoingPayments{}, errors.New("opclient: Token.Value is empty")
	}
	// Without a management URL the token cannot rotate after the first payment,
	// and the second payment would run on a token the auth server already took
	// away.
	if err := validateTokenURLParam("Token.ManageURL", params.Token.ManageURL); err != nil {
		return OutgoingPayments{}, err
	}

	store, err := c.createOutgoingPayment(ctx, "the store", clientWallet, params.StoreQuoteURL, params.Token.Value)
	if err != nil {
		return OutgoingPayments{}, err
	}

	rotated, err := c.RotateAccessToken(ctx, params.Token.ManageURL, params.Token.Value)
	if err != nil {
		// The store was already paid: the money moved, so the payment travels back
		// with the error instead of being thrown away.
		return OutgoingPayments{Store: store}, fmt.Errorf("opclient: the store was paid (%s) and rotating its token failed: %w", store.URL, err)
	}

	platform, err := c.createOutgoingPayment(ctx, "the platform", clientWallet, params.PlatformQuoteURL, rotated.Value)
	if err != nil {
		return OutgoingPayments{Store: store}, err
	}

	final, err := c.RotateAccessToken(ctx, rotated.ManageURL, rotated.Value)
	if err != nil {
		// Both payments were made, so both travel back with the error along with the
		// token the second one used: what is missing is a fresh rotation, not the
		// record of what was paid.
		return OutgoingPayments{Store: store, Platform: platform, Token: rotated}, fmt.Errorf("opclient: the platform was paid (%s) and rotating its token failed: %w", platform.URL, err)
	}

	return OutgoingPayments{Store: store, Platform: platform, Token: final}, nil
}

// createOutgoingPayment pays one quote from the client wallet with one access
// token. A payment the resource server reports as failed is an error, not a
// success: the money did not move, and the caller must not go on as if it had.
func (c *Client) createOutgoingPayment(ctx context.Context, role, clientWallet, quoteURL, accessToken string) (OutgoingPayment, error) {
	var payload rs.CreateOutgoingPaymentRequest
	if err := payload.FromCreateOutgoingPaymentRequestFromQuote(rs.CreateOutgoingPaymentRequestFromQuote{
		WalletAddressSchema: clientWallet,
		QuoteId:             quoteURL,
	}); err != nil {
		return OutgoingPayment{}, fmt.Errorf("opclient: building the outgoing payment of %s: %w", role, err)
	}

	payment, err := c.sdk.OutgoingPayment.Create(ctx, openpayments.OutgoingPaymentCreateParams{
		BaseURL:     clientWallet,
		AccessToken: accessToken,
		Payload:     payload,
	})
	if err != nil {
		return OutgoingPayment{}, fmt.Errorf("opclient: creating the outgoing payment of %s from %s (quote %s): %w", role, clientWallet, quoteURL, err)
	}

	if payment.Id == nil || *payment.Id == "" {
		return OutgoingPayment{}, fmt.Errorf("opclient: the resource server did not return the URL of the outgoing payment of %s", role)
	}
	if payment.Failed != nil && *payment.Failed {
		return OutgoingPayment{}, fmt.Errorf("opclient: the resource server reports the outgoing payment of %s (%s) as failed: no money moved", role, *payment.Id)
	}

	return OutgoingPayment{
		URL:           *payment.Id,
		DebitAmount:   toAmount(payment.DebitAmount),
		ReceiveAmount: toAmount(payment.ReceiveAmount),
	}, nil
}

// IncomingPaymentStatus is how complete a collection point is (contract 8.8):
// whether it received everything it asked for, and how much it has so far.
type IncomingPaymentStatus struct {
	Completed bool
	Received  Amount
}

// IncomingPaymentStatus reads the state of one collection point (contract 8.8).
// It reads the signed endpoint with the access token, never the public one: the
// public document of an incoming payment carries the received amount but not
// the completion, so a route built on it could hand out money on an unpaid
// withdrawal.
func (c *Client) IncomingPaymentStatus(ctx context.Context, incomingPaymentURL, accessToken string) (IncomingPaymentStatus, error) {
	if strings.TrimSpace(incomingPaymentURL) == "" {
		return IncomingPaymentStatus{}, errors.New("opclient: incomingPaymentURL is empty")
	}
	if strings.TrimSpace(accessToken) == "" {
		return IncomingPaymentStatus{}, errors.New("opclient: accessToken is empty")
	}

	payment, err := c.sdk.IncomingPayment.Get(ctx, openpayments.IncomingPaymentGetParams{URL: incomingPaymentURL, AccessToken: accessToken})
	if err != nil {
		return IncomingPaymentStatus{}, fmt.Errorf("opclient: reading the status of the incoming payment %s: %w", incomingPaymentURL, err)
	}

	return IncomingPaymentStatus{Completed: payment.Completed, Received: toAmount(payment.ReceivedAmount)}, nil
}

// WaitForIncomingPaymentParams names the wait of the withdrawal (contract 8.7
// step 3): which collection point to poll, with which credential, and for how
// long.
type WaitForIncomingPaymentParams struct {
	IncomingPaymentURL string
	AccessToken        string

	// Timeout is how long the wait may poll before giving up. Zero means
	// fifteen seconds, which is the budget the contract fixes (about 15 s).
	Timeout time.Duration

	// Interval is the pause between two status reads. Zero means one second.
	Interval time.Duration
}

// The budgets of the wait, with the defaults the doc comments promise.
const (
	defaultWaitTimeout  = 15 * time.Second
	defaultWaitInterval = time.Second
)

func effectiveWaitTimeout(timeout time.Duration) (time.Duration, error) {
	if timeout < 0 {
		return 0, fmt.Errorf("opclient: Timeout=%s is negative", timeout)
	}
	if timeout == 0 {
		return defaultWaitTimeout, nil
	}
	return timeout, nil
}

func effectiveWaitInterval(interval time.Duration) (time.Duration, error) {
	if interval < 0 {
		return 0, fmt.Errorf("opclient: Interval=%s is negative", interval)
	}
	if interval == 0 {
		return defaultWaitInterval, nil
	}
	return interval, nil
}

// WaitForIncomingPayment polls the signed status of the store's incoming
// payment until it is complete or the budget runs out, and returns the last
// status when it is complete.
//
// A timeout is an error, never a success: no money leaves before the payment
// is completed, so a caller that cannot prove completion must not proceed.
// A cancelled context returns as soon as the context is done.
func (c *Client) WaitForIncomingPayment(ctx context.Context, params WaitForIncomingPaymentParams) (IncomingPaymentStatus, error) {
	if strings.TrimSpace(params.IncomingPaymentURL) == "" {
		return IncomingPaymentStatus{}, errors.New("opclient: IncomingPaymentURL is empty")
	}
	if strings.TrimSpace(params.AccessToken) == "" {
		return IncomingPaymentStatus{}, errors.New("opclient: AccessToken is empty")
	}
	timeout, err := effectiveWaitTimeout(params.Timeout)
	if err != nil {
		return IncomingPaymentStatus{}, err
	}
	interval, err := effectiveWaitInterval(params.Interval)
	if err != nil {
		return IncomingPaymentStatus{}, err
	}

	deadline := time.Now().Add(timeout)
	for {
		status, err := c.IncomingPaymentStatus(ctx, params.IncomingPaymentURL, params.AccessToken)
		if err != nil {
			if ctx.Err() != nil {
				return IncomingPaymentStatus{}, fmt.Errorf("opclient: waiting for the incoming payment %s: %w", params.IncomingPaymentURL, ctx.Err())
			}
			return IncomingPaymentStatus{}, err
		}
		if status.Completed {
			return status, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return IncomingPaymentStatus{}, fmt.Errorf("opclient: the incoming payment %s did not complete within %s, and a withdrawal cannot proceed without proof that the payment completed", params.IncomingPaymentURL, timeout)
		}

		pause := interval
		if pause > remaining {
			pause = remaining
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return IncomingPaymentStatus{}, fmt.Errorf("opclient: waiting for the incoming payment %s: %w", params.IncomingPaymentURL, ctx.Err())
		case <-timer.C:
		}
	}
}

// InteractiveHash computes the hash of the callback of an interactive grant
// (contract 8.3): base64 of the SHA-256 over the client nonce, the server
// nonce, the interact ref and the authorization server URL, joined by newlines.
// The base64 is the standard alphabet, which is what Rafiki's auth server signs.
func InteractiveHash(clientNonce, serverNonce, interactRef, authServerURL string) string {
	digest := sha256.Sum256([]byte(clientNonce + "\n" + serverNonce + "\n" + interactRef + "\n" + authServerURL))
	return base64.StdEncoding.EncodeToString(digest[:])
}

// VerifyInteractiveHash verifies a callback hash in constant time (contract
// 8.3). A comparison that leaks where the first difference is would let an
// attacker forge the hash of a callback byte by byte, so it cannot be a plain
// string comparison.
func VerifyInteractiveHash(clientNonce, serverNonce, interactRef, authServerURL, received string) bool {
	expected := InteractiveHash(clientNonce, serverNonce, interactRef, authServerURL)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(received)) == 1
}

// NewNonce generates an unguessable client nonce for a finish block: at least
// 32 bytes of crypto randomness, base64url encoded. An attacker who could guess
// it could forge the hash of a callback, so mathrand is not enough.
func NewNonce() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("opclient: reading random bytes for the nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// validateFinishURLParam checks the URL the wallet sends the user back to; the
// grant id is appended as its query, so it has to be a URL with a host.
func validateFinishURLParam(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("opclient: FinishURL is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("opclient: FinishURL=%q is not the URL the wallet can send the user back to", raw)
	}
	return trimmed, nil
}

// validateGrantIDParam checks the caller's identifier of a cupo. It travels both
// inside the finish URI the wallet opens and as a query parameter the callback
// reads back, so it is limited to the characters that cannot change the URI it is
// embedded in.
func validateGrantIDParam(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("opclient: GrantID is empty")
	}

	for _, character := range trimmed {
		safe := character == '_' || character == '-' ||
			(character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z')
		if !safe {
			return "", fmt.Errorf("opclient: GrantID=%q carries %q, which cannot travel in the finish URI", raw, character)
		}
	}

	return trimmed, nil
}

// addGrantIDToURL adds the grant id to the finish URL as one more query parameter.
// Concatenating it by hand would produce a second "?" when the finish URL already
// carries a query, and the wallet would come back to a URI the callback cannot read.
func addGrantIDToURL(finishURL, grantID string) (string, error) {
	parsed, err := url.Parse(finishURL)
	if err != nil {
		return "", fmt.Errorf("opclient: FinishURL=%q is not a URL: %w", finishURL, err)
	}

	query := parsed.Query()
	query.Set("grant_id", grantID)
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

// validateContinueURIParam checks a URI that continues or manages a grant. The
// SDK refuses a continuation URL without continue/ and a management URL without
// token/, and both are refused here, before any network call, so the error
// names the parameter of the caller instead of an SDK message.
func validateContinueURIParam(name, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("opclient: %s is empty", name)
	}
	if !strings.Contains(trimmed, "continue/") {
		return fmt.Errorf("opclient: %s=%q is not the URI that continues a grant", name, raw)
	}
	return nil
}

func validateTokenURLParam(name, raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("opclient: %s is empty", name)
	}
	if !strings.Contains(trimmed, "token/") {
		return fmt.Errorf("opclient: %s=%q is not the URL that manages an access token", name, raw)
	}
	return nil
}

func toAmount(a rs.Amount) Amount {
	return Amount{AssetCode: a.AssetCode, AssetScale: a.AssetScale, Value: a.Value}
}
