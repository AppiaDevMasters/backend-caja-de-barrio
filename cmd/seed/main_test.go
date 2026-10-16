package main

import (
	"strings"
	"testing"
)

func TestWalletAddressURL(t *testing.T) {
	tests := map[string]struct {
		base    string
		address string
		want    string
	}{
		"https base without trailing slash": {"https://100.76.141.47:3000", "tienda-cajadebarrio", "https://100.76.141.47:3000/tienda-cajadebarrio"},
		"https base with trailing slash":    {"https://100.76.141.47:3000/", "tienda-cajadebarrio", "https://100.76.141.47:3000/tienda-cajadebarrio"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := walletAddressURL(tc.base, tc.address)
			if err != nil {
				t.Fatalf("walletAddressURL(%q, %q) returned an error: %v", tc.base, tc.address, err)
			}
			if got != tc.want {
				t.Errorf("walletAddressURL(%q, %q) = %q, want %q", tc.base, tc.address, got, tc.want)
			}
		})
	}
}

// Rafiki refuses any wallet address URL that is not https, so the seed reports
// the variable to fix instead of letting the Admin API answer "invalid url".
func TestWalletAddressURLRejectsABaseRafikiCannotAccept(t *testing.T) {
	tests := map[string]string{
		"http base": "http://100.76.141.47:3000",
		"no scheme": "100.76.141.47:3000",
		"empty":     "",
	}

	for name, base := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := walletAddressURL(base, "tienda-cajadebarrio")
			if err == nil {
				t.Fatalf("walletAddressURL(%q, ...) accepted a base Rafiki rejects, want an error", base)
			}
			if !strings.Contains(err.Error(), "RAFIKI_OPEN_PAYMENTS_URL") {
				t.Errorf("error %q does not name RAFIKI_OPEN_PAYMENTS_URL", err)
			}
		})
	}
}
