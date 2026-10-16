package rafikiadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	walletAddressesQuery        = `query WalletAddresses($first: Int) { walletAddresses(first: $first) { edges { node { id address publicName asset { id code scale } } } } }`
	createWalletAddressMutation = `mutation CreateWalletAddress($input: CreateWalletAddressInput!) { createWalletAddress(input: $input) { walletAddress { id address publicName asset { id code scale } } } }`

	// walletAddressPageSize is how many addresses EnsureWalletAddress inspects
	// before deciding that the one it wants does not exist yet.
	walletAddressPageSize = 100
)

// WalletAddress is one account at the ASE. Rafiki stores the full wallet address
// URL here (lowercased), not the bare name it also accepts on creation.
type WalletAddress struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	PublicName string `json:"publicName"`
	Asset      Asset  `json:"asset"`
}

// CreateWalletAddressInput is what createWalletAddress takes in Rafiki.
//
// Address may be a bare name or a full URL. Rafiki composes a bare name over its
// own public URL and refuses any wallet address URL that is not https.
type CreateWalletAddressInput struct {
	AssetID    string
	Address    string
	PublicName string

	// IdempotencyKey makes a retried request a no-op at the Admin API instead of
	// a duplicate. Callers that provision repeatedly should set it.
	IdempotencyKey string
}

// WalletAddresses lists the wallet addresses of the tenant acting on this
// connection.
func (c *Client) WalletAddresses(ctx context.Context) ([]WalletAddress, error) {
	var data struct {
		WalletAddresses struct {
			Edges []struct {
				Node WalletAddress `json:"node"`
			} `json:"edges"`
		} `json:"walletAddresses"`
	}
	if err := c.Do(ctx, walletAddressesQuery, map[string]any{"first": walletAddressPageSize}, &data); err != nil {
		return nil, err
	}

	addresses := make([]WalletAddress, 0, len(data.WalletAddresses.Edges))
	for _, edge := range data.WalletAddresses.Edges {
		addresses = append(addresses, edge.Node)
	}

	return addresses, nil
}

// CreateWalletAddress creates one wallet address at the ASE. The optional fields
// are only sent when the caller set them, so the request holds exactly what was
// asked for.
func (c *Client) CreateWalletAddress(ctx context.Context, in CreateWalletAddressInput) (WalletAddress, error) {
	input := map[string]any{
		"assetId": in.AssetID,
		"address": in.Address,
	}
	if in.PublicName != "" {
		input["publicName"] = in.PublicName
	}
	if in.IdempotencyKey != "" {
		input["idempotencyKey"] = in.IdempotencyKey
	}

	var data struct {
		CreateWalletAddress struct {
			WalletAddress WalletAddress `json:"walletAddress"`
		} `json:"createWalletAddress"`
	}
	if err := c.Do(ctx, createWalletAddressMutation, map[string]any{"input": input}, &data); err != nil {
		return WalletAddress{}, err
	}

	created := data.CreateWalletAddress.WalletAddress
	if created.ID == "" {
		return WalletAddress{}, errors.New("rafikiadmin: the Admin API replied with a wallet address without an ID")
	}

	return created, nil
}

// EnsureWalletAddress returns the wallet address with that address, creating it
// when the ASE does not host it yet.
//
// Rafiki stores the wallet address URL lowercased, so the comparison ignores
// case: a second provisioning run must recognise what the first one created
// instead of trying to insert a duplicate.
func (c *Client) EnsureWalletAddress(ctx context.Context, in CreateWalletAddressInput) (WalletAddress, error) {
	addresses, err := c.WalletAddresses(ctx)
	if err != nil {
		return WalletAddress{}, fmt.Errorf("listing the wallet addresses: %w", err)
	}
	for _, address := range addresses {
		if strings.EqualFold(address.Address, in.Address) {
			return address, nil
		}
	}

	return c.CreateWalletAddress(ctx, in)
}
