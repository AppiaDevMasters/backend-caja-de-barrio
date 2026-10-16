package main

import (
	"strings"
	"testing"
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
