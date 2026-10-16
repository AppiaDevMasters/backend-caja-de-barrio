package ase_test

import (
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/ase"
)

// A missing secret has to fail where the server is built, naming the environment
// variable, instead of leaving a process that refuses every request at runtime.
func TestNewRejectsAnIncompleteConfiguration(t *testing.T) {
	tests := map[string]struct {
		cfg  ase.Config
		want string
	}{
		"sin secreto de webhooks": {
			cfg:  ase.Config{IdentityServerSecret: testIdPSecret},
			want: "RAFIKI_SIGNATURE_SECRET",
		},
		"sin secreto del IdP": {
			cfg:  ase.Config{SignatureSecret: testSecret},
			want: "RAFIKI_IDENTITY_SERVER_SECRET",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv, err := ase.New(tc.cfg)
			if err == nil {
				t.Fatalf("New() accepted an incomplete configuration and returned %v", srv)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

func TestNewAcceptsACompleteConfiguration(t *testing.T) {
	srv, err := ase.New(ase.Config{SignatureSecret: testSecret, IdentityServerSecret: testIdPSecret})
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}
	if srv == nil || srv.Handler() == nil {
		t.Fatal("New() returned a server without a router")
	}
}
