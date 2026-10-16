package rafikiadmin

import (
	"context"
	"errors"
	"fmt"
)

const (
	walletAddressKeysQuery         = `query WalletAddressKeys($id: String!) { walletAddress(id: $id) { walletAddressKeys { edges { node { id revoked jwk { kid x alg kty crv } } } } } }`
	createWalletAddressKeyMutation = `mutation CreateWalletAddressKey($input: CreateWalletAddressKeyInput!) { createWalletAddressKey(input: $input) { walletAddressKey { id revoked jwk { kid x alg kty crv } } } }`
)

// JWK is a JSON Web Key in the form Rafiki stores it on a wallet address.
type JWK struct {
	Kid string `json:"kid"`
	X   string `json:"x"`
	Alg string `json:"alg"`
	Kty string `json:"kty"`
	Crv string `json:"crv"`
}

// Ed25519JWK builds the only key type Rafiki accepts on a wallet address.
func Ed25519JWK(kid, x string) JWK {
	return JWK{Kid: kid, X: x, Alg: "EdDSA", Kty: "OKP", Crv: "Ed25519"}
}

// WalletAddressKey is one public key registered on a wallet address.
type WalletAddressKey struct {
	ID      string `json:"id"`
	Revoked bool   `json:"revoked"`
	JWK     JWK    `json:"jwk"`
}

// CreateWalletAddressKeyInput is what createWalletAddressKey takes in Rafiki.
type CreateWalletAddressKeyInput struct {
	WalletAddressID string
	JWK             JWK
	IdempotencyKey  string
}

// WalletAddressKeys lists the public keys registered on a wallet address.
func (c *Client) WalletAddressKeys(ctx context.Context, walletAddressID string) ([]WalletAddressKey, error) {
	var data struct {
		WalletAddress *struct {
			WalletAddressKeys struct {
				Edges []struct {
					Node WalletAddressKey `json:"node"`
				} `json:"edges"`
			} `json:"walletAddressKeys"`
		} `json:"walletAddress"`
	}
	if err := c.Do(ctx, walletAddressKeysQuery, map[string]any{"id": walletAddressID}, &data); err != nil {
		return nil, err
	}
	if data.WalletAddress == nil {
		return nil, fmt.Errorf("rafikiadmin: the ASE does not have the wallet address %s", walletAddressID)
	}

	keys := make([]WalletAddressKey, 0, len(data.WalletAddress.WalletAddressKeys.Edges))
	for _, edge := range data.WalletAddress.WalletAddressKeys.Edges {
		keys = append(keys, edge.Node)
	}

	return keys, nil
}

// CreateWalletAddressKey registers one public key on a wallet address. Without a
// registered key no signed Open Payments request from that client can be
// verified.
func (c *Client) CreateWalletAddressKey(ctx context.Context, in CreateWalletAddressKeyInput) (WalletAddressKey, error) {
	input := map[string]any{
		"walletAddressId": in.WalletAddressID,
		"jwk":             in.JWK,
	}
	if in.IdempotencyKey != "" {
		input["idempotencyKey"] = in.IdempotencyKey
	}

	var data struct {
		CreateWalletAddressKey struct {
			WalletAddressKey WalletAddressKey `json:"walletAddressKey"`
		} `json:"createWalletAddressKey"`
	}
	if err := c.Do(ctx, createWalletAddressKeyMutation, map[string]any{"input": input}, &data); err != nil {
		return WalletAddressKey{}, err
	}

	created := data.CreateWalletAddressKey.WalletAddressKey
	if created.ID == "" {
		return WalletAddressKey{}, errors.New("rafikiadmin: the Admin API replied with a wallet address key without an ID")
	}

	return created, nil
}

// EnsureWalletAddressKey registers the key only when that kid is not already
// active on the wallet address: Rafiki refuses a duplicate, while a revoked key
// has to be registered again to be of any use.
func (c *Client) EnsureWalletAddressKey(ctx context.Context, in CreateWalletAddressKeyInput) error {
	keys, err := c.WalletAddressKeys(ctx, in.WalletAddressID)
	if err != nil {
		return fmt.Errorf("listing the keys of the wallet address %s: %w", in.WalletAddressID, err)
	}
	for _, key := range keys {
		if key.JWK.Kid == in.JWK.Kid && !key.Revoked {
			return nil
		}
	}

	if _, err := c.CreateWalletAddressKey(ctx, in); err != nil {
		return err
	}

	return nil
}
