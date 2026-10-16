package rafikiadmin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

const (
	keysReply = `{"data":{"walletAddress":{"walletAddressKeys":{"edges":[` +
		`{"node":{"id":"key-1","revoked":false,"jwk":{"kid":"kid-1","x":"x-1","alg":"EdDSA","kty":"OKP","crv":"Ed25519"}}}` +
		`]}}}}`

	noKeysReply    = `{"data":{"walletAddress":{"walletAddressKeys":{"edges":[]}}}}`
	createKeyReply = `{"data":{"createWalletAddressKey":{"walletAddressKey":` +
		`{"id":"key-2","revoked":false,"jwk":{"kid":"kid-2","x":"x-2","alg":"EdDSA","kty":"OKP","crv":"Ed25519"}}}}}`
)

// Rafiki accepts one key type on a wallet address: EdDSA over Ed25519 in JWK
// form, and nothing else.
func TestEd25519JWKUsesTheOnlyValuesRafikiAccepts(t *testing.T) {
	got := rafikiadmin.Ed25519JWK("kid-1", "x-1")

	want := rafikiadmin.JWK{Kid: "kid-1", X: "x-1", Alg: "EdDSA", Kty: "OKP", Crv: "Ed25519"}
	if got != want {
		t.Errorf("Ed25519JWK() = %+v, want %+v", got, want)
	}
}

func TestWalletAddressKeysDecodesTheRegisteredKeys(t *testing.T) {
	srv, asked := adminStub(t, keysReply)

	keys, err := newTestClient(t, srv.URL).WalletAddressKeys(context.Background(), "wa-3")
	if err != nil {
		t.Fatalf("WalletAddressKeys() returned an error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if keys[0].ID != "key-1" || keys[0].JWK.Kid != "kid-1" {
		t.Errorf("key = %+v, want key-1 with kid-1", keys[0])
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "walletAddressKeys") {
		t.Errorf("query = %q, want a walletAddressKeys query", requests[0].Query)
	}
	requireVariables(t, requests[0].Variables, map[string]any{"id": "wa-3"})
}

func TestCreateWalletAddressKeySendsTheJWK(t *testing.T) {
	srv, asked := adminStub(t, createKeyReply)

	created, err := newTestClient(t, srv.URL).CreateWalletAddressKey(context.Background(), rafikiadmin.CreateWalletAddressKeyInput{
		WalletAddressID: "wa-3",
		JWK:             rafikiadmin.Ed25519JWK("kid-2", "x-2"),
		IdempotencyKey:  "client-key:kid-2",
	})
	if err != nil {
		t.Fatalf("CreateWalletAddressKey() returned an error: %v", err)
	}
	if created.ID != "key-2" {
		t.Errorf("created.ID = %q, want %q", created.ID, "key-2")
	}

	requests := asked()
	requireRequests(t, requests, 1)
	if !strings.Contains(requests[0].Query, "createWalletAddressKey") {
		t.Errorf("query = %q, want a createWalletAddressKey mutation", requests[0].Query)
	}
	requireVariables(t, requests[0].Variables, map[string]any{
		"input": map[string]any{
			"walletAddressId": "wa-3",
			"jwk": map[string]any{
				"kid": "kid-2",
				"x":   "x-2",
				"alg": "EdDSA",
				"kty": "OKP",
				"crv": "Ed25519",
			},
			"idempotencyKey": "client-key:kid-2",
		},
	})
}

// A Go struct keeps its declaration order, so a nested struct that is not
// already alphabetical would make the bytes on the wire differ from the
// canonical form Rafiki re-computes before hashing. The client normalizes, and
// this pins the raw body.
func TestDoSendsNestedObjectsInCanonicalOrder(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Write([]byte(createKeyReply))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL).CreateWalletAddressKey(context.Background(), rafikiadmin.CreateWalletAddressKeyInput{
		WalletAddressID: "wa-3",
		JWK:             rafikiadmin.Ed25519JWK("kid-2", "x-2"),
		IdempotencyKey:  "client-key:kid-2",
	})
	if err != nil {
		t.Fatalf("CreateWalletAddressKey() returned an error: %v", err)
	}

	wantJWK := `"jwk":{"alg":"EdDSA","crv":"Ed25519","kid":"kid-2","kty":"OKP","x":"x-2"}`
	if !strings.Contains(string(got), wantJWK) {
		t.Errorf("request body %s does not carry the canonical JWK %s", got, wantJWK)
	}
}

// Rafiki refuses a key it already stores with the same kid, so registering the
// client identity again on every seed run has to look first.
func TestEnsureWalletAddressKeyDoesNothingWhenTheKidIsRegistered(t *testing.T) {
	srv, asked := adminStub(t, keysReply)

	err := newTestClient(t, srv.URL).EnsureWalletAddressKey(context.Background(), rafikiadmin.CreateWalletAddressKeyInput{
		WalletAddressID: "wa-3",
		JWK:             rafikiadmin.Ed25519JWK("kid-1", "x-1"),
	})
	if err != nil {
		t.Fatalf("EnsureWalletAddressKey() returned an error: %v", err)
	}
	requireRequests(t, asked(), 1)
}

func TestEnsureWalletAddressKeyRegistersTheKeyWhenItIsMissing(t *testing.T) {
	srv, asked := adminStub(t, noKeysReply, createKeyReply)

	err := newTestClient(t, srv.URL).EnsureWalletAddressKey(context.Background(), rafikiadmin.CreateWalletAddressKeyInput{
		WalletAddressID: "wa-3",
		JWK:             rafikiadmin.Ed25519JWK("kid-2", "x-2"),
		IdempotencyKey:  "client-key:kid-2",
	})
	if err != nil {
		t.Fatalf("EnsureWalletAddressKey() returned an error: %v", err)
	}

	requests := asked()
	requireRequests(t, requests, 2)
	if !strings.Contains(requests[1].Query, "createWalletAddressKey") {
		t.Errorf("second query = %q, want a createWalletAddressKey mutation", requests[1].Query)
	}
}

// A revoked key no longer verifies anything, so it has to be registered again.
func TestEnsureWalletAddressKeyRegistersARevokedKeyAgain(t *testing.T) {
	revokedReply := `{"data":{"walletAddress":{"walletAddressKeys":{"edges":[` +
		`{"node":{"id":"key-1","revoked":true,"jwk":{"kid":"kid-1","x":"x-1","alg":"EdDSA","kty":"OKP","crv":"Ed25519"}}}` +
		`]}}}}`
	srv, asked := adminStub(t, revokedReply, createKeyReply)

	err := newTestClient(t, srv.URL).EnsureWalletAddressKey(context.Background(), rafikiadmin.CreateWalletAddressKeyInput{
		WalletAddressID: "wa-3",
		JWK:             rafikiadmin.Ed25519JWK("kid-1", "x-1"),
		IdempotencyKey:  "client-key:kid-1",
	})
	if err != nil {
		t.Fatalf("EnsureWalletAddressKey() returned an error: %v", err)
	}
	requireRequests(t, asked(), 2)
}
