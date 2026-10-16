package opclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opkeys"
)

// newKey writes a fresh Ed25519 key pair and returns its path plus the kid both
// sides derive from the public key.
func newKey(t *testing.T) (path, kid string) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "op-private-key.pem")
	key, err := opkeys.LoadOrGenerate(path)
	if err != nil {
		t.Fatalf("opkeys.LoadOrGenerate() returned an error: %v", err)
	}

	return path, key.Kid()
}

// httpsURL is the address of a stub as the ASE publishes it: the scheme Rafiki
// requires from a wallet address.
func httpsURL(srv *httptest.Server) string {
	return "https://" + strings.TrimPrefix(srv.URL, "http://")
}

// walletAddressDocument is the document Rafiki serves for a wallet address, with
// the fields verified live against the local stack.
func walletAddressDocument(id, authServer, resourceServer string) string {
	return fmt.Sprintf(`{"id":%q,"publicName":"Laura","assetCode":"COP","assetScale":2,"authServer":%q,"resourceServer":%q}`,
		id, authServer, resourceServer)
}

// The client reports a value it cannot use at construction, not at the first
// request: an incomplete address or a key that is not there would otherwise turn
// into a confusing failure of the withdrawal.
func TestNewRejectsAConfigItCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)

	tests := []struct {
		name   string
		config opclient.Config
		want   string
	}{
		{
			name:   "no wallet address",
			config: opclient.Config{PrivateKeyPath: keyPath, KeyID: kid},
			want:   "WalletAddressURL",
		},
		{
			name:   "wallet address in the $ilp form",
			config: opclient.Config{WalletAddressURL: "$ilp.interledger-test.dev/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid},
			want:   "WalletAddressURL",
		},
		{
			name:   "wallet address without a host",
			config: opclient.Config{WalletAddressURL: "rafiki-backend:3000", PrivateKeyPath: keyPath, KeyID: kid},
			want:   "WalletAddressURL",
		},
		{
			name:   "no key path",
			config: opclient.Config{WalletAddressURL: "https://rafiki-backend:3000/app-cajadebarrio", KeyID: kid},
			want:   "PrivateKeyPath",
		},
		{
			name:   "key path that does not exist",
			config: opclient.Config{WalletAddressURL: "https://rafiki-backend:3000/app-cajadebarrio", PrivateKeyPath: filepath.Join(t.TempDir(), "missing.pem"), KeyID: kid},
			want:   "missing.pem",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := opclient.New(test.config)
			if err == nil {
				t.Fatalf("New(%+v) accepted a config it cannot use, want an error", test.config)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}

// A file that exists but is not a PKCS#8 Ed25519 PEM cannot sign anything, so it
// is rejected at construction.
func TestNewRejectsAFileThatIsNotAnEd25519PEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-key.pem")
	if err := os.WriteFile(path, []byte("this is not a key"), 0o600); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}

	_, err := opclient.New(opclient.Config{
		WalletAddressURL: "https://rafiki-backend:3000/app-cajadebarrio",
		PrivateKeyPath:   path,
	})
	if err == nil {
		t.Fatal("New() accepted a file that is not an Ed25519 PEM, want an error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
}

// The ASE publishes its wallet addresses in https because Rafiki refuses any other
// scheme, but the local stack has no TLS on that port. The stub only speaks http,
// so reaching it at all is the proof that the client downgraded that host.
func TestResolveWalletAddressReadsTheDocumentOverHTTP(t *testing.T) {
	keyPath, kid := newKey(t)

	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, walletAddressDocument(
			"https://"+r.Host+r.URL.Path,
			"http://rafiki-auth:3006/tenant",
			"http://rafiki-backend:3000/tenant",
		))
	}))
	defer srv.Close()

	addressURL := httpsURL(srv) + "/laura-cajadebarrio"

	client, err := opclient.New(opclient.Config{
		WalletAddressURL: addressURL,
		PrivateKeyPath:   keyPath,
		KeyID:            kid,
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.ResolveWalletAddress(context.Background(), addressURL)
	if err != nil {
		t.Fatalf("ResolveWalletAddress() returned an error: %v", err)
	}

	want := opclient.WalletAddress{
		URL:            addressURL,
		AuthServer:     "http://rafiki-auth:3006/tenant",
		ResourceServer: "http://rafiki-backend:3000/tenant",
		AssetCode:      "COP",
		AssetScale:     2,
	}
	if got != want {
		t.Errorf("ResolveWalletAddress() = %+v, want %+v", got, want)
	}
	if requestedPath != "/laura-cajadebarrio" {
		t.Errorf("the ASE was asked for %q, want %q", requestedPath, "/laura-cajadebarrio")
	}
}

// The downgrade is not a blanket rule: only the host this client identifies itself
// by loses its scheme. Another ASE keeps the https it published, so a stack that
// does have TLS is called over TLS.
func TestResolveWalletAddressLeavesAnotherHostAlone(t *testing.T) {
	keyPath, kid := newKey(t)

	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer own.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the other ASE was called over http, want the https it published")
	}))
	defer other.Close()

	client, err := opclient.New(opclient.Config{
		WalletAddressURL: httpsURL(own) + "/app-cajadebarrio",
		PrivateKeyPath:   keyPath,
		KeyID:            kid,
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	if _, err := client.ResolveWalletAddress(context.Background(), httpsURL(other)+"/laura-cajadebarrio"); err == nil {
		t.Fatal("ResolveWalletAddress() downgraded a host that is not the one this client identifies itself by, want an error")
	}
}

// Without the two servers of the document there is no flow to run: the caller
// learns which wallet address is misconfigured instead of failing later inside the
// authorization server.
func TestResolveWalletAddressRejectsADocumentWithoutItsServers(t *testing.T) {
	keyPath, kid := newKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"https://rafiki-backend:3000/laura-cajadebarrio","assetCode":"COP","assetScale":2}`)
	}))
	defer srv.Close()

	addressURL := httpsURL(srv) + "/laura-cajadebarrio"

	client, err := opclient.New(opclient.Config{
		WalletAddressURL: addressURL,
		PrivateKeyPath:   keyPath,
		KeyID:            kid,
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	_, err = client.ResolveWalletAddress(context.Background(), addressURL)
	if err == nil {
		t.Fatal("ResolveWalletAddress() accepted a document without an authorization and resource server, want an error")
	}
	if !strings.Contains(err.Error(), addressURL) {
		t.Errorf("error %q does not name %s", err, addressURL)
	}
}

// nonInteractiveGrant is the GNAP response of an authorization server that hands
// out the access token without any interaction.
func nonInteractiveGrant(token string) string {
	return fmt.Sprintf(`{"access_token":{"access":[],"value":%q,"manage":"https://rafiki-auth:3006/token"},"continue":{"access_token":{"value":"continue-token","manage":"https://rafiki-auth:3006/continue"}}}`, token)
}

// newAuthServer stands in for the authorization server of one account. It
// captures every grant request it receives and answers each one with a
// non-interactive grant carrying the given token.
func newAuthServer(t *testing.T, token string) (*httptest.Server, func() []map[string]any) {
	t.Helper()

	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the grant request body is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, nonInteractiveGrant(token))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), requests...)
	}
}

// grantAccess is the first access item of a captured grant request body.
func grantAccess(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	token, ok := body["access_token"].(map[string]any)
	if !ok {
		t.Fatalf("grant request without access_token: %v", body)
	}
	access, ok := token["access"].([]any)
	if !ok || len(access) != 1 {
		t.Fatalf("grant request without exactly one access item: %v", token)
	}
	item, ok := access[0].(map[string]any)
	if !ok {
		t.Fatalf("grant access item is not an object: %v", access[0])
	}
	return item
}

// The store and the platform are two collection points with one non-interactive
// grant each: incoming-payment create/read/complete on that one wallet address,
// and one incoming payment for the exact whole-pesos amount in its smallest unit.
func TestCreateIncomingPayments(t *testing.T) {
	keyPath, kid := newKey(t)

	storeAuth, storeGrants := newAuthServer(t, "token-tienda")
	platformAuth, platformGrants := newAuthServer(t, "token-plataforma")

	var mu sync.Mutex
	var created []map[string]any
	var authorizations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/tienda-cajadebarrio":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, walletAddressDocument(
				"https://"+r.Host+r.URL.Path,
				storeAuth.URL,
				"https://"+r.Host+"/tenant",
			))
		case r.Method == http.MethodGet && r.URL.Path == "/app-cajadebarrio-cop":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, walletAddressDocument(
				"https://"+r.Host+r.URL.Path,
				platformAuth.URL,
				"https://"+r.Host+"/tenant",
			))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/incoming-payments"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("the incoming payment body is not JSON: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			created = append(created, body)
			authorizations = append(authorizations, r.Header.Get("Authorization"))
			mu.Unlock()

			incomingAmount, _ := json.Marshal(body["incomingAmount"])
			walletAddress := "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/incoming-payments")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%q,"walletAddress":%q,"completed":false,"incomingAmount":%s,"receivedAmount":{"assetCode":"COP","assetScale":2,"value":"0"},"methods":[]}`,
				walletAddress+"/incoming-payments/ip-1", walletAddress, incomingAmount)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	storeURL := httpsURL(srv) + "/tienda-cajadebarrio"
	platformURL := httpsURL(srv) + "/app-cajadebarrio-cop"
	ownURL := httpsURL(srv) + "/app-cajadebarrio"

	client, err := opclient.New(opclient.Config{WalletAddressURL: ownURL, PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.CreateIncomingPayments(context.Background(), opclient.CreateIncomingPaymentsParams{
		StoreWalletAddress:    storeURL,
		StoreAmountCOP:        203000,
		PlatformWalletAddress: platformURL,
		PlatformAmountCOP:     2000,
	})
	if err != nil {
		t.Fatalf("CreateIncomingPayments() returned an error: %v", err)
	}

	// The store grant goes to the store auth server, with the incoming-payment
	// access on the store wallet address and the client this backend acts as.
	storeRequests := storeGrants()
	if len(storeRequests) != 1 {
		t.Fatalf("the store auth server received %d grant requests, want 1", len(storeRequests))
	}
	storeItem := grantAccess(t, storeRequests[0])
	if storeItem["type"] != "incoming-payment" {
		t.Errorf("store grant access type = %v, want incoming-payment", storeItem["type"])
	}
	if fmt.Sprint(storeItem["actions"]) != "[create read complete]" {
		t.Errorf("store grant access actions = %v, want [create read complete]", storeItem["actions"])
	}
	if storeItem["identifier"] != storeURL {
		t.Errorf("store grant access identifier = %v, want %s", storeItem["identifier"], storeURL)
	}
	if clientField, ok := storeRequests[0]["client"].(map[string]any); !ok || clientField["walletAddress"] != ownURL {
		t.Errorf("store grant client = %v, want the wallet address this client acts as (%s)", storeRequests[0]["client"], ownURL)
	}

	// The platform grant goes to the platform auth server, on the platform wallet.
	platformRequests := platformGrants()
	if len(platformRequests) != 1 {
		t.Fatalf("the platform auth server received %d grant requests, want 1", len(platformRequests))
	}
	platformItem := grantAccess(t, platformRequests[0])
	if platformItem["type"] != "incoming-payment" {
		t.Errorf("platform grant access type = %v, want incoming-payment", platformItem["type"])
	}
	if platformItem["identifier"] != platformURL {
		t.Errorf("platform grant access identifier = %v, want %s", platformItem["identifier"], platformURL)
	}

	if len(created) != 2 {
		t.Fatalf("the resource server received %d incoming payments, want 2", len(created))
	}

	// The store incoming payment carries the wallet address and the whole-pesos
	// amount already converted to the smallest unit of its asset (scale 2).
	if created[0]["walletAddress"] != storeURL {
		t.Errorf("store incoming payment walletAddress = %v, want %s", created[0]["walletAddress"], storeURL)
	}
	storeAmount, ok := created[0]["incomingAmount"].(map[string]any)
	if !ok {
		t.Fatalf("store incoming payment has no incomingAmount: %v", created[0])
	}
	if storeAmount["value"] != "20300000" {
		t.Errorf("store incoming payment value = %v, want 20300000 (203000 pesos at scale 2)", storeAmount["value"])
	}
	if storeAmount["assetCode"] != "COP" || storeAmount["assetScale"] != float64(2) {
		t.Errorf("store incoming payment asset = %v, want COP at scale 2", storeAmount)
	}
	if authorizations[0] != "GNAP token-tienda" {
		t.Errorf("store incoming payment authorization = %q, want the store grant token", authorizations[0])
	}

	// The platform incoming payment, with the platform grant token and its amount.
	if created[1]["walletAddress"] != platformURL {
		t.Errorf("platform incoming payment walletAddress = %v, want %s", created[1]["walletAddress"], platformURL)
	}
	platformAmount, ok := created[1]["incomingAmount"].(map[string]any)
	if !ok {
		t.Fatalf("platform incoming payment has no incomingAmount: %v", created[1])
	}
	if platformAmount["value"] != "200000" {
		t.Errorf("platform incoming payment value = %v, want 200000 (2000 pesos at scale 2)", platformAmount["value"])
	}
	if authorizations[1] != "GNAP token-plataforma" {
		t.Errorf("platform incoming payment authorization = %q, want the platform grant token", authorizations[1])
	}

	// What comes back identifies both collection points with the confirmed amount.
	wantStore := opclient.IncomingPayment{
		URL:           storeURL + "/incoming-payments/ip-1",
		WalletAddress: storeURL,
		Amount:        opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"},
	}
	if got.Store != wantStore {
		t.Errorf("Store = %+v, want %+v", got.Store, wantStore)
	}
	wantPlatform := opclient.IncomingPayment{
		URL:           platformURL + "/incoming-payments/ip-1",
		WalletAddress: platformURL,
		Amount:        opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "200000"},
	}
	if got.Platform != wantPlatform {
		t.Errorf("Platform = %+v, want %+v", got.Platform, wantPlatform)
	}
}

