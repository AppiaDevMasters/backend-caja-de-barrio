package opclient

import (
	"context"
	"fmt"
	"strings"

	openpayments "github.com/interledger/open-payments-go"
	rs "github.com/interledger/open-payments-go/generated/resourceserver"
)

// These three calls run a withdrawal with ONE collection point, the store's, and are
// what the testnet mode uses (docs/modo-testnet.md). The two-point calls of the own
// ASE (CreateIncomingPayments, CreateQuotes, CreateOutgoingPayments) stay as they are.

// CreateIncomingPayment creates one incoming payment on a wallet for a whole amount of
// that wallet's own asset (pesos for a COP store), already converted to its scale.
func (c *Client) CreateIncomingPayment(ctx context.Context, walletAddress string, amount int64) (IncomingPayment, error) {
	wallet, err := validateWalletAddressParam("WalletAddress", walletAddress)
	if err != nil {
		return IncomingPayment{}, err
	}
	if err := validateAmountParam("Amount", amount); err != nil {
		return IncomingPayment{}, err
	}

	return c.createIncomingPayment(ctx, collectionPoint{
		role: "the store", walletAddressParam: "WalletAddress", amountParam: "Amount",
		walletAddress: wallet, amountCOP: amount,
	})
}

// CreateQuoteByReceiver quotes a payment from the client's wallet to an incoming
// payment that has an incomingAmount, WITHOUT a debit amount: the quote takes what the
// receiver asked for and the ASE works out the debit in the client's own asset. It is
// how a USD wallet pays a COP store the exact pesos it asked for.
func (c *Client) CreateQuoteByReceiver(ctx context.Context, clientWalletAddress, receiver string) (Quote, error) {
	clientWallet, err := validateWalletAddressParam("ClientWalletAddress", clientWalletAddress)
	if err != nil {
		return Quote{}, err
	}
	if err := validateReceiverParam("Receiver", receiver); err != nil {
		return Quote{}, err
	}

	address, err := c.ResolveWalletAddress(ctx, clientWallet)
	if err != nil {
		return Quote{}, fmt.Errorf("opclient: the client: %w", err)
	}
	token, err := c.requestQuoteGrant(ctx, "the client", address.AuthServer)
	if err != nil {
		return Quote{}, err
	}

	quote, err := c.sdk.Quote.Create(ctx, openpayments.QuoteCreateParams{
		BaseURL:     address.ResourceServer,
		AccessToken: token,
		Payload: rs.CreateQuoteRequestByReceiver{
			WalletAddressSchema: address.URL,
			Method:              rs.PaymentMethod("ilp"),
			Receiver:            rs.Receiver(receiver),
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

// CreateOutgoingPayment pays one quote with the cupo's token and rotates the token
// after the use. A payment made with a failed rotation travels back with the error, so
// the caller keeps the record of the money that moved.
func (c *Client) CreateOutgoingPayment(ctx context.Context, clientWalletAddress, quoteURL string, token AccessToken) (OutgoingPayment, AccessToken, error) {
	clientWallet, err := validateWalletAddressParam("ClientWalletAddress", clientWalletAddress)
	if err != nil {
		return OutgoingPayment{}, AccessToken{}, err
	}
	if err := validateQuoteURLParam("QuoteURL", quoteURL); err != nil {
		return OutgoingPayment{}, AccessToken{}, err
	}
	if strings.TrimSpace(token.Value) == "" {
		return OutgoingPayment{}, AccessToken{}, fmt.Errorf("opclient: Token.Value is empty")
	}
	if err := validateTokenURLParam("Token.ManageURL", token.ManageURL); err != nil {
		return OutgoingPayment{}, AccessToken{}, err
	}

	address, err := c.ResolveWalletAddress(ctx, clientWallet)
	if err != nil {
		return OutgoingPayment{}, AccessToken{}, fmt.Errorf("opclient: the client: %w", err)
	}
	payment, err := c.createOutgoingPayment(ctx, address.ResourceServer, "the store", clientWallet, quoteURL, token.Value)
	if err != nil {
		return OutgoingPayment{}, AccessToken{}, err
	}

	rotated, err := c.RotateAccessToken(ctx, token.ManageURL, token.Value)
	if err != nil {
		return payment, AccessToken{}, fmt.Errorf("opclient: the store was paid (%s) and rotating its token failed: %w", payment.URL, err)
	}

	return payment, rotated, nil
}
