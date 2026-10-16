package rafikiadmin

import (
	"context"
	"errors"
	"fmt"
)

const (
	assetByCodeAndScaleQuery = `query AssetByCodeAndScale($code: String!, $scale: UInt8!) { assetByCodeAndScale(code: $code, scale: $scale) { id code scale liquidity } }`
	createAssetMutation      = `mutation CreateAsset($input: CreateAssetInput!) { createAsset(input: $input) { asset { id code scale liquidity } } }`
)

// Asset is one currency the ASE keeps accounts in. Scale is the number of
// decimals Open Payments works with for that currency, and Liquidity is how much
// of it the ASE holds.
type Asset struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Scale     int    `json:"scale"`
	Liquidity UInt64 `json:"liquidity"`
}

// AssetByCodeAndScale returns the asset for that currency and says whether the
// ASE has it. Rafiki answers null, not an error, when it does not.
func (c *Client) AssetByCodeAndScale(ctx context.Context, code string, scale int) (Asset, bool, error) {
	var data struct {
		Asset *Asset `json:"assetByCodeAndScale"`
	}
	if err := c.Do(ctx, assetByCodeAndScaleQuery, map[string]any{"code": code, "scale": scale}, &data); err != nil {
		return Asset{}, false, err
	}
	if data.Asset == nil {
		return Asset{}, false, nil
	}

	return *data.Asset, true, nil
}

// CreateAsset creates one asset at the ASE. Every wallet address belongs to an
// asset, so this comes before any account exists.
func (c *Client) CreateAsset(ctx context.Context, code string, scale int) (Asset, error) {
	var data struct {
		CreateAsset struct {
			Asset Asset `json:"asset"`
		} `json:"createAsset"`
	}
	input := map[string]any{"code": code, "scale": scale}
	if err := c.Do(ctx, createAssetMutation, map[string]any{"input": input}, &data); err != nil {
		return Asset{}, err
	}

	created := data.CreateAsset.Asset
	if created.ID == "" {
		return Asset{}, errors.New("rafikiadmin: the Admin API replied with an asset without an ID")
	}

	return created, nil
}

// EnsureAsset returns the asset, creating it when the ASE does not have it yet.
// Rafiki refuses a duplicate asset, so provisioning a stack that may already be
// seeded has to look first.
func (c *Client) EnsureAsset(ctx context.Context, code string, scale int) (Asset, error) {
	asset, found, err := c.AssetByCodeAndScale(ctx, code, scale)
	if err != nil {
		return Asset{}, fmt.Errorf("looking up the %s asset: %w", code, err)
	}
	if found {
		return asset, nil
	}

	return c.CreateAsset(ctx, code, scale)
}