// Two quotes from the client wallet, one to each incoming payment, method ilp,
// with the debit in the client's own asset; the total debit is the sum of both
// and the expiry is the earliest of the two.
func TestCreateQuotes(t *testing.T) {
	keyPath, kid := newKey(t)

	clientAuth, quoteGrants := newAuthServer(t, "token-cotizacion")

	var mu sync.Mutex
	var quotes []map[string]any
	var authorizations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/laura-cajadebarrio":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, walletAddressDocument(
				"https://"+r.Host+r.URL.Path,
				clientAuth.URL,
				"https://"+r.Host+"/tenant",
			))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/quotes"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("the quote body is not JSON: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			index := len(quotes)
			quotes = append(quotes, body)
			authorizations = append(authorizations, r.Header.Get("Authorization"))
			mu.Unlock()

			// The platform quote expires first, so the earliest expiry is not
			// just the expiry of the first quote that comes back.
			expiresAt := "2026-10-17T15:10:00Z"
			value := "20300000"
			if index == 1 {
				expiresAt = "2026-10-17T15:05:00Z"
				value = "200000"
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%q,"method":"ilp","walletAddress":%q,"receiver":%q,"debitAmount":{"assetCode":"COP","assetScale":2,"value":%q},"receiveAmount":{"assetCode":"COP","assetScale":2,"value":%q},"expiresAt":%q}`,
				"https://"+r.Host+r.URL.Path+"/q1", "https://"+r.Host+"/laura-cajadebarrio", body["receiver"], value, value, expiresAt)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	clientURL := httpsURL(srv) + "/laura-cajadebarrio"
	storeReceiver := "https://rafiki-backend:3000/tienda-cajadebarrio/incoming-payments/ip-tienda"
	platformReceiver := "https://rafiki-backend:3000/app-cajadebarrio-cop/incoming-payments/ip-plataforma"

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.CreateQuotes(context.Background(), opclient.CreateQuotesParams{
		ClientWalletAddress: clientURL,
		StoreReceiver:       storeReceiver,
		StoreAmountCOP:      203000,
		PlatformReceiver:    platformReceiver,
		PlatformAmountCOP:   2000,
	})
	if err != nil {
		t.Fatalf("CreateQuotes() returned an error: %v", err)
	}

	// One quote grant, non-interactive, for quote create/read at the client's
	// auth server; the quote access item carries no identifier.
	requests := quoteGrants()
	if len(requests) != 1 {
		t.Fatalf("the client auth server received %d grant requests, want 1", len(requests))
	}
	item := grantAccess(t, requests[0])
	if item["type"] != "quote" {
		t.Errorf("quote grant access type = %v, want quote", item["type"])
	}
	if fmt.Sprint(item["actions"]) != "[create read]" {
		t.Errorf("quote grant access actions = %v, want [create read]", item["actions"])
	}
	if _, hasIdentifier := item["identifier"]; hasIdentifier {
		t.Errorf("quote grant access carries an identifier: %v", item["identifier"])
	}

	if len(quotes) != 2 {
		t.Fatalf("the resource server received %d quotes, want 2", len(quotes))
	}

	// Each quote is sent from the client wallet with method ilp, the receiver it
	// pays and the debit amount of that collection point in the smallest unit.
	wantReceivers := []string{storeReceiver, platformReceiver}
	wantValues := []string{"20300000", "200000"}
	for i, quote := range quotes {
		if quote["walletAddress"] != clientURL {
			t.Errorf("quote %d walletAddress = %v, want %s", i, quote["walletAddress"], clientURL)
		}
		if quote["method"] != "ilp" {
			t.Errorf("quote %d method = %v, want ilp", i, quote["method"])
		}
		if quote["receiver"] != wantReceivers[i] {
			t.Errorf("quote %d receiver = %v, want %s", i, quote["receiver"], wantReceivers[i])
		}
		debit, ok := quote["debitAmount"].(map[string]any)
		if !ok {
			t.Fatalf("quote %d has no debitAmount: %v", i, quote)
		}
		if debit["value"] != wantValues[i] {
			t.Errorf("quote %d debit value = %v, want %s", i, debit["value"], wantValues[i])
		}
		if debit["assetCode"] != "COP" || debit["assetScale"] != float64(2) {
			t.Errorf("quote %d debit asset = %v, want COP at scale 2", i, debit)
		}
		if authorizations[i] != "GNAP token-cotizacion" {
			t.Errorf("quote %d authorization = %q, want the quote grant token", i, authorizations[i])
		}
	}

	// Both quotes, the total debit in the client asset and the earliest expiry.
	if got.Store.URL != clientURL+"/quotes/q1" || got.Platform.URL != clientURL+"/quotes/q1" {
		t.Errorf("quote URLs = %q and %q, want the q1 quote of the client wallet", got.Store.URL, got.Platform.URL)
	}
	wantStoreDebit := opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"}
	if got.Store.DebitAmount != wantStoreDebit || got.Store.ReceiveAmount != wantStoreDebit {
		t.Errorf("Store amounts = %+v / %+v, want %+v", got.Store.DebitAmount, got.Store.ReceiveAmount, wantStoreDebit)
	}
	if got.Store.ExpiresAt != "2026-10-17T15:10:00Z" {
		t.Errorf("Store.ExpiresAt = %q, want 2026-10-17T15:10:00Z", got.Store.ExpiresAt)
	}
	wantPlatformDebit := opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "200000"}
	if got.Platform.DebitAmount != wantPlatformDebit || got.Platform.ReceiveAmount != wantPlatformDebit {
		t.Errorf("Platform amounts = %+v / %+v, want %+v", got.Platform.DebitAmount, got.Platform.ReceiveAmount, wantPlatformDebit)
	}
	if got.Platform.ExpiresAt != "2026-10-17T15:05:00Z" {
		t.Errorf("Platform.ExpiresAt = %q, want 2026-10-17T15:05:00Z", got.Platform.ExpiresAt)
	}
	wantDebit := opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20500000"}
	if got.Debit != wantDebit {
		t.Errorf("Debit = %+v, want %+v", got.Debit, wantDebit)
	}
	if got.ExpiresAt != "2026-10-17T15:05:00Z" {
		t.Errorf("ExpiresAt = %q, want the earliest of the two quotes", got.ExpiresAt)
	}
}

// The callers speak whole pesos; the smallest unit is pesos times 10^assetScale.
func TestSmallestUnit(t *testing.T) {
	tests := []struct {
		name       string
		amount     int64
		assetScale int
		want       string
	}{
		{name: "zero stays zero", amount: 0, assetScale: 2, want: "0"},
		{name: "scale 2", amount: 200000, assetScale: 2, want: "20000000"},
		{name: "scale 0", amount: 200000, assetScale: 0, want: "200000"},
		{name: "cents", amount: 51, assetScale: 2, want: "5100"},
		{name: "scale 18", amount: 1, assetScale: 18, want: "1000000000000000000"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := opclient.SmallestUnit(test.amount, test.assetScale)
			if err != nil {
				t.Fatalf("SmallestUnit(%d, %d) returned an error: %v", test.amount, test.assetScale, err)
			}
			if got != test.want {
				t.Errorf("SmallestUnit(%d, %d) = %q, want %q", test.amount, test.assetScale, got, test.want)
			}
		})
	}

	// A panic in a running backend takes the process down, so a value a caller
	// cannot use is an error, not a crash.
	t.Run("negative scale is rejected", func(t *testing.T) {
		_, err := opclient.SmallestUnit(1, -1)
		if err == nil {
			t.Fatal("SmallestUnit(1, -1) accepted a negative scale, want an error")
		}
		if !strings.Contains(err.Error(), "assetScale") {
			t.Errorf("error %q does not name assetScale", err)
		}
	})

	t.Run("an amount that overflows is rejected", func(t *testing.T) {
		_, err := opclient.SmallestUnit(math.MaxInt64, 2)
		if err == nil {
			t.Fatal("SmallestUnit(MaxInt64, 2) accepted an overflow, want an error")
		}
		if !strings.Contains(err.Error(), "overflow") {
			t.Errorf("error %q does not name the overflow", err)
		}
	})
}

// The withdrawal cannot run with a collection point it cannot name or an amount
// that makes no sense; the error says which parameter is wrong.
func TestCreateIncomingPaymentsRejectsParamsItCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)
	srv, _ := newAuthServer(t, "token")
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	store := httpsURL(srv) + "/tienda-cajadebarrio"
	platform := httpsURL(srv) + "/app-cajadebarrio-cop"

	tests := []struct {
		name   string
		params opclient.CreateIncomingPaymentsParams
		want   string
	}{
		{
			name:   "no store wallet address",
			params: opclient.CreateIncomingPaymentsParams{StoreAmountCOP: 203000, PlatformWalletAddress: platform, PlatformAmountCOP: 2000},
			want:   "StoreWalletAddress",
		},
		{
			name:   "store wallet address in the $ilp form",
			params: opclient.CreateIncomingPaymentsParams{StoreWalletAddress: "$ilp.interledger-test.dev/tienda-cajadebarrio", StoreAmountCOP: 203000, PlatformWalletAddress: platform, PlatformAmountCOP: 2000},
			want:   "StoreWalletAddress",
		},
		{
			name:   "no platform wallet address",
			params: opclient.CreateIncomingPaymentsParams{StoreWalletAddress: store, StoreAmountCOP: 203000, PlatformAmountCOP: 2000},
			want:   "PlatformWalletAddress",
		},
		{
			name:   "zero store amount",
			params: opclient.CreateIncomingPaymentsParams{StoreWalletAddress: store, PlatformWalletAddress: platform, PlatformAmountCOP: 2000},
			want:   "StoreAmountCOP",
		},
		{
			name:   "negative platform amount",
			params: opclient.CreateIncomingPaymentsParams{StoreWalletAddress: store, StoreAmountCOP: 203000, PlatformWalletAddress: platform, PlatformAmountCOP: -1},
			want:   "PlatformAmountCOP",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.CreateIncomingPayments(context.Background(), test.params)
			if err == nil {
				t.Fatalf("CreateIncomingPayments(%+v) accepted params it cannot use, want an error", test.params)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}

// A quote needs the wallet it debits, the incoming payment it pays and an amount
// in whole pesos for each collection point.
func TestCreateQuotesRejectsParamsItCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)
	srv, _ := newAuthServer(t, "token")
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	wallet := httpsURL(srv) + "/laura-cajadebarrio"
	storeReceiver := "https://rafiki-backend:3000/tienda-cajadebarrio/incoming-payments/ip-tienda"
	platformReceiver := "https://rafiki-backend:3000/app-cajadebarrio-cop/incoming-payments/ip-plataforma"

	tests := []struct {
		name   string
		params opclient.CreateQuotesParams
		want   string
	}{
		{
			name:   "no client wallet address",
			params: opclient.CreateQuotesParams{StoreReceiver: storeReceiver, StoreAmountCOP: 203000, PlatformReceiver: platformReceiver, PlatformAmountCOP: 2000},
			want:   "ClientWalletAddress",
		},
		{
			name:   "no store receiver",
			params: opclient.CreateQuotesParams{ClientWalletAddress: wallet, StoreAmountCOP: 203000, PlatformReceiver: platformReceiver, PlatformAmountCOP: 2000},
			want:   "StoreReceiver",
		},
		{
			name:   "no platform receiver",
			params: opclient.CreateQuotesParams{ClientWalletAddress: wallet, StoreReceiver: storeReceiver, StoreAmountCOP: 203000, PlatformAmountCOP: 2000},
			want:   "PlatformReceiver",
		},
		{
			name:   "zero store amount",
			params: opclient.CreateQuotesParams{ClientWalletAddress: wallet, StoreReceiver: storeReceiver, PlatformReceiver: platformReceiver, PlatformAmountCOP: 2000},
			want:   "StoreAmountCOP",
		},
		{
			name:   "negative platform amount",
			params: opclient.CreateQuotesParams{ClientWalletAddress: wallet, StoreReceiver: storeReceiver, PlatformReceiver: platformReceiver, StoreAmountCOP: 203000, PlatformAmountCOP: -1},
			want:   "PlatformAmountCOP",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.CreateQuotes(context.Background(), test.params)
			if err == nil {
				t.Fatalf("CreateQuotes(%+v) accepted params it cannot use, want an error", test.params)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}

// The collection runs unattended: an authorization server that answers with an
// interactive grant, or without a token at all, stops the flow instead of
// leaving the caller without a usable credential.
func TestCreateIncomingPaymentsRequiresANonInteractiveGrant(t *testing.T) {
	keyPath, kid := newKey(t)

	tests := []struct {
		name        string
		grantBody   string
		wantInError string
	}{
		{
			name:        "an interactive grant",
			grantBody:   `{"interact":{"redirect":"https://auth/interact","finish":"https://auth/finish"},"continue":{"access_token":{"value":"continue-token","manage":"https://auth/continue"}}}`,
			wantInError: "non-interactive",
		},
		{
			name:        "no access token",
			grantBody:   `{"continue":{"access_token":{"value":"continue-token","manage":"https://auth/continue"}}}`,
			wantInError: "access token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, test.grantBody)
			}))
			defer auth.Close()

			resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, walletAddressDocument(
					"https://"+r.Host+r.URL.Path,
					auth.URL,
					"https://"+r.Host+"/tenant",
				))
			}))
			defer resource.Close()

			client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
			if err != nil {
				t.Fatalf("New() returned an error: %v", err)
			}

			_, err = client.CreateIncomingPayments(context.Background(), opclient.CreateIncomingPaymentsParams{
				StoreWalletAddress:    httpsURL(resource) + "/tienda-cajadebarrio",
				StoreAmountCOP:        203000,
				PlatformWalletAddress: httpsURL(resource) + "/app-cajadebarrio-cop",
				PlatformAmountCOP:     2000,
			})
			if err == nil {
				t.Fatal("CreateIncomingPayments() accepted a grant it cannot run unattended, want an error")
			}
			if !strings.Contains(err.Error(), test.wantInError) {
				t.Errorf("error %q does not mention %q", err, test.wantInError)
			}
		})
	}
}

// interactiveGrant is the GNAP response of an authorization server that starts
// an interaction: where the user approves and the finish key of the callback.
func interactiveGrant(redirect, serverNonce, continueURI, continueToken string) string {
	return fmt.Sprintf(`{"interact":{"redirect":%q,"finish":%q},"continue":{"access_token":{"value":%q},"uri":%q}}`,
		redirect, serverNonce, continueToken, continueURI)
}

// The interactive half of the withdrawal (contract 8.2): the grant of
// outgoing-payment that caps the cupo carries the debit limit in the smallest
// unit of the client's own asset, an interval the caller builds, and a finish
// block whose nonce travels back to the caller for the callback hash.
func TestRequestOutgoingPaymentGrant(t *testing.T) {
	keyPath, kid := newKey(t)

	var mu sync.Mutex
	var grantRequests []map[string]any
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the grant request body is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		grantRequests = append(grantRequests, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, interactiveGrant(
			"https://wallet.example/grant-interactions/xyz",
			"server-nonce-1",
			"https://auth.example/continue/grant-1",
			"PDP_continue_1",
		))
	}))
	defer auth.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, walletAddressDocument(
			"https://"+r.Host+r.URL.Path,
			auth.URL,
			"https://"+r.Host+"/tenant",
		))
	}))
	defer resource.Close()

	clientWallet := httpsURL(resource) + "/laura-cajadebarrio"

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.RequestOutgoingPaymentGrant(context.Background(), opclient.OutgoingPaymentGrantParams{
		ClientWalletAddress: clientWallet,
		DebitLimit:          1000000,
		Interval:            "R/2026-10-16T00:00:00Z/P7D",
		FinishURL:           "https://tunel.example/op/finish",
		GrantID:             "gra_8f2a",
	})
	if err != nil {
		t.Fatalf("RequestOutgoingPaymentGrant() returned an error: %v", err)
	}

	if len(grantRequests) != 1 {
		t.Fatalf("the client auth server received %d grant requests, want 1", len(grantRequests))
	}
	request := grantRequests[0]

	// One outgoing-payment access item, scoped to the client wallet, with the
	// ceiling of the cupo already converted to the smallest unit of its asset.
	item := grantAccess(t, request)
	if item["type"] != "outgoing-payment" {
		t.Errorf("grant access type = %v, want outgoing-payment", item["type"])
	}
	if fmt.Sprint(item["actions"]) != "[create read]" {
		t.Errorf("grant access actions = %v, want [create read]", item["actions"])
	}
	if item["identifier"] != clientWallet {
		t.Errorf("grant access identifier = %v, want %s", item["identifier"], clientWallet)
	}
	limits, ok := item["limits"].(map[string]any)
	if !ok {
		t.Fatalf("grant access has no limits: %v", item)
	}
	debit, ok := limits["debitAmount"].(map[string]any)
	if !ok {
		t.Fatalf("limits have no debitAmount: %v", limits)
	}
	if debit["value"] != "100000000" {
		t.Errorf("debit value = %v, want 100000000 (1000000 pesos at scale 2)", debit["value"])
	}
	if debit["assetCode"] != "COP" || debit["assetScale"] != float64(2) {
		t.Errorf("debit asset = %v, want COP at scale 2", debit)
	}
	if limits["interval"] != "R/2026-10-16T00:00:00Z/P7D" {
		t.Errorf("limits interval = %v, want the interval the caller passed", limits["interval"])
	}

	// The finish block carries the redirect method, the URI built from the
	// caller's grant id, and the nonce the caller keeps for the hash.
	interact, ok := request["interact"].(map[string]any)
	if !ok {
		t.Fatalf("grant request has no interact block: %v", request)
	}
	finish, ok := interact["finish"].(map[string]any)
	if !ok {
		t.Fatalf("interact block has no finish: %v", interact)
	}
	if finish["method"] != "redirect" {
		t.Errorf("finish method = %v, want redirect", finish["method"])
	}
	if finish["uri"] != "https://tunel.example/op/finish?grant_id=gra_8f2a" {
		t.Errorf("finish uri = %v, want the finish URL with the caller's grant id", finish["uri"])
	}
	if finish["nonce"] != got.ClientNonce {
		t.Errorf("finish nonce = %v, want the nonce returned to the caller", finish["nonce"])
	}
	if start, ok := interact["start"]; !ok || fmt.Sprint(start) != "[redirect]" {
		t.Errorf("interact start = %v, want [redirect]", start)
	}

	// What comes back is everything the callback needs (contract 8.3).
	want := opclient.OutgoingPaymentGrant{
		ID:            "gra_8f2a",
		RedirectURL:   "https://wallet.example/grant-interactions/xyz",
		ContinueURI:   "https://auth.example/continue/grant-1",
		ContinueToken: "PDP_continue_1",
		ClientNonce:   got.ClientNonce,
		ServerNonce:   "server-nonce-1",
		AuthServer:    auth.URL,
	}
	if got != want {
		t.Errorf("RequestOutgoingPaymentGrant() = %+v, want %+v", got, want)
	}
	if len(got.ClientNonce) < 32 {
		t.Errorf("ClientNonce = %q, want at least 32 characters of random data", got.ClientNonce)
	}
}

// The finish URL of the caller can already carry a query of its own, and the grant
// id has to be added to it: concatenating by hand would produce a second "?" and
// the wallet would come back to a URI the callback cannot read.
func TestRequestOutgoingPaymentGrantKeepsAnExistingQueryOnTheFinishURL(t *testing.T) {
	keyPath, kid := newKey(t)

	var grantRequests []map[string]any
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the grant request body is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		grantRequests = append(grantRequests, body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, interactiveGrant(
			"https://wallet.example/grant-interactions/xyz",
			"server-nonce-1",
			"https://auth.example/continue/grant-1",
			"PDP_continue_1",
		))
	}))
	defer auth.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, walletAddressDocument(
			"https://"+r.Host+r.URL.Path,
			auth.URL,
			"https://"+r.Host+"/tenant",
		))
	}))
	defer resource.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	_, err = client.RequestOutgoingPaymentGrant(context.Background(), opclient.OutgoingPaymentGrantParams{
		ClientWalletAddress: httpsURL(resource) + "/laura-cajadebarrio",
		DebitLimit:          1000000,
		Interval:            "R/2026-10-16T00:00:00Z/P7D",
		FinishURL:           "https://tunel.example/op/finish?etapa=1",
		GrantID:             "gra_8f2a",
	})
	if err != nil {
		t.Fatalf("RequestOutgoingPaymentGrant() returned an error: %v", err)
	}

	if len(grantRequests) != 1 {
		t.Fatalf("the client auth server received %d grant requests, want 1", len(grantRequests))
	}
	interact, ok := grantRequests[0]["interact"].(map[string]any)
	if !ok {
		t.Fatalf("grant request has no interact block: %v", grantRequests[0])
	}
	finish, ok := interact["finish"].(map[string]any)
	if !ok {
		t.Fatalf("interact block has no finish: %v", interact)
	}
	if finish["uri"] != "https://tunel.example/op/finish?etapa=1&grant_id=gra_8f2a" {
		t.Errorf("finish uri = %v, want the caller's query kept and the grant id added to it", finish["uri"])
	}
}

// The interactive half needs an interact block: a grant that comes back
// without one stops the flow, naming the account, instead of returning a
// callback nobody can finish.
func TestRequestOutgoingPaymentGrantRejectsAGrantWithoutInteraction(t *testing.T) {
	keyPath, kid := newKey(t)

	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, nonInteractiveGrant("token-no-interactivo"))
	}))
	defer auth.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, walletAddressDocument(
			"https://"+r.Host+r.URL.Path,
			auth.URL,
			"https://"+r.Host+"/tenant",
		))
	}))
	defer resource.Close()

	clientWallet := httpsURL(resource) + "/laura-cajadebarrio"

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	_, err = client.RequestOutgoingPaymentGrant(context.Background(), opclient.OutgoingPaymentGrantParams{
		ClientWalletAddress: clientWallet,
		DebitLimit:          1000000,
		Interval:            "R/2026-10-16T00:00:00Z/P7D",
		FinishURL:           "https://tunel.example/op/finish",
		GrantID:             "gra_8f2a",
	})
	if err == nil {
		t.Fatal("RequestOutgoingPaymentGrant() accepted a non-interactive grant, want an error")
	}
	if !strings.Contains(err.Error(), clientWallet) {
		t.Errorf("error %q does not name the account %s", err, clientWallet)
	}
}

// The callback continues the grant with the interact_ref the wallet sent back
// (contract 8.3): the continuation carries that ref to the continue URI under
// the continuation token, and comes back with the approved token.
func TestContinueOutgoingPaymentGrant(t *testing.T) {
	keyPath, kid := newKey(t)

	var mu sync.Mutex
	var interactRef string
	var authorization string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/continue/") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the continuation body is not JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		interactRef, _ = body["interact_ref"].(string)
		authorization = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":{"access":[],"value":"PDP_aprobado_1","manage":"https://auth.example/token/abc","expires_in":600}}`)
	}))
	defer auth.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the continuation must not resolve the wallet address again: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer resource.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.ContinueOutgoingPaymentGrant(context.Background(), opclient.ContinueOutgoingPaymentGrantParams{
		ClientWalletAddress: httpsURL(resource) + "/laura-cajadebarrio",
		ContinueURI:         auth.URL + "/continue/grant-1",
		ContinueToken:       "PDP_continue_1",
		InteractRef:         "4f3c",
	})
	if err != nil {
		t.Fatalf("ContinueOutgoingPaymentGrant() returned an error: %v", err)
	}

	mu.Lock()
	ref, sent := interactRef, authorization
	mu.Unlock()
	if ref != "4f3c" {
		t.Errorf("continuation interact_ref = %q, want 4f3c", ref)
	}
	if sent != "GNAP PDP_continue_1" {
		t.Errorf("continuation authorization = %q, want the continuation token", sent)
	}

	want := opclient.AccessToken{Value: "PDP_aprobado_1", ManageURL: "https://auth.example/token/abc", ExpiresIn: 600}
	if got != want {
		t.Errorf("ContinueOutgoingPaymentGrant() = %+v, want %+v", got, want)
	}
}

