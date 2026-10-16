package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

func TestWalletAddressURL(t *testing.T) {
	if got, want := walletAddressURL("https://100.76.141.47:3000", "tienda-cajadebarrio"), "https://100.76.141.47:3000/tienda-cajadebarrio"; got != want {
		t.Errorf("walletAddressURL(...) = %q, want %q", got, want)
	}
}

func TestRequireHTTPSBaseDropsATrailingSlash(t *testing.T) {
	got, err := requireHTTPSBase("https://100.76.141.47:3000/")
	if err != nil {
		t.Fatalf("requireHTTPSBase() returned an error: %v", err)
	}
	if want := "https://100.76.141.47:3000"; got != want {
		t.Errorf("requireHTTPSBase() = %q, want %q", got, want)
	}
}

// The demo needs two currencies and five accounts, not the one currency and three
// accounts of the first version. This test pins that plan for review: it is a change
// detector, not a behaviour test.
func TestProvisioningPlanMatchesTheContract(t *testing.T) {
	want := []actor{
		{address: "tienda-cajadebarrio", publicName: "Don Jaime - Tienda", assetCode: assetCOP},
		{address: "laura-cajadebarrio", publicName: "Laura", assetCode: assetCOP},
		{address: appAddress, publicName: "Caja de Barrio", assetCode: assetCOP},
		{address: platformAddress, publicName: "Caja de Barrio - Comision", assetCode: assetCOP},
		// The contract (section 4) gives Rosa the USD address of Andres, who pays
		// the remittance, so there is deliberately no rosa-* wallet address.
		{address: "andres-cajadebarrio", publicName: "Andres", assetCode: assetUSD},
	}

	if !reflect.DeepEqual(actors, want) {
		t.Errorf("actors = %#v, want %#v", actors, want)
	}
}

// Every wallet address belongs to one of the provisioned assets, and Rafiki
// refuses a wallet address whose asset does not exist.
func TestEveryActorUsesAProvisionedAsset(t *testing.T) {
	provisioned := make(map[string]bool, len(assets))
	for _, a := range assets {
		provisioned[a.code] = true
	}

	for _, a := range actors {
		if !provisioned[a.assetCode] {
			t.Errorf("wallet address %s uses asset %s, which the seed does not provision", a.address, a.assetCode)
		}
	}
	for _, code := range []string{assetCOP, assetUSD} {
		if !provisioned[code] {
			t.Errorf("the seed provisions %v, want it to provision %s", provisioned, code)
		}
	}
}

// An asset with no liquidity cannot move money, so no payment of that currency
// can complete.
func TestEveryProvisionedAssetIsFunded(t *testing.T) {
	for _, a := range assets {
		if a.liquidity == 0 {
			t.Errorf("asset %s has liquidity 0, want a funded asset", a.code)
		}
	}
}

// The client key has to be active on both accounts this backend acts as. A loop
// that registered it on only one of them looks identical from the outside, so the
// seed reads back what the ASE holds and this is the function that decides.
func TestHasActiveKeyCountsOnlyTheNamedActiveKey(t *testing.T) {
	tests := []struct {
		name string
		keys []rafikiadmin.WalletAddressKey
		kid  string
		want bool
	}{
		{
			name: "an active key with that kid counts",
			keys: []rafikiadmin.WalletAddressKey{{JWK: rafikiadmin.JWK{Kid: "the-kid"}}},
			kid:  "the-kid",
			want: true,
		},
		{
			name: "the same kid revoked does not count",
			keys: []rafikiadmin.WalletAddressKey{{JWK: rafikiadmin.JWK{Kid: "the-kid"}, Revoked: true}},
			kid:  "the-kid",
			want: false,
		},
		{
			name: "another kid does not count",
			keys: []rafikiadmin.WalletAddressKey{{JWK: rafikiadmin.JWK{Kid: "another-kid"}}},
			kid:  "the-kid",
			want: false,
		},
		{
			name: "a revoked key of another kid does not make the named one count",
			keys: []rafikiadmin.WalletAddressKey{{JWK: rafikiadmin.JWK{Kid: "the-kid"}, Revoked: true}, {JWK: rafikiadmin.JWK{Kid: "another-kid"}}},
			kid:  "the-kid",
			want: false,
		},
		{
			name: "no keys at all",
			kid:  "the-kid",
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasActiveKey(test.keys, test.kid); got != test.want {
				t.Errorf("hasActiveKey(%+v, %q) = %t, want %t", test.keys, test.kid, got, test.want)
			}
		})
	}
}

// This test pins the plan the seed executes: it is a change detector for the two
// tables above, and it says nothing about what the seed does with them. The
// behaviour is covered by the read-back the seed performs (hasActiveKey) and by
// the live run recorded in L7 of odd/tasks/contrato-interno-y-pagos.md.
func TestThePlanNamesTheTwoAccountsThatCarryTheClientKey(t *testing.T) {
	want := map[string]bool{appAddress: true, platformAddress: true}

	got := make(map[string]bool, len(clientKeyAddresses))
	for _, address := range clientKeyAddresses {
		got[address] = true
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("clientKeyAddresses = %v, want %v", got, want)
	}
}

// Rafiki refuses any wallet address URL that is not https, so the seed reports
// the variable to fix instead of letting the Admin API answer "invalid url".
func TestRequireHTTPSBaseRejectsABaseRafikiCannotAccept(t *testing.T) {
	tests := map[string]string{
		"http base": "http://100.76.141.47:3000",
		"no scheme": "100.76.141.47:3000",
		"empty":     "",
	}

	for name, base := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := requireHTTPSBase(base)
			if err == nil {
				t.Fatalf("requireHTTPSBase(%q) accepted a base Rafiki rejects, want an error", base)
			}
			if !strings.Contains(err.Error(), "RAFIKI_OPEN_PAYMENTS_URL") {
				t.Errorf("error %q does not name RAFIKI_OPEN_PAYMENTS_URL", err)
			}
		})
	}
}
