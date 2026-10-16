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
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opkeys"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

// The COP asset of the demo: pesos with two decimals, which is the asset scale
// Open Payments uses for Colombian pesos.
const (
	assetCode  = "COP"
	assetScale = 2
)

// assetLiquidityTarget is how much the ASE is funded with for the demo, in the
// smallest unit of the asset: with scale 2 this is 10.000.000 COP.
const assetLiquidityTarget rafikiadmin.UInt64 = 1_000_000_000

// appAddress is the wallet address this backend acts as: the identity whose key
// signs its Open Payments requests.
const appAddress = "app-cajadebarrio"

// actor is one wallet address the demo needs: both sides of a withdrawal and the
// application identity that signs the Open Payments calls.
type actor struct {
	address    string
	publicName string
}

var actors = []actor{
	{address: "tienda-cajadebarrio", publicName: "Don Jaime - Tienda"},
	{address: "laura-cajadebarrio", publicName: "Laura"},
	{address: appAddress, publicName: "Caja de Barrio"},
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

	// Without liquidity the ASE cannot move money, so no payment can complete.
	deposited, err := client.EnsureAssetLiquidity(ctx, asset.ID, assetLiquidityTarget)
	if err != nil {
		return fmt.Errorf("ensuring the asset liquidity: %w", err)
	}
	fmt.Printf("asset liquidity target %d, deposited now %d\n", assetLiquidityTarget, deposited)

	// The tenant setting is what makes Rafiki compose and accept wallet address
	// URLs in https while the stack itself keeps publishing plain http.
	if err := client.EnsureTenantSetting(ctx, rafikiadmin.SettingWalletAddressURL, base); err != nil {
		return fmt.Errorf("ensuring the tenant setting %s: %w", rafikiadmin.SettingWalletAddressURL, err)
	}
	fmt.Printf("tenant setting %s = %s\n", rafikiadmin.SettingWalletAddressURL, base)

	addresses := make(map[string]rafikiadmin.WalletAddress, len(actors))
	for _, a := range actors {
		addressURL := walletAddressURL(base, a.address)

		address, err := client.EnsureWalletAddress(ctx, rafikiadmin.CreateWalletAddressInput{
			AssetID:    asset.ID,
			Address:    addressURL,
			PublicName: a.publicName,
			// Rafiki binds an idempotency key to the arguments of the first request
			// that used it and refuses the same key with different arguments, so the
			// key is derived from the URL: changing the published base has to create
			// the wallet address instead of being rejected.
			IdempotencyKey: "seed:wallet-address:" + addressURL,
		})
		if err != nil {
			return fmt.Errorf("ensuring the wallet address %s: %w", addressURL, err)
		}
		addresses[a.address] = address
		fmt.Printf("wallet address %s id %s %s\n", addressURL, address.ID, address.PublicName)
	}

	// The client identity: the key that signs this backend's Open Payments
	// requests, registered on the wallet address it acts as. Without it no signed
	// request from this backend can be verified.
	key, err := opkeys.LoadOrGenerate(cfg.OP.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("loading the Open Payments client key: %w", err)
	}

	kid := cfg.OP.KeyID
	if kid == "" {
		kid = key.Kid()
	}

	app := addresses[appAddress]
	if err := client.EnsureWalletAddressKey(ctx, rafikiadmin.CreateWalletAddressKeyInput{
		WalletAddressID: app.ID,
		JWK:             rafikiadmin.Ed25519JWK(kid, key.PublicKeyBase64()),
		// The wallet address ID is part of the key on purpose: changing the
		// published base creates a new wallet address, and the same kid has to be
		// registered on it instead of being deduplicated against the old one.
		IdempotencyKey: "client-key:" + kid + ":" + app.ID,
	}); err != nil {
		return fmt.Errorf("registering the client key %s: %w", kid, err)
	}
	fmt.Printf("client key %s registered on %s, private key at %s\n", kid, app.Address, cfg.OP.PrivateKeyPath)

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