// The hash of the callback (contract 8.3) binds the two nonces, the interact_ref
// and the authorization server URL; the expected value was computed by hand for
// this tuple, so the formula is pinned and not just re-derived.
func TestInteractiveHash(t *testing.T) {
	got := opclient.InteractiveHash("client-nonce", "server-nonce", "4f3c", "https://auth.example.com")
	// printf 'client-nonce\nserver-nonce\n4f3c\nhttps://auth.example.com' | \
	//   openssl dgst -sha256 -binary | openssl base64
	want := "zhNTMsPaclfUFaav77EheRiROt/MCwe3kcFQ0Gciz6A="
	if got != want {
		t.Errorf("InteractiveHash() = %q, want %q", got, want)
	}

	if !opclient.VerifyInteractiveHash("client-nonce", "server-nonce", "4f3c", "https://auth.example.com", want) {
		t.Error("VerifyInteractiveHash() rejected the hash it computed for the same tuple")
	}
	if opclient.VerifyInteractiveHash("client-nonce", "server-nonce", "4f3c", "https://auth.example.com", "dGFtYXJlZA==") {
		t.Error("VerifyInteractiveHash() accepted a tampered hash")
	}
	if opclient.VerifyInteractiveHash("otro-client-nonce", "server-nonce", "4f3c", "https://auth.example.com", want) {
		t.Error("VerifyInteractiveHash() accepted a hash computed over another client nonce")
	}
}

