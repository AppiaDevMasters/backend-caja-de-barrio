// Command seed provisions the local ASE with what the withdrawal flow needs: the
// COP asset and one wallet address per actor of the demo. It is idempotent, so it
// can run again after every stack restart.
//
// It reads the same RAFIKI_* variables as the backend:
//
//	RAFIKI_ADMIN_URL         http://localhost:3001/graphql
//	RAFIKI_OPEN_PAYMENTS_URL http://localhost:3000
//	RAFIKI_TENANT_ID         the OPERATOR_TENANT_ID of rafiki/.env
//	RAFIKI_ADMIN_API_SECRET  the ADMIN_API_SECRET of rafiki/.env
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

// The COP asset of the demo: pesos with two decimals, which is the asset scale
// Open Payments uses for Colombian pesos.
const (
	assetCode  = "COP"
	assetScale = 2
)

// actor is one wallet address the demo needs: both sides of a withdrawal and the
// application identity that signs the Open Payments calls.
type actor struct {
	address    string
	publicName string
}

var actors = []actor{
	{address: "tienda-cajadebarrio", publicName: "Don Jaime - Tienda"},
	{address: "laura-cajadebarrio", publicName: "Laura"},
	{address: "app-cajadebarrio", publicName: "Caja de Barrio"},
}

func main() {
	if err := run(); err != nil {
		slog.Error("seed stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	client, err := rafikiadmin.New(rafikiadmin.Config{
		URL:       cfg.Rafiki.AdminURL,
		TenantID:  cfg.Rafiki.TenantID,
		APISecret: cfg.Rafiki.AdminAPISecret,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Rafiki refuses every wallet address URL that is not https, so the base has
	// to be settled before anything is created.
	base, err := requireHTTPSBase(cfg.Rafiki.OpenPaymentsURL)
	if err != nil {
		return err
	}

	asset, err := client.EnsureAsset(ctx, assetCode, assetScale)
	if err != nil {
		return fmt.Errorf("ensuring the %s asset: %w", assetCode, err)
	}
	fmt.Printf("asset %s scale %d id %s\n", asset.Code, asset.Scale, asset.ID)

	// The tenant setting is what makes Rafiki compose and accept wallet address
	// URLs in https while the stack itself keeps publishing plain http.
	if err := client.EnsureTenantSetting(ctx, rafikiadmin.SettingWalletAddressURL, base); err != nil {
		return fmt.Errorf("ensuring the tenant setting %s: %w", rafikiadmin.SettingWalletAddressURL, err)
	}
	fmt.Printf("tenant setting %s = %s\n", rafikiadmin.SettingWalletAddressURL, base)

	for _, a := range actors {
		addressURL := walletAddressURL(base, a.address)

		address, err := client.EnsureWalletAddress(ctx, rafikiadmin.CreateWalletAddressInput{
			AssetID:        asset.ID,
			Address:        addressURL,
			PublicName:     a.publicName,
			IdempotencyKey: "seed:" + a.address,
		})
		if err != nil {
			return fmt.Errorf("ensuring the wallet address %s: %w", addressURL, err)
		}
		fmt.Printf("wallet address %s id %s %s\n", addressURL, address.ID, address.PublicName)
	}

	return nil
}

// requireHTTPSBase validates the public base URL the ASE publishes for its wallet
// addresses and returns it without a trailing slash.
//
// Rafiki accepts a bare name and composes it over this base, but it refuses any
// wallet address URL whose scheme is not https. Composing it here reports the
// variable to fix instead of letting the Admin API answer a bare "invalid url".
func requireHTTPSBase(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("RAFIKI_OPEN_PAYMENTS_URL=%q is not a URL: %w", base, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("RAFIKI_OPEN_PAYMENTS_URL=%q must use https: Rafiki refuses a wallet address URL that is not https", base)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("RAFIKI_OPEN_PAYMENTS_URL=%q has no host", base)
	}

	return strings.TrimSuffix(base, "/"), nil
}

// walletAddressURL is the public URL of one wallet address of the ASE.
func walletAddressURL(base, address string) string {
	return base + "/" + address
}
