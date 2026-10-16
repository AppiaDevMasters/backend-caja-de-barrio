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
)

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
