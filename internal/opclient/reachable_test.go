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

// The authorization server publishes its continuation and its token management URLs with the host
// of the demo, which a container cannot reach. The client rewrites that host BEFORE it signs,
// because the signature covers the target URI: rewriting it on the way out would produce a
// signature the server cannot verify, and not rewriting it at all gives an i/o timeout (the eighth
// defect the live withdrawal found).
//
// The redirect is deliberately NOT rewritten: the user's browser opens it, and it needs the host
// the stack published.
func TestTheClientMakesTheContinuationReachableAndLeavesTheRedirectAlone(t *testing.T) {
	keyPath, kid := newKey(t)

	const hostPublicado = "auth.publico:3006"

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet {
			fmt.Fprint(w, walletAddressDocument(
				"https://"+r.Host+r.URL.Path,
				"http://"+r.Host,
				"http://"+r.Host,
			))

			return
		}

		// The grant hands back its URLs with the PUBLISHED host, like Rafiki does.
		fmt.Fprintf(w, `{"interact":{"redirect":"http://%s/interact/i1/n1","finish":"server-nonce"},"continue":{"uri":"http://%s/continue/c1","access_token":{"value":"PDP_1"}}}`,
			hostPublicado, hostPublicado)
	}))
	defer stub.Close()

	interna := strings.TrimPrefix(stub.URL, "http://")
	client, err := opclient.New(opclient.Config{
		WalletAddressURL: httpsURL(stub) + "/app-cajadebarrio",
		PrivateKeyPath:   keyPath,
		KeyID:            kid,
		HostRewrites:     map[string]string{hostPublicado: interna},
	})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	grant, err := client.RequestOutgoingPaymentGrant(context.Background(), opclient.OutgoingPaymentGrantParams{
		ClientWalletAddress: httpsURL(stub) + "/laura-cajadebarrio",
		DebitLimit:          11_100,
		Interval:            "R/2026-10-17T00:00:00Z/P7D",
		FinishURL:           "http://" + interna + "/op/finish",
		GrantID:             "gra_test",
	})
	if err != nil {
		t.Fatalf("RequestOutgoingPaymentGrant() returned an error: %v", err)
	}

	if !strings.Contains(grant.ContinueURI, interna) || strings.Contains(grant.ContinueURI, hostPublicado) {
		t.Errorf("ContinueURI = %q, want the internal host %s: la continuación se llama desde adentro de la red", grant.ContinueURI, interna)
	}
	if want := "http://" + hostPublicado + "/interact/i1/n1"; grant.RedirectURL != want {
		t.Errorf("RedirectURL = %q, want %q: el redirect lo abre el navegador del usuario, y ese no resuelve nombres de contenedor", grant.RedirectURL, want)
	}
}