// Two nonces generated one after the other must differ: the callback hash is
// only as strong as the unguessability of the client nonce.
func TestNewNonce(t *testing.T) {
	first, err := opclient.NewNonce()
	if err != nil {
		t.Fatalf("NewNonce() returned an error: %v", err)
	}
	second, err := opclient.NewNonce()
	if err != nil {
		t.Fatalf("NewNonce() returned an error: %v", err)
	}
	if first == second {
		t.Errorf("NewNonce() returned the same value twice: %q", first)
	}
	if len(first) < 32 {
		t.Errorf("NewNonce() = %q, want at least 32 characters of random data", first)
	}
}

// Rotation (contract 8.7 step 2): the approved token is rotated around its
// management URL, and the new token arrives with its own management URL.
func TestRotateAccessToken(t *testing.T) {
	keyPath, kid := newKey(t)

	var mu sync.Mutex
	var authorization string
	manage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorization = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The rotation answers with the token wrapped in access_token, unlike the
		// continuation response, whose token is at the top level.
		fmt.Fprint(w, `{"access_token":{"access":[],"value":"PDP_rotado_1","manage":"https://auth.example/token/abc-2","expires_in":600}}`)
	}))
	defer manage.Close()

	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the rotation must not resolve the wallet address: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer resource.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.RotateAccessToken(context.Background(), manage.URL+"/token/abc-1", "PDP_aprobado_1")
	if err != nil {
		t.Fatalf("RotateAccessToken() returned an error: %v", err)
	}

	mu.Lock()
	sent := authorization
	mu.Unlock()
	if sent != "GNAP PDP_aprobado_1" {
		t.Errorf("rotation authorization = %q, want the token being rotated", sent)
	}

	want := opclient.AccessToken{Value: "PDP_rotado_1", ManageURL: "https://auth.example/token/abc-2", ExpiresIn: 600}
	if got != want {
		t.Errorf("RotateAccessToken() = %+v, want %+v", got, want)
	}
}

