package rafikiadmin

import (
	"context"
	"fmt"
)

const (
	// SettingWalletAddressURL names the tenant setting that holds the base URL of
	// the wallet addresses this ASE hosts.
	//
	// Rafiki reads that setting before falling back to its own OPEN_PAYMENTS_URL,
	// and it refuses every wallet address URL that is not https. A local stack
	// whose OPEN_PAYMENTS_URL is plain http therefore cannot create any wallet
	// address until this setting names an https base, which is why the backend
	// writes it instead of changing the stack.
	SettingWalletAddressURL = "WALLET_ADDRESS_URL"

	tenantSettingsQuery          = `query TenantSettings($id: String!) { tenant(id: $id) { settings { key value } } }`
	createTenantSettingsMutation = `mutation CreateTenantSettings($input: CreateTenantSettingsInput!) { createTenantSettings(input: $input) { settings { key value } } }`
)

// TenantSetting is one key/value pair of the tenant acting on this connection.
type TenantSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// TenantSettings lists the settings of the tenant acting on this connection.
func (c *Client) TenantSettings(ctx context.Context) ([]TenantSetting, error) {
	var data struct {
		Tenant struct {
			Settings []TenantSetting `json:"settings"`
		} `json:"tenant"`
	}
	if err := c.Do(ctx, tenantSettingsQuery, map[string]any{"id": c.tenantID}, &data); err != nil {
		return nil, err
	}

	return data.Tenant.Settings, nil
}

// CreateTenantSettings writes the given settings. Rafiki upserts them, so a key
// that already holds a different value is updated. The settings are taken in
// order instead of as a map so the request body is deterministic.
func (c *Client) CreateTenantSettings(ctx context.Context, settings ...TenantSetting) ([]TenantSetting, error) {
	input := make([]map[string]any, 0, len(settings))
	for _, setting := range settings {
		input = append(input, map[string]any{"key": setting.Key, "value": setting.Value})
	}

	var data struct {
		CreateTenantSettings struct {
			Settings []TenantSetting `json:"settings"`
		} `json:"createTenantSettings"`
	}
	if err := c.Do(ctx, createTenantSettingsMutation, map[string]any{"input": map[string]any{"settings": input}}, &data); err != nil {
		return nil, err
	}

	return data.CreateTenantSettings.Settings, nil
}

// EnsureTenantSetting writes the setting only when it does not already hold that
// value: Rafiki refuses the very same value with DuplicateWalletAddressUrl, and
// updates a different one, so provisioning has to read first either way.
func (c *Client) EnsureTenantSetting(ctx context.Context, key, value string) error {
	current, err := c.TenantSettings(ctx)
	if err != nil {
		return fmt.Errorf("reading the tenant settings: %w", err)
	}

	for _, setting := range current {
		if setting.Key != key {
			continue
		}
		if setting.Value == value {
			return nil
		}
		break
	}

	if _, err := c.CreateTenantSettings(ctx, TenantSetting{Key: key, Value: value}); err != nil {
		return fmt.Errorf("writing the %s tenant setting: %w", key, err)
	}

	return nil
}
