package opclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