// outgoingPaymentResponse is what the resource server answers to a created
// outgoing payment, with the fields the SDK decodes.
func outgoingPaymentResponse(id, walletAddress, quoteID string) string {
	return fmt.Sprintf(`{"id":%q,"walletAddress":%q,"quoteId":%q,"debitAmount":{"assetCode":"COP","assetScale":2,"value":"20300000"},"receiveAmount":{"assetCode":"COP","assetScale":2,"value":"20300000"},"sentAmount":{"assetCode":"COP","assetScale":2,"value":"0"},"failed":false,"createdAt":"2026-10-17T15:10:00Z"}`,
		id, walletAddress, quoteID)
}

// The last half of the withdrawal (contract 8.7 step 2): the two outgoing
// payments that pay the two quotes, from the client wallet, under the approved
// token of the interactive grant. The token rotates after each use, so the
// second payment carries the rotated value and the caller stores the token as
// it ended, because the values that paid are dead.
func TestCreateOutgoingPayments(t *testing.T) {
	keyPath, kid := newKey(t)

	var mu sync.Mutex
	var payments []map[string]any
	var paymentAuthorizations []string
	var rotationAuthorizations []string
	var rotationPaths []string
	var rotations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/outgoing-payments"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("the outgoing payment body is not JSON: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			index := len(payments)
			payments = append(payments, body)
			paymentAuthorizations = append(paymentAuthorizations, r.Header.Get("Authorization"))
			mu.Unlock()

			quoteID, _ := body["quoteId"].(string)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, outgoingPaymentResponse(
				"https://"+r.Host+strings.TrimSuffix(r.URL.Path, "/outgoing-payments")+"/outgoing-payments/op-"+fmt.Sprint(index+1),
				"https://"+r.Host+strings.TrimSuffix(r.URL.Path, "/outgoing-payments"),
				quoteID,
			))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/token/"):
			mu.Lock()
			rotations++
			rotation := rotations
			rotationAuthorizations = append(rotationAuthorizations, r.Header.Get("Authorization"))
			rotationPaths = append(rotationPaths, r.URL.Path)
			mu.Unlock()

			value := "PDP_rotado_1"
			manage := "https://" + r.Host + "/token/abc-2"
			if rotation == 2 {
				value = "PDP_rotado_2"
				manage = "https://" + r.Host + "/token/abc-3"
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":{"access":[],"value":%q,"manage":%q,"expires_in":600}}`, value, manage)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	clientWallet := httpsURL(srv) + "/laura-cajadebarrio"
	storeQuote := clientWallet + "/quotes/q-tienda"
	platformQuote := clientWallet + "/quotes/q-plataforma"

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.CreateOutgoingPayments(context.Background(), opclient.CreateOutgoingPaymentsParams{
		ClientWalletAddress: clientWallet,
		StoreQuoteURL:       storeQuote,
		PlatformQuoteURL:    platformQuote,
		Token:               opclient.AccessToken{Value: "PDP_aprobado_1", ManageURL: httpsURL(srv) + "/token/abc-1", ExpiresIn: 600},
	})
	if err != nil {
		t.Fatalf("CreateOutgoingPayments() returned an error: %v", err)
	}

	// Two payments, in the order the contract runs them: the store first, then
	// the platform.
	if len(payments) != 2 {
		t.Fatalf("the resource server received %d outgoing payments, want 2", len(payments))
	}

	// The store payment carries exactly the wallet address and the quote id of
	// the store quote: the SDK's own TODO admits the quote id path is
	// under-tested, so the payload is asserted key by key.
	if len(payments[0]) != 2 {
		t.Errorf("store outgoing payment payload has %d keys (%v), want exactly walletAddress and quoteId", len(payments[0]), payments[0])
	}
	if payments[0]["walletAddress"] != clientWallet {
		t.Errorf("store outgoing payment walletAddress = %v, want %s", payments[0]["walletAddress"], clientWallet)
	}
	if payments[0]["quoteId"] != storeQuote {
		t.Errorf("store outgoing payment quoteId = %v, want %s", payments[0]["quoteId"], storeQuote)
	}
	if paymentAuthorizations[0] != "GNAP PDP_aprobado_1" {
		t.Errorf("store outgoing payment authorization = %q, want the approved grant token", paymentAuthorizations[0])
	}

	// The rotation of the token that just paid the store goes to the management
	// URL the grant returned.
	if len(rotationAuthorizations) != 2 {
		t.Fatalf("the auth server received %d rotations, want one after each payment", len(rotationAuthorizations))
	}
	if rotationAuthorizations[0] != "GNAP PDP_aprobado_1" {
		t.Errorf("first rotation authorization = %q, want the token that just paid the store", rotationAuthorizations[0])
	}

	// The platform payment carries the rotated token, not the original one.
	if len(payments[1]) != 2 {
		t.Errorf("platform outgoing payment payload has %d keys (%v), want exactly walletAddress and quoteId", len(payments[1]), payments[1])
	}
	if payments[1]["walletAddress"] != clientWallet {
		t.Errorf("platform outgoing payment walletAddress = %v, want %s", payments[1]["walletAddress"], clientWallet)
	}
	if payments[1]["quoteId"] != platformQuote {
		t.Errorf("platform outgoing payment quoteId = %v, want %s", payments[1]["quoteId"], platformQuote)
	}
	if paymentAuthorizations[1] != "GNAP PDP_rotado_1" {
		t.Errorf("platform outgoing payment authorization = %q, want the token returned by the rotation", paymentAuthorizations[1])
	}
	if rotationAuthorizations[1] != "GNAP PDP_rotado_1" {
		t.Errorf("second rotation authorization = %q, want the token that just paid the platform", rotationAuthorizations[1])
	}
	// The second rotation goes to the management URL the FIRST one returned, not to
	// the one the caller passed in: a regression that reused the original URL would
	// try to rotate a token that is already dead.
	if rotationPaths[1] != "/token/abc-2" {
		t.Errorf("second rotation path = %q, want the management URL the first rotation returned (/token/abc-2)", rotationPaths[1])
	}

	// Both payments are identified, and the token handed back is the one the
	// second rotation produced: every value that paid is dead.
	wantStore := opclient.OutgoingPayment{
		URL:           clientWallet + "/outgoing-payments/op-1",
		DebitAmount:   opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"},
		ReceiveAmount: opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"},
	}
	if got.Store != wantStore {
		t.Errorf("Store = %+v, want %+v", got.Store, wantStore)
	}
	wantPlatform := opclient.OutgoingPayment{
		URL:           clientWallet + "/outgoing-payments/op-2",
		DebitAmount:   opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"},
		ReceiveAmount: opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"},
	}
	if got.Platform != wantPlatform {
		t.Errorf("Platform = %+v, want %+v", got.Platform, wantPlatform)
	}
	wantToken := opclient.AccessToken{Value: "PDP_rotado_2", ManageURL: httpsURL(srv) + "/token/abc-3", ExpiresIn: 600}
	if got.Token != wantToken {
		t.Errorf("Token = %+v, want the token after the last rotation (%+v)", got.Token, wantToken)
	}
}

