package rafikiadmin_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

// Rafiki validates the transfer identifier as an RFC 4122 version 4 UUID.
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func assetReply(liquidity string) string {
	return `{"data":{"asset":{"id":"asset-1","code":"COP","scale":2,"liquidity":"` + liquidity + `"}}}`
}

const liquidityReply = `{"data":{"depositAssetLiquidity":{"success":true}}}`

// inputOf returns the "input" object of a recorded request.
func inputOf(t *testing.T, request graphQLRequest) map[string]any {
	t.Helper()

	input, ok := request.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("request %q carried variables %v without an input object", request.Query, request.Variables)
	}

	return input
}

func TestEnsureAssetLiquidityDepositsOnlyTheDifference(t *testing.T) {
	srv, asked := adminStub(t, assetReply("300"), liquidityReply)

	deposited, err := newTestClient(t, srv.URL).EnsureAssetLiquidity(context.Background(), "asset-1", 1000)
	if err != nil {
		t.Fatalf("EnsureAssetLiquidity() returned an error: %v", err)
	}
	if deposited != 700 {
		t.Errorf("deposited = %d, want 700", deposited)
	}

	requests := asked()
	requireRequests(t, requests, 2)
	if !strings.Contains(requests[1].Query, "depositAssetLiquidity") {
		t.Errorf("second query = %q, want a depositAssetLiquidity mutation", requests[1].Query)
	}

	input := inputOf(t, requests[1])
	if input["assetId"] != "asset-1" {
		t.Errorf("input assetId = %v, want asset-1", input["assetId"])
	}
	if input["amount"] != "700" {
		t.Errorf("input amount = %#v, want the string \"700\": Rafiki takes UInt64 as a JSON string", input["amount"])
	}
	if input["idempotencyKey"] != "seed:liquidity:asset-1:700" {
		t.Errorf("input idempotencyKey = %v, want seed:liquidity:asset-1:700", input["idempotencyKey"])
	}
	id, _ := input["id"].(string)
	if !uuidV4Pattern.MatchString(id) {
		t.Errorf("input id = %q, want an RFC 4122 version 4 UUID", id)
	}
}

// The seed runs on every restart, so a funded asset must not be funded twice.
func TestEnsureAssetLiquidityDepositsNothingWhenTheTargetIsReached(t *testing.T) {
	srv, asked := adminStub(t, assetReply("1000"))

	deposited, err := newTestClient(t, srv.URL).EnsureAssetLiquidity(context.Background(), "asset-1", 1000)
	if err != nil {
		t.Fatalf("EnsureAssetLiquidity() returned an error: %v", err)
	}
	if deposited != 0 {
		t.Errorf("deposited = %d, want 0", deposited)
	}
	requireRequests(t, asked(), 1)
}

func TestEnsureAssetLiquidityRefusesAnAssetTheAseDoesNotHave(t *testing.T) {
	srv, asked := adminStub(t, `{"data":{"asset":null}}`)

	if _, err := newTestClient(t, srv.URL).EnsureAssetLiquidity(context.Background(), "asset-9", 1000); err == nil {
		t.Fatal("EnsureAssetLiquidity() accepted an unknown asset, want an error")
	}
	requireRequests(t, asked(), 1)
}

func TestDepositAssetLiquiditySendsTheTransfer(t *testing.T) {
	srv, asked := adminStub(t, liquidityReply)

	transfer := rafikiadmin.LiquidityTransfer{
		AssetID:        "asset-1",
		Amount:         700,
		ID:             "0f8fad5b-d9cb-469f-a165-70867728950e",
		IdempotencyKey: "seed:liquidity:asset-1:700",
	}
	if err := newTestClient(t, srv.URL).DepositAssetLiquidity(context.Background(), transfer); err != nil {
		t.Fatalf("DepositAssetLiquidity() returned an error: %v", err)
	}

	requests := asked()
	requireRequests(t, requests, 1)
	requireVariables(t, requests[0].Variables, map[string]any{
		"input": map[string]any{
			"assetId":        "asset-1",
			"amount":         rafikiadmin.UInt64(700),
			"id":             "0f8fad5b-d9cb-469f-a165-70867728950e",
			"idempotencyKey": "seed:liquidity:asset-1:700",
		},
	})
}

// Rafiki answers with success false instead of an error when the deposit is
// refused, and a silent failure would leave the demo without funds.
func TestDepositAssetLiquidityFailsWhenRafikiReportsNoSuccess(t *testing.T) {
	srv, _ := adminStub(t, `{"data":{"depositAssetLiquidity":{"success":false}}}`)

	transfer := rafikiadmin.LiquidityTransfer{
		AssetID:        "asset-1",
		Amount:         700,
		ID:             "0f8fad5b-d9cb-469f-a165-70867728950e",
		IdempotencyKey: "seed:liquidity:asset-1:700",
	}
	if err := newTestClient(t, srv.URL).DepositAssetLiquidity(context.Background(), transfer); err == nil {
		t.Fatal("DepositAssetLiquidity() ignored success false, want an error")
	}
}
