package opclient_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
)

// peticion is one call the client made, recorded by the stub below.
type peticion struct {
	metodo string
	ruta   string
}

// The flow builds its requests on the servers the wallet address document announces: the
// AUTHORIZATION server for the grants and the RESOURCE server for the payments.
//
// They are the same URL on the testnet, and they are NOT on this ASE: it publishes its
// wallet addresses in https, which is the only scheme Rafiki accepts, while its resource
// server answers on http. Building the calls on the wallet address made every signed request
// go to a URL the signature did not cover, because the signature binds the target URI, and
// the first live withdrawal got a 401 "provided signature is invalid".
func TestTheFlowCallsTheServersOfTheDocument(t *testing.T) {
	keyPath, kid := newKey(t)

	var recibidas []peticion

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recibidas = append(recibidas, peticion{metodo: r.Method, ruta: r.URL.Path})

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet:
			// The document names its servers somewhere else than its own address.
			fmt.Fprint(w, walletAddressDocument(
				"https://"+r.Host+r.URL.Path,
				"http://"+r.Host+"/acceso",
				"http://"+r.Host+"/recurso",
			))
		case r.URL.Path == "/acceso":
			fmt.Fprintf(w, `{"access_token":{"access":[],"value":"PDP_1","manage":"http://%s/token/1","expires_in":600}}`, r.Host)
		case strings.HasSuffix(r.URL.Path, "/quotes"):
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"http://%s%s/q-1","debitAmount":{"assetCode":"COP","assetScale":2,"value":"11000"},"receiveAmount":{"assetCode":"COP","assetScale":2,"value":"11000"},"method":"ilp","expiresAt":"2026-10-17T00:00:00Z"}`, r.Host, r.URL.Path)
		default:
			t.Errorf("peticion inesperada %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client, err := opclient.New(opclient.Config{
		WalletAddressURL: httpsURL(srv) + "/app-cajadebarrio",
		PrivateKeyPath:   keyPath,
		KeyID:            kid,
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	if _, err := client.CreateQuotes(context.Background(), opclient.CreateQuotesParams{
		ClientWalletAddress: httpsURL(srv) + "/laura-cajadebarrio",
		StoreReceiver:       "https://x/incoming-payments/ip-1",
		StoreAmountCOP:      11_000,
		PlatformReceiver:    "https://x/incoming-payments/ip-2",
		PlatformAmountCOP:   100,
	}); err != nil {
		t.Fatalf("CreateQuotes() returned an error: %v", err)
	}

	if !llegoAlguna(recibidas, http.MethodPost, "/acceso") {
		t.Error("el grant no fue al servidor de autorizacion que anuncia el documento")
	}
	if !llegoAlguna(recibidas, http.MethodPost, "/recurso/quotes") {
		t.Error("las cotizaciones no fueron al resource server que anuncia el documento")
	}
	// And no CALL goes to the wallet address URL: reading the document is done there, because
	// that address IS the document, but building a call on it is what broke the signature.
	for _, p := range recibidas {
		if p.metodo != http.MethodGet && strings.Contains(p.ruta, "cajadebarrio") {
			t.Errorf("una llamada fue a la URL de la wallet address (%s %s) en vez de al servidor del documento", p.metodo, p.ruta)
		}
	}
}

func llegoAlguna(recibidas []peticion, metodo, ruta string) bool {
	for _, p := range recibidas {
		if p.metodo == metodo && p.ruta == ruta {
			return true
		}
	}

	return false
}
