package opclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
