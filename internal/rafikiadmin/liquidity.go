package rafikiadmin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
)

const (
	assetQuery = `query Asset($id: String!) { asset(id: $id) { id code scale liquidity } }`

	depositAssetLiquidityMutation = `mutation DepositAssetLiquidity($input: DepositAssetLiquidityInput!) { depositAssetLiquidity(input: $input) { success } }`

	depositOutgoingPaymentLiquidityMutation = `mutation DepositOutgoingPaymentLiquidity($input: DepositOutgoingPaymentLiquidityInput!) { depositOutgoingPaymentLiquidity(input: $input) { success } }`

	withdrawIncomingPaymentLiquidityMutation = `mutation WithdrawIncomingPaymentLiquidity($input: WithdrawIncomingPaymentLiquidityInput!) { withdrawIncomingPaymentLiquidity(input: $input) { success } }`
)

// WithdrawIncomingPaymentLiquidity takes out of Rafiki the money a completed incoming
// payment received. Rafiki moves money and does not keep balances: the ASE withdraws
// what arrived and credits it in its own ledger (the virtual wallet), which is the
// accounting cycle Rafiki expects. The key is tied to the payment, so a retry
// withdraws once.
func (c *Client) WithdrawIncomingPaymentLiquidity(ctx context.Context, paymentID string) error {
	input := map[string]any{
		"incomingPaymentId": paymentID,
		"idempotencyKey":    "cobro:retirar:" + paymentID,
	}

	var data struct {
		WithdrawIncomingPaymentLiquidity struct {
			Success bool `json:"success"`
		} `json:"withdrawIncomingPaymentLiquidity"`
	}
	if err := c.Do(ctx, withdrawIncomingPaymentLiquidityMutation, map[string]any{"input": input}, &data); err != nil {
		return err
	}
	if !data.WithdrawIncomingPaymentLiquidity.Success {
		return errors.New("rafikiadmin: the Admin API reported the incoming payment liquidity withdrawal as unsuccessful")
	}

	return nil
}

// Asset returns one asset of the ASE and says whether the ASE has it.
func (c *Client) Asset(ctx context.Context, id string) (Asset, bool, error) {
	var data struct {
		Asset *Asset `json:"asset"`
	}
	if err := c.Do(ctx, assetQuery, map[string]any{"id": id}, &data); err != nil {
		return Asset{}, false, err
	}
	if data.Asset == nil {
		return Asset{}, false, nil
	}

	return *data.Asset, true, nil
}

// LiquidityTransfer funds one asset of the ASE.
type LiquidityTransfer struct {
	AssetID string
	Amount  UInt64

	// ID names the transfer and has to be an RFC 4122 version 4 UUID, because
	// Rafiki refuses anything else. IdempotencyKey makes a retry a no-op.
	ID             string
	IdempotencyKey string
}

// DepositAssetLiquidity funds the asset. Without liquidity the ASE cannot move
// money, so no payment can complete.
func (c *Client) DepositAssetLiquidity(ctx context.Context, transfer LiquidityTransfer) error {
	input := map[string]any{
		"assetId":        transfer.AssetID,
		"amount":         transfer.Amount,
		"id":             transfer.ID,
		"idempotencyKey": transfer.IdempotencyKey,
	}

	var data struct {
		DepositAssetLiquidity struct {
			Success bool `json:"success"`
		} `json:"depositAssetLiquidity"`
	}
	if err := c.Do(ctx, depositAssetLiquidityMutation, map[string]any{"input": input}, &data); err != nil {
		return err
	}
	if !data.DepositAssetLiquidity.Success {
		return errors.New("rafikiadmin: the Admin API reported the liquidity deposit as unsuccessful")
	}

	return nil
}

// EnsureAssetLiquidity brings the asset up to the target and returns what it
// deposited, which is zero when the asset already holds that much.
//
// The seed runs on every restart, so it funds the difference instead of adding
// the target again.
func (c *Client) EnsureAssetLiquidity(ctx context.Context, assetID string, target UInt64) (UInt64, error) {
	asset, found, err := c.Asset(ctx, assetID)
	if err != nil {
		return 0, fmt.Errorf("reading the asset liquidity: %w", err)
	}
	if !found {
		return 0, fmt.Errorf("rafikiadmin: the ASE does not have the asset %s", assetID)
	}
	if asset.Liquidity >= target {
		return 0, nil
	}

	missing := target - asset.Liquidity

	id, err := uuidV4()
	if err != nil {
		return 0, err
	}

	transfer := LiquidityTransfer{
		AssetID:        assetID,
		Amount:         missing,
		ID:             id,
		IdempotencyKey: "seed:liquidity:" + assetID + ":" + strconv.FormatInt(int64(missing), 10),
	}
	if err := c.DepositAssetLiquidity(ctx, transfer); err != nil {
		return 0, err
	}

	return missing, nil
}

// DepositOutgoingPaymentLiquidity funds one outgoing payment.
//
// Rafiki parks an outgoing payment in FUNDING when the account that pays cannot cover it. That is
// exactly what happens in the local trial: the client's wallet lives at this same ASE, so it has no
// balance, and the ASE ends up on both sides of the payment. Depositing the payment's liquidity is
// how the ASE advances the money that, in the demo, the client's real wallet already holds.
//
// The product calls it only for a client wallet of this same ASE, and only after debiting that
// client's virtual wallet for the payment (internal/pagos): the ASE moves the client's own money,
// never the caja's. The trial conductor calls it behind its explicit -fondear flag.
func (c *Client) DepositOutgoingPaymentLiquidity(ctx context.Context, paymentID string) error {
	input := map[string]any{
		"outgoingPaymentId": paymentID,
		// Tied to the payment, so retrying the trial funds a payment once and never twice.
		"idempotencyKey": "retiro:fondear:" + paymentID,
	}

	var data struct {
		DepositOutgoingPaymentLiquidity struct {
			Success bool `json:"success"`
		} `json:"depositOutgoingPaymentLiquidity"`
	}
	if err := c.Do(ctx, depositOutgoingPaymentLiquidityMutation, map[string]any{"input": input}, &data); err != nil {
		return err
	}
	if !data.DepositOutgoingPaymentLiquidity.Success {
		return errors.New("rafikiadmin: the Admin API reported the outgoing payment liquidity deposit as unsuccessful")
	}

	return nil
}

// uuidV4 returns a random RFC 4122 version 4 UUID, the identifier Rafiki
// requires for a liquidity transfer.
func uuidV4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("rafikiadmin: generating a UUID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40 // version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}
