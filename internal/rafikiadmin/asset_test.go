package rafikiadmin_test

import (
	"context"
	"strings"
	"testing"
)

func TestAssetByCodeAndScaleFindsTheAsset(t *testing.T) {
	srv, asked := adminStub(t, `{"data":{"assetByCodeAndScale":{"id":"asset-1","code":"COP","scale":2,"liquidity":null}}}`)

	asset, found, err := newTestClient(t, srv.URL).AssetByCodeAndScale(context.Background(), "COP", 2)
	if err != nil {
		t.Fatalf("AssetByCodeAndScale() returned an error: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if asset.ID != "asset-1" || asset.Code != "COP" || asset.Scale != 2 {
		t.Errorf("asset = %+v, want the COP asset with ID asset-1", asset)
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "assetByCodeAndScale") {
		t.Errorf("query = %q, want an assetByCodeAndScale query", requests[0].Query)
	}
	requireVariables(t, requests[0].Variables, map[string]any{"code": "COP", "scale": 2})
}

// Rafiki answers null, not an error, when the asset does not exist yet.
func TestAssetByCodeAndScaleReportsAMissingAsset(t *testing.T) {
	srv, _ := adminStub(t, `{"data":{"assetByCodeAndScale":null}}`)

	_, found, err := newTestClient(t, srv.URL).AssetByCodeAndScale(context.Background(), "COP", 2)
	if err != nil {
		t.Fatalf("AssetByCodeAndScale() returned an error: %v", err)
	}
	if found {
		t.Error("found = true, want false for a null asset")
	}
}

func TestCreateAssetDecodesTheCreatedAsset(t *testing.T) {
	srv, asked := adminStub(t, `{"data":{"createAsset":{"asset":{"id":"asset-2","code":"COP","scale":2,"liquidity":"250"}}}}`)

	asset, err := newTestClient(t, srv.URL).CreateAsset(context.Background(), "COP", 2)
	if err != nil {
		t.Fatalf("CreateAsset() returned an error: %v", err)
	}
	if asset.ID != "asset-2" {
		t.Errorf("asset.ID = %q, want %q", asset.ID, "asset-2")
	}
	if asset.Liquidity != 250 {
		t.Errorf("asset.Liquidity = %d, want 250", asset.Liquidity)
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "createAsset") {
		t.Errorf("query = %q, want a createAsset mutation", requests[0].Query)
	}
	requireVariables(t, requests[0].Variables, map[string]any{
		"input": map[string]any{"code": "COP", "scale": 2},
	})
}

// The seed runs on every stack restart, so it must not try to create what is
// already there: Rafiki refuses a duplicate asset.
func TestEnsureAssetReusesAnExistingAsset(t *testing.T) {
	srv, asked := adminStub(t, `{"data":{"assetByCodeAndScale":{"id":"asset-1","code":"COP","scale":2,"liquidity":null}}}`)

	asset, err := newTestClient(t, srv.URL).EnsureAsset(context.Background(), "COP", 2)
	if err != nil {
		t.Fatalf("EnsureAsset() returned an error: %v", err)
	}
	if asset.ID != "asset-1" {
		t.Errorf("asset.ID = %q, want the existing %q", asset.ID, "asset-1")
	}
	requireRequests(t, asked(), 1)
}

func TestEnsureAssetCreatesTheAssetWhenItIsMissing(t *testing.T) {
	srv, asked := adminStub(t,
		`{"data":{"assetByCodeAndScale":null}}`,
		`{"data":{"createAsset":{"asset":{"id":"asset-2","code":"COP","scale":2,"liquidity":null}}}}`,
	)

	asset, err := newTestClient(t, srv.URL).EnsureAsset(context.Background(), "COP", 2)
	if err != nil {
		t.Fatalf("EnsureAsset() returned an error: %v", err)
	}
	if asset.ID != "asset-2" {
		t.Errorf("asset.ID = %q, want the created %q", asset.ID, "asset-2")
	}

	requests := asked()
	requireRequests(t, requests, 2)
	if !strings.Contains(requests[1].Query, "createAsset") {
		t.Errorf("second query = %q, want a createAsset mutation", requests[1].Query)
	}
}