// A rotation that fails AFTER a payment was made must not hide the payment: the
// money moved, and a caller that only saw the error could not record which
// outgoing payment it has to account for.
func TestCreateOutgoingPaymentsKeepsThePaymentsAlreadyMadeWhenARotationFails(t *testing.T) {
	keyPath, kid := newKey(t)

	var payments, rotations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/outgoing-payments"):
			payments++
			wallet := "https://" + r.Host + strings.TrimSuffix(r.URL.Path, "/outgoing-payments")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, outgoingPaymentResponse(
				wallet+"/outgoing-payments/op-"+fmt.Sprint(payments),
				wallet,
				"https://"+r.Host+"/quotes/q-1",
			))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/token/"):
			rotations++
			if rotations == 2 {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":"la rotacion se cayo"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":{"access":[],"value":"PDP_rotado_1","manage":"https://%s/token/abc-2","expires_in":600}}`, r.Host)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	got, err := client.CreateOutgoingPayments(context.Background(), opclient.CreateOutgoingPaymentsParams{
		ClientWalletAddress: httpsURL(srv) + "/laura-cajadebarrio",
		StoreQuoteURL:       httpsURL(srv) + "/quotes/q-1",
		PlatformQuoteURL:    httpsURL(srv) + "/quotes/q-2",
		Token:               opclient.AccessToken{Value: "PDP_aprobado_1", ManageURL: httpsURL(srv) + "/token/abc-1", ExpiresIn: 600},
	})
	if err == nil {
		t.Fatal("CreateOutgoingPayments() reported success with a rotation that failed, want an error")
	}
	if !strings.Contains(got.Store.URL, "/outgoing-payments/op-") || !strings.Contains(got.Platform.URL, "/outgoing-payments/op-") {
		t.Errorf("the payments that were made were lost: got %+v, want both outgoing payment URLs so the caller can record them", got)
	}
	if got.Store.URL == got.Platform.URL {
		t.Errorf("both payments report the same URL %q, and they are different payments", got.Store.URL)
	}
}

// A payment the resource server reports as failed is an error, not a success:
// the money did not move, so the withdrawal cannot go on to the second payment,
// let alone report the withdrawal as paid.
func TestCreateOutgoingPaymentsReportsAFailedPaymentAsAnError(t *testing.T) {
	keyPath, kid := newKey(t)

	var mu sync.Mutex
	var payments, rotations int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/outgoing-payments"):
			mu.Lock()
			payments++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"https://rafiki-backend:3000/laura-cajadebarrio/outgoing-payments/op-1","walletAddress":"https://rafiki-backend:3000/laura-cajadebarrio","quoteId":"https://rafiki-backend:3000/laura-cajadebarrio/quotes/q-tienda","debitAmount":{"assetCode":"COP","assetScale":2,"value":"20300000"},"receiveAmount":{"assetCode":"COP","assetScale":2,"value":"20300000"},"sentAmount":{"assetCode":"COP","assetScale":2,"value":"0"},"failed":true,"createdAt":"2026-10-17T15:10:00Z"}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/token/"):
			mu.Lock()
			rotations++
			mu.Unlock()
			fmt.Fprint(w, `{"access_token":{"access":[],"value":"PDP_rotado_1","manage":"https://x/token/abc-2","expires_in":600}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	_, err = client.CreateOutgoingPayments(context.Background(), opclient.CreateOutgoingPaymentsParams{
		ClientWalletAddress: httpsURL(srv) + "/laura-cajadebarrio",
		StoreQuoteURL:       httpsURL(srv) + "/laura-cajadebarrio/quotes/q-tienda",
		PlatformQuoteURL:    httpsURL(srv) + "/laura-cajadebarrio/quotes/q-plataforma",
		Token:               opclient.AccessToken{Value: "PDP_aprobado_1", ManageURL: httpsURL(srv) + "/token/abc-1", ExpiresIn: 600},
	})
	if err == nil {
		t.Fatal("CreateOutgoingPayments() accepted a payment the server reports as failed, want an error")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error %q does not say the payment failed", err)
	}

	mu.Lock()
	created, rotated := payments, rotations
	mu.Unlock()
	if created != 1 {
		t.Errorf("the resource server received %d outgoing payments, want 1: a failed payment stops the flow", created)
	}
	if rotated != 0 {
		t.Errorf("the auth server received %d rotations after a failed payment, want 0", rotated)
	}
}

