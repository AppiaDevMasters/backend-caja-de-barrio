package opclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
)

// The testnet quote names only the receiver: the incoming payment already says how
// many pesos the store gets, and the ASE prices the debit in the client's dollars.
// TestCreateQuotes keeps guarding that the own-ASE quotes still carry their debit.
func TestCreateQuoteByReceiverSendsNoAmountAndReadsBothSides(t *testing.T) {
	keyPath, kid := newKey(t)
	clientAuth, _ := newAuthServer(t, "token-cotizacion")

	var (
		mu    sync.Mutex
		quote map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/andres-cajadebarrio":
			w.Header().Set("Content-Type", "application/json")
			doc := walletAddressDocument("https://"+r.Host+r.URL.Path, clientAuth.URL, "https://"+r.Host+r.URL.Path)
			fmt.Fprint(w, strings.Replace(doc, `"COP"`, `"USD"`, 1))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/quotes"):
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&quote)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":%q,"method":"ilp","walletAddress":%q,"receiver":%q,`+
				`"debitAmount":{"assetCode":"USD","assetScale":2,"value":"5214"},`+
				`"receiveAmount":{"assetCode":"COP","assetScale":2,"value":"20360000"},"expiresAt":"2026-10-17T15:10:00Z"}`,
				"https://"+r.Host+r.URL.Path+"/q1", "https://"+r.Host+"/andres-cajadebarrio", quote["receiver"])
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, walletAddressDocument("https://"+r.Host+r.URL.Path, "https://"+r.Host+r.URL.Path, "https://"+r.Host+r.URL.Path))
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
	receiver := "https://ilp.interledger-test.dev/laura-cajadebarrio/incoming-payments/ip-1"
	got, err := client.CreateQuoteByReceiver(context.Background(), httpsURL(srv)+"/andres-cajadebarrio", receiver)
	if err != nil {
		t.Fatalf("CreateQuoteByReceiver() returned an error: %v", err)
	}

	if _, conDebito := quote["debitAmount"]; conDebito {
		t.Errorf("the quote by receiver must not fix a debit: %v", quote)
	}
	if _, conRecibo := quote["receiveAmount"]; conRecibo {
		t.Errorf("the quote by receiver must not fix a receive amount: %v", quote)
	}
	if quote["receiver"] != receiver || quote["method"] != "ilp" {
		t.Errorf("quote = %v", quote)
	}
	if got.DebitAmount.AssetCode != "USD" || got.DebitAmount.Value != "5214" ||
		got.ReceiveAmount.AssetCode != "COP" || got.ReceiveAmount.Value != "20360000" || got.ReceiveAmount.AssetScale != 2 {
		t.Errorf("both sides of the cross-currency quote must come back with their asset: %+v", got)
	}
}

func TestLasOperacionesDeUnCobroRechazanLoQueNoSirve(t *testing.T) {
	keyPath, kid := newKey(t)
	client, err := opclient.New(opclient.Config{WalletAddressURL: "https://ase.example/app", PrivateKeyPath: keyPath, KeyID: kid})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := client.CreateIncomingPayment(ctx, "", 1000); err == nil {
		t.Error("an incoming payment without a wallet must fail before calling anyone")
	}
	if _, err := client.CreateIncomingPayment(ctx, "https://ase.example/tienda", 0); err == nil {
		t.Error("an incoming payment of zero must fail")
	}
	if _, err := client.CreateQuoteByReceiver(ctx, "https://ase.example/cliente", ""); err == nil {
		t.Error("a quote without a receiver must fail")
	}
	if _, _, err := client.CreateOutgoingPayment(ctx, "https://ase.example/cliente", "https://ase.example/q", opclient.AccessToken{}); err == nil {
		t.Error("a payment without a token must fail")
	}
}
