package rafikiadmin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

const walletAddressesReply = `{"data":{"walletAddresses":{"edges":[` +
	`{"node":{"id":"wa-1","address":"tienda-cajadebarrio","publicName":"Don Jaime","asset":{"id":"asset-1","code":"COP","scale":2}}},` +
	`{"node":{"id":"wa-2","address":"laura-cajadebarrio","publicName":"Laura","asset":{"id":"asset-1","code":"COP","scale":2}}}` +
	`]}}}`

func TestWalletAddressesDecodesTheList(t *testing.T) {
	srv, asked := adminStub(t, walletAddressesReply)

	addresses, err := newTestClient(t, srv.URL).WalletAddresses(context.Background())
	if err != nil {
		t.Fatalf("WalletAddresses() returned an error: %v", err)
	}
	if len(addresses) != 2 {
		t.Fatalf("got %d wallet addresses, want 2", len(addresses))
	}
	if addresses[0].ID != "wa-1" || addresses[0].Address != "tienda-cajadebarrio" {
		t.Errorf("first wallet address = %+v, want wa-1 / tienda-cajadebarrio", addresses[0])
	}
	if addresses[0].Asset.Code != "COP" || addresses[0].Asset.Scale != 2 {
		t.Errorf("first wallet address asset = %+v, want COP scale 2", addresses[0].Asset)
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "walletAddresses") {
		t.Errorf("query = %q, want a walletAddresses query", requests[0].Query)
	}
}

// The Admin API cannot report a wallet address URL, so the query must ask for the
// address name and the asset the caller needs to build that URL.
func TestCreateWalletAddressSendsTheInputAndDecodesTheResult(t *testing.T) {
	srv, asked := adminStub(t, `{"data":{"createWalletAddress":{"walletAddress":`+
		`{"id":"wa-3","address":"app-cajadebarrio","publicName":"Caja de Barrio","asset":{"id":"asset-1","code":"COP","scale":2}}}}}`)

	created, err := newTestClient(t, srv.URL).CreateWalletAddress(context.Background(), rafikiadmin.CreateWalletAddressInput{
		AssetID:        "asset-1",
		Address:        "app-cajadebarrio",
		PublicName:     "Caja de Barrio",
		IdempotencyKey: "seed:app-cajadebarrio",
	})
	if err != nil {
		t.Fatalf("CreateWalletAddress() returned an error: %v", err)
	}
	if created.ID != "wa-3" {
		t.Errorf("created.ID = %q, want %q", created.ID, "wa-3")
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "createWalletAddress") {
		t.Errorf("query = %q, want a createWalletAddress mutation", requests[0].Query)
	}
	requireVariables(t, requests[0].Variables, map[string]any{
		"input": map[string]any{
			"assetId":        "asset-1",
			"address":        "app-cajadebarrio",
			"publicName":     "Caja de Barrio",
			"idempotencyKey": "seed:app-cajadebarrio",
		},
	})
}

func TestEnsureWalletAddressReusesAnExistingAddress(t *testing.T) {
	srv, asked := adminStub(t, walletAddressesReply)

	address, err := newTestClient(t, srv.URL).EnsureWalletAddress(context.Background(), rafikiadmin.CreateWalletAddressInput{
		AssetID:    "asset-1",
		Address:    "laura-cajadebarrio",
		PublicName: "Laura",
	})
	if err != nil {
		t.Fatalf("EnsureWalletAddress() returned an error: %v", err)
	}
	if address.ID != "wa-2" {
		t.Errorf("address.ID = %q, want the existing %q", address.ID, "wa-2")
	}
	requireRequests(t, asked(), 1)
}

// Rafiki stores the wallet address URL lowercased, so a provisioning run that
// asks with different casing has to recognise what is already there instead of
// trying to create a duplicate.
func TestEnsureWalletAddressIgnoresCase(t *testing.T) {
	srv, asked := adminStub(t, `{"data":{"walletAddresses":{"edges":[`+
		`{"node":{"id":"wa-9","address":"https://100.76.141.47:3000/tienda-cajadebarrio","publicName":"Don Jaime","asset":{"id":"asset-1","code":"COP","scale":2}}}`+
		`]}}}`)

	address, err := newTestClient(t, srv.URL).EnsureWalletAddress(context.Background(), rafikiadmin.CreateWalletAddressInput{
		AssetID: "asset-1",
		Address: "https://100.76.141.47:3000/Tienda-CajaDeBarrio",
	})
	if err != nil {
		t.Fatalf("EnsureWalletAddress() returned an error: %v", err)
	}
	if address.ID != "wa-9" {
		t.Errorf("address.ID = %q, want the existing %q", address.ID, "wa-9")
	}
	requireRequests(t, asked(), 1)
}

func TestEnsureWalletAddressCreatesTheAddressWhenItIsMissing(t *testing.T) {
	srv, asked := adminStub(t,
		walletAddressesReply,
		`{"data":{"createWalletAddress":{"walletAddress":`+
			`{"id":"wa-3","address":"app-cajadebarrio","publicName":"Caja de Barrio","asset":{"id":"asset-1","code":"COP","scale":2}}}}}`,
	)

	address, err := newTestClient(t, srv.URL).EnsureWalletAddress(context.Background(), rafikiadmin.CreateWalletAddressInput{
		AssetID:        "asset-1",
		Address:        "app-cajadebarrio",
		PublicName:     "Caja de Barrio",
		IdempotencyKey: "seed:app-cajadebarrio",
	})
	if err != nil {
		t.Fatalf("EnsureWalletAddress() returned an error: %v", err)
	}
	if address.ID != "wa-3" {
		t.Errorf("address.ID = %q, want the created %q", address.ID, "wa-3")
	}

	requests := asked()
	requireRequests(t, requests, 2)
	if !strings.Contains(requests[1].Query, "createWalletAddress") {
		t.Errorf("second query = %q, want a createWalletAddress mutation", requests[1].Query)
	}
}