// Every parameter of the payment half is validated before any network call
// happens, and the error names the parameter that is wrong.
func TestCreateOutgoingPaymentsRejectsParamsItCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)

	// The stub fails the test if it is ever reached: validation happens before
	// the network, not at the first response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request went out before the parameters were validated: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	wallet := httpsURL(srv) + "/laura-cajadebarrio"
	token := opclient.AccessToken{Value: "PDP_aprobado_1", ManageURL: httpsURL(srv) + "/token/abc-1", ExpiresIn: 600}

	params := func(mutate func(*opclient.CreateOutgoingPaymentsParams)) opclient.CreateOutgoingPaymentsParams {
		p := opclient.CreateOutgoingPaymentsParams{
			ClientWalletAddress: wallet,
			StoreQuoteURL:       wallet + "/quotes/q-tienda",
			PlatformQuoteURL:    wallet + "/quotes/q-plataforma",
			Token:               token,
		}
		mutate(&p)
		return p
	}

	tests := []struct {
		name   string
		params opclient.CreateOutgoingPaymentsParams
		want   string
	}{
		{
			name:   "no client wallet address",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) { p.ClientWalletAddress = "" }),
			want:   "ClientWalletAddress",
		},
		{
			name: "client wallet address in the $ilp form",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) {
				p.ClientWalletAddress = "$ilp.interledger-test.dev/laura-cajadebarrio"
			}),
			want: "ClientWalletAddress",
		},
		{
			name:   "no store quote URL",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) { p.StoreQuoteURL = "" }),
			want:   "StoreQuoteURL",
		},
		{
			name:   "store quote URL without a host",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) { p.StoreQuoteURL = "/quotes/q-tienda" }),
			want:   "StoreQuoteURL",
		},
		{
			name:   "no platform quote URL",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) { p.PlatformQuoteURL = "" }),
			want:   "PlatformQuoteURL",
		},
		{
			name:   "no token value",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) { p.Token.Value = "" }),
			want:   "Token.Value",
		},
		{
			name:   "no token manage URL",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) { p.Token.ManageURL = "" }),
			want:   "Token.ManageURL",
		},
		{
			name: "token manage URL that is not a token URL",
			params: params(func(p *opclient.CreateOutgoingPaymentsParams) {
				p.Token.ManageURL = "https://auth.example/continue/grant-1"
			}),
			want: "Token.ManageURL",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.CreateOutgoingPayments(context.Background(), test.params)
			if err == nil {
				t.Fatalf("CreateOutgoingPayments(%+v) accepted params it cannot use, want an error", test.params)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}

// The status of a collection point (contract 8.8) reads the SIGNED endpoint:
// the public document of an incoming payment does not carry the completion, so
// a route built on it could hand out money on an unpaid withdrawal.
func TestIncomingPaymentStatus(t *testing.T) {
	keyPath, kid := newKey(t)

	tests := []struct {
		name       string
		body       string
		wantStatus opclient.IncomingPaymentStatus
	}{
		{
			name:       "a complete collection point",
			body:       `{"id":"https://rafiki-backend:3000/tienda-cajadebarrio/incoming-payments/ip-1","completed":true,"receivedAmount":{"assetCode":"COP","assetScale":2,"value":"20300000"},"methods":[]}`,
			wantStatus: opclient.IncomingPaymentStatus{Completed: true, Received: opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"}},
		},
		{
			name:       "a collection point still receiving",
			body:       `{"id":"https://rafiki-backend:3000/tienda-cajadebarrio/incoming-payments/ip-1","completed":false,"receivedAmount":{"assetCode":"COP","assetScale":2,"value":"5000000"},"methods":[]}`,
			wantStatus: opclient.IncomingPaymentStatus{Completed: false, Received: opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "5000000"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var authorization string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/incoming-payments/ip-1") {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				mu.Lock()
				authorization = r.Header.Get("Authorization")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, test.body)
			}))
			defer srv.Close()

			client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
			if err != nil {
				t.Fatalf("New() returned an error: %v", err)
			}

			got, err := client.IncomingPaymentStatus(context.Background(), httpsURL(srv)+"/incoming-payments/ip-1", "token-tienda")
			if err != nil {
				t.Fatalf("IncomingPaymentStatus() returned an error: %v", err)
			}

			mu.Lock()
			sent := authorization
			mu.Unlock()
			if sent != "GNAP token-tienda" {
				t.Errorf("status authorization = %q, want the signed read with the access token", sent)
			}
			if got != test.wantStatus {
				t.Errorf("IncomingPaymentStatus() = %+v, want %+v", got, test.wantStatus)
			}
		})
	}
}

// The status read needs the URL it reads and the token it reads it with.
func TestIncomingPaymentStatusRejectsParamsItCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)

	// The stub fails the test if it is ever reached: validation happens before
	// the network, not at the first response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request went out before the parameters were validated: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	url := httpsURL(srv) + "/incoming-payments/ip-1"

	tests := []struct {
		name               string
		incomingPaymentURL string
		accessToken        string
		want               string
	}{
		{
			name:               "no incoming payment URL",
			incomingPaymentURL: "",
			accessToken:        "token-tienda",
			want:               "incomingPaymentURL",
		},
		{
			name:               "no access token",
			incomingPaymentURL: url,
			accessToken:        "",
			want:               "accessToken",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.IncomingPaymentStatus(context.Background(), test.incomingPaymentURL, test.accessToken)
			if err == nil {
				t.Fatal("IncomingPaymentStatus() accepted params it cannot use, want an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}

// The wait for the money to land (contract 8.7 step 3): poll the signed status
// until the incoming payment is complete or the budget runs out.
func TestWaitForIncomingPayment(t *testing.T) {
	keyPath, kid := newKey(t)

	newClient := func(t *testing.T, srv *httptest.Server) *opclient.Client {
		t.Helper()
		client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
		if err != nil {
			t.Fatalf("New() returned an error: %v", err)
		}
		return client
	}

	pendingBody := `{"id":"https://rafiki-backend:3000/tienda-cajadebarrio/incoming-payments/ip-1","completed":false,"receivedAmount":{"assetCode":"COP","assetScale":2,"value":"5000000"},"methods":[]}`
	completeBody := `{"id":"https://rafiki-backend:3000/tienda-cajadebarrio/incoming-payments/ip-1","completed":true,"receivedAmount":{"assetCode":"COP","assetScale":2,"value":"20300000"},"methods":[]}`

	t.Run("returns as soon as the payment completes", func(t *testing.T) {
		var mu sync.Mutex
		var polls int
		var authorizations []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			index := polls
			polls++
			authorizations = append(authorizations, r.Header.Get("Authorization"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if index == 0 {
				fmt.Fprint(w, pendingBody)
				return
			}
			fmt.Fprint(w, completeBody)
		}))
		defer srv.Close()

		client := newClient(t, srv)
		started := time.Now()
		got, err := client.WaitForIncomingPayment(context.Background(), opclient.WaitForIncomingPaymentParams{
			IncomingPaymentURL: httpsURL(srv) + "/incoming-payments/ip-1",
			AccessToken:        "token-tienda",
			Timeout:            5 * time.Second,
			Interval:           10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("WaitForIncomingPayment() returned an error: %v", err)
		}
		want := opclient.IncomingPaymentStatus{Completed: true, Received: opclient.Amount{AssetCode: "COP", AssetScale: 2, Value: "20300000"}}
		if got != want {
			t.Errorf("WaitForIncomingPayment() = %+v, want %+v", got, want)
		}
		mu.Lock()
		pollCount, sent := polls, authorizations
		mu.Unlock()
		if pollCount != 2 {
			t.Errorf("the resource server received %d status reads, want 2 (pending, then complete)", pollCount)
		}
		for i, authorization := range sent {
			if authorization != "GNAP token-tienda" {
				t.Errorf("status read %d authorization = %q, want the signed read", i, authorization)
			}
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("the wait took %s after the payment completed", elapsed)
		}
	})

	t.Run("an already complete payment returns without waiting", func(t *testing.T) {
		var mu sync.Mutex
		var polls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			polls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, completeBody)
		}))
		defer srv.Close()

		client := newClient(t, srv)
		got, err := client.WaitForIncomingPayment(context.Background(), opclient.WaitForIncomingPaymentParams{
			IncomingPaymentURL: httpsURL(srv) + "/incoming-payments/ip-1",
			AccessToken:        "token-tienda",
			Timeout:            5 * time.Second,
			Interval:           10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("WaitForIncomingPayment() returned an error: %v", err)
		}
		if !got.Completed {
			t.Errorf("WaitForIncomingPayment() = %+v, want a completed status", got)
		}
		mu.Lock()
		pollCount := polls
		mu.Unlock()
		if pollCount != 1 {
			t.Errorf("the resource server received %d status reads, want 1", pollCount)
		}
	})

	// Invariant 1: no money leaves before the payment is completed. A caller
	// that cannot prove completion must not proceed, so the timeout is an
	// error, never a success.
	t.Run("a budget that runs out without completion is an error", func(t *testing.T) {
		var mu sync.Mutex
		var polls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			polls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, pendingBody)
		}))
		defer srv.Close()

		client := newClient(t, srv)
		url := httpsURL(srv) + "/incoming-payments/ip-1"
		status, err := client.WaitForIncomingPayment(context.Background(), opclient.WaitForIncomingPaymentParams{
			IncomingPaymentURL: url,
			AccessToken:        "token-tienda",
			Timeout:            120 * time.Millisecond,
			Interval:           10 * time.Millisecond,
		})
		if err == nil {
			t.Fatal("WaitForIncomingPayment() reported success without proof of completion, want an error")
		}
		// The status itself must not say completed: a caller that ignored the error and
		// looked only at the value must not find an authorization to deliver.
		if status.Completed {
			t.Error("WaitForIncomingPayment() answered Completed = true on a timeout, and nothing completed")
		}
		if !strings.Contains(err.Error(), url) {
			t.Errorf("error %q does not name the incoming payment %s", err, url)
		}
		mu.Lock()
		pollCount := polls
		mu.Unlock()
		if pollCount < 2 {
			t.Errorf("the resource server received %d status reads, want the budget to be spent polling", pollCount)
		}
	})

	t.Run("a cancelled context stops the polling", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, pendingBody)
		}))
		defer srv.Close()

		client := newClient(t, srv)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			_, err := client.WaitForIncomingPayment(ctx, opclient.WaitForIncomingPaymentParams{
				IncomingPaymentURL: httpsURL(srv) + "/incoming-payments/ip-1",
				AccessToken:        "token-tienda",
				Timeout:            30 * time.Second,
				Interval:           10 * time.Millisecond,
			})
			done <- err
		}()

		time.Sleep(50 * time.Millisecond)
		cancel()

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("WaitForIncomingPayment() returned %v, want a context cancellation", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the wait kept polling after the context was cancelled")
		}
	})

	// The doc comment says a zero Interval means one second: the behavior shows
	// the pause between the polls, measured on a payment that completes on the
	// second read.
	t.Run("a zero interval waits one second between polls", func(t *testing.T) {
		var mu sync.Mutex
		var polls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			index := polls
			polls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if index == 0 {
				fmt.Fprint(w, pendingBody)
				return
			}
			fmt.Fprint(w, completeBody)
		}))
		defer srv.Close()

		client := newClient(t, srv)
		started := time.Now()
		got, err := client.WaitForIncomingPayment(context.Background(), opclient.WaitForIncomingPaymentParams{
			IncomingPaymentURL: httpsURL(srv) + "/incoming-payments/ip-1",
			AccessToken:        "token-tienda",
			Timeout:            5 * time.Second,
		})
		if err != nil {
			t.Fatalf("WaitForIncomingPayment() returned an error: %v", err)
		}
		if !got.Completed {
			t.Errorf("WaitForIncomingPayment() = %+v, want a completed status", got)
		}
		if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
			t.Errorf("the wait between the two polls took %s, want the one second default", elapsed)
		}
	})
}

