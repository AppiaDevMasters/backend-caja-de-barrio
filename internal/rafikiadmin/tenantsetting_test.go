package rafikiadmin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

const (
	settingValue        = "https://100.76.141.47:3000"
	tenantSettingsReply = `{"data":{"tenant":{"settings":[{"key":"WALLET_ADDRESS_URL","value":"` + settingValue + `"}]}}}`
)

func TestTenantSettingsDecodesTheSettings(t *testing.T) {
	srv, asked := adminStub(t, tenantSettingsReply)

	settings, err := newTestClient(t, srv.URL).TenantSettings(context.Background())
	if err != nil {
		t.Fatalf("TenantSettings() returned an error: %v", err)
	}
	if len(settings) != 1 {
		t.Fatalf("got %d settings, want 1", len(settings))
	}
	if settings[0].Key != rafikiadmin.SettingWalletAddressURL || settings[0].Value != settingValue {
		t.Errorf("setting = %+v, want %s = %s", settings[0], rafikiadmin.SettingWalletAddressURL, settingValue)
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "tenant(id:") {
		t.Errorf("query = %q, want a query for the tenant and its settings", requests[0].Query)
	}
	requireVariables(t, requests[0].Variables, map[string]any{"id": tenantID})
}

// Rafiki answers DuplicateWalletAddressUrl when the same setting already holds
// the same value, so provisioning has to read before writing.
func TestEnsureTenantSettingCreatesItWhenItIsMissing(t *testing.T) {
	srv, asked := adminStub(t,
		`{"data":{"tenant":{"settings":[]}}}`,
		`{"data":{"createTenantSettings":{"settings":[{"key":"WALLET_ADDRESS_URL","value":"`+settingValue+`"}]}}}`,
	)

	err := newTestClient(t, srv.URL).EnsureTenantSetting(context.Background(), rafikiadmin.SettingWalletAddressURL, settingValue)
	if err != nil {
		t.Fatalf("EnsureTenantSetting() returned an error: %v", err)
	}

	requests := asked()
	requireRequests(t, requests, 2)
	if !strings.Contains(requests[1].Query, "createTenantSettings") {
		t.Errorf("second query = %q, want a createTenantSettings mutation", requests[1].Query)
	}
	requireVariables(t, requests[1].Variables, map[string]any{
		"input": map[string]any{
			"settings": []any{
				map[string]any{"key": rafikiadmin.SettingWalletAddressURL, "value": settingValue},
			},
		},
	})
}

func TestEnsureTenantSettingDoesNothingWhenItAlreadyHoldsTheValue(t *testing.T) {
	srv, asked := adminStub(t, tenantSettingsReply)

	if err := newTestClient(t, srv.URL).EnsureTenantSetting(context.Background(), rafikiadmin.SettingWalletAddressURL, settingValue); err != nil {
		t.Fatalf("EnsureTenantSetting() returned an error: %v", err)
	}
	requireRequests(t, asked(), 1)
}

// Rafiki upserts on a different value, so a changed base has to be written.
func TestEnsureTenantSettingReplacesAValueThatChanged(t *testing.T) {
	const newValue = "https://pay.example"
	srv, asked := adminStub(t,
		tenantSettingsReply,
		`{"data":{"createTenantSettings":{"settings":[{"key":"WALLET_ADDRESS_URL","value":"`+newValue+`"}]}}}`,
	)

	if err := newTestClient(t, srv.URL).EnsureTenantSetting(context.Background(), rafikiadmin.SettingWalletAddressURL, newValue); err != nil {
		t.Fatalf("EnsureTenantSetting() returned an error: %v", err)
	}

	requests := asked()
	requireRequests(t, requests, 2)
	requireVariables(t, requests[1].Variables, map[string]any{
		"input": map[string]any{
			"settings": []any{
				map[string]any{"key": rafikiadmin.SettingWalletAddressURL, "value": newValue},
			},
		},
	})
}