// The wait needs the URL it polls and the token it polls with; a negative
// budget or interval is a value it cannot use either.
func TestWaitForIncomingPaymentRejectsParamsItCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)

	// The stub fails the test if it is ever reached: validation happens before
	// the network, not at the first response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request went out before the parameters were validated: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	url := httpsURL(srv) + "/incoming-payments/ip-1"

	params := func(mutate func(*opclient.WaitForIncomingPaymentParams)) opclient.WaitForIncomingPaymentParams {
		p := opclient.WaitForIncomingPaymentParams{
			IncomingPaymentURL: url,
			AccessToken:        "token-tienda",
			Timeout:            15 * time.Second,
			Interval:           time.Second,
		}
		mutate(&p)
		return p
	}

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "no incoming payment URL",
			call: func() error {
				_, err := client.WaitForIncomingPayment(context.Background(), params(func(p *opclient.WaitForIncomingPaymentParams) { p.IncomingPaymentURL = "" }))
				return err
			},
			want: "IncomingPaymentURL",
		},
		{
			name: "no access token",
			call: func() error {
				_, err := client.WaitForIncomingPayment(context.Background(), params(func(p *opclient.WaitForIncomingPaymentParams) { p.AccessToken = "" }))
				return err
			},
			want: "AccessToken",
		},
		{
			name: "negative timeout",
			call: func() error {
				_, err := client.WaitForIncomingPayment(context.Background(), params(func(p *opclient.WaitForIncomingPaymentParams) { p.Timeout = -time.Second }))
				return err
			},
			want: "Timeout",
		},
		{
			name: "negative interval",
			call: func() error {
				_, err := client.WaitForIncomingPayment(context.Background(), params(func(p *opclient.WaitForIncomingPaymentParams) { p.Interval = -time.Second }))
				return err
			},
			want: "Interval",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("the call accepted params it cannot use, want an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}

// Every parameter of the interactive half is validated before any network call
// happens, and the error names the parameter that is wrong.
func TestInteractiveGrantsRejectParamsTheyCannotUse(t *testing.T) {
	keyPath, kid := newKey(t)

	// The stub fails the test if it is ever reached: validation happens before
	// the network, not at the first response.
	resource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request went out before the parameters were validated: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer resource.Close()

	client, err := opclient.New(opclient.Config{WalletAddressURL: httpsURL(resource) + "/app-cajadebarrio", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	wallet := httpsURL(resource) + "/laura-cajadebarrio"
	continueURI := "https://auth.example/continue/grant-1"

	grantParams := func(mutate func(*opclient.OutgoingPaymentGrantParams)) opclient.OutgoingPaymentGrantParams {
		params := opclient.OutgoingPaymentGrantParams{
			ClientWalletAddress: wallet,
			DebitLimit:          1000000,
			Interval:            "R/2026-10-16T00:00:00Z/P7D",
			FinishURL:           "https://tunel.example/op/finish",
			GrantID:             "gra_8f2a",
		}
		mutate(&params)
		return params
	}
	continueParams := func(mutate func(*opclient.ContinueOutgoingPaymentGrantParams)) opclient.ContinueOutgoingPaymentGrantParams {
		params := opclient.ContinueOutgoingPaymentGrantParams{
			ClientWalletAddress: wallet,
			ContinueURI:         continueURI,
			ContinueToken:       "PDP_continue_1",
			InteractRef:         "4f3c",
		}
		mutate(&params)
		return params
	}

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "grant: no client wallet address",
			call: func() error {
				_, err := client.RequestOutgoingPaymentGrant(context.Background(), grantParams(func(p *opclient.OutgoingPaymentGrantParams) { p.ClientWalletAddress = "" }))
				return err
			},
			want: "ClientWalletAddress",
		},
		{
			name: "grant: zero debit limit",
			call: func() error {
				_, err := client.RequestOutgoingPaymentGrant(context.Background(), grantParams(func(p *opclient.OutgoingPaymentGrantParams) { p.DebitLimit = 0 }))
				return err
			},
			want: "DebitLimit",
		},
		{
			name: "grant: no interval",
			call: func() error {
				_, err := client.RequestOutgoingPaymentGrant(context.Background(), grantParams(func(p *opclient.OutgoingPaymentGrantParams) { p.Interval = "" }))
				return err
			},
			want: "Interval",
		},
		{
			name: "grant: finish URL without a host",
			call: func() error {
				_, err := client.RequestOutgoingPaymentGrant(context.Background(), grantParams(func(p *opclient.OutgoingPaymentGrantParams) { p.FinishURL = "/op/finish" }))
				return err
			},
			want: "FinishURL",
		},
		{
			name: "grant: grant id that cannot travel inside the finish URI",
			call: func() error {
				_, err := client.RequestOutgoingPaymentGrant(context.Background(), grantParams(func(p *opclient.OutgoingPaymentGrantParams) { p.GrantID = "gra 8f2a&x=1" }))
				return err
			},
			want: "GrantID",
		},
		{
			name: "grant: no grant id",
			call: func() error {
				_, err := client.RequestOutgoingPaymentGrant(context.Background(), grantParams(func(p *opclient.OutgoingPaymentGrantParams) { p.GrantID = "" }))
				return err
			},
			want: "GrantID",
		},
		{
			name: "continue: no client wallet address",
			call: func() error {
				_, err := client.ContinueOutgoingPaymentGrant(context.Background(), continueParams(func(p *opclient.ContinueOutgoingPaymentGrantParams) { p.ClientWalletAddress = "" }))
				return err
			},
			want: "ClientWalletAddress",
		},
		{
			name: "continue: URI that is not a continue URI",
			call: func() error {
				_, err := client.ContinueOutgoingPaymentGrant(context.Background(), continueParams(func(p *opclient.ContinueOutgoingPaymentGrantParams) { p.ContinueURI = "https://auth.example/token/abc" }))
				return err
			},
			want: "ContinueURI",
		},
		{
			name: "continue: no continuation token",
			call: func() error {
				_, err := client.ContinueOutgoingPaymentGrant(context.Background(), continueParams(func(p *opclient.ContinueOutgoingPaymentGrantParams) { p.ContinueToken = "" }))
				return err
			},
			want: "ContinueToken",
		},
		{
			name: "continue: no interact ref",
			call: func() error {
				_, err := client.ContinueOutgoingPaymentGrant(context.Background(), continueParams(func(p *opclient.ContinueOutgoingPaymentGrantParams) { p.InteractRef = "" }))
				return err
			},
			want: "InteractRef",
		},
		{
			name: "rotate: no manage URL",
			call: func() error {
				_, err := client.RotateAccessToken(context.Background(), "", "PDP_aprobado_1")
				return err
			},
			want: "manageURL",
		},
		{
			name: "rotate: URL that is not a token URL",
			call: func() error {
				_, err := client.RotateAccessToken(context.Background(), "https://auth.example/continue/grant-1", "PDP_aprobado_1")
				return err
			},
			want: "manageURL",
		},
		{
			name: "rotate: no access token",
			call: func() error {
				_, err := client.RotateAccessToken(context.Background(), "https://auth.example/token/abc", "")
				return err
			},
			want: "accessToken",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("the call accepted params it cannot use, want an error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("error %q does not name %s", err, test.want)
			}
		})
	}
}
