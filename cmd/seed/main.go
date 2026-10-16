// Command seed provisions the local ASE with what the demo needs: the two
// currencies (pesos for the withdrawal, dollars for the remittance), their
// liquidity and one wallet address per actor. It is idempotent, so it can run
// again after every stack restart.
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

// The two currencies of the demo: the withdrawal moves pesos and the remittance is
// paid in dollars. Both use two decimals, which is the asset scale Open Payments
// works with for them.
const (
	assetCOP   = "COP"
	assetUSD   = "USD"
	assetScale = 2
)

// appAddress is the wallet address this backend acts as: the identity whose key
// signs its Open Payments requests.
const appAddress = "app-cajadebarrio"

// platformAddress is the COP account where the 1% platform commission is charged.
// The contract asks for a COP account under the same user as app-cajadebarrio, so
// the exact 1% lands in pesos instead of having to be converted from the EUR
// account of the test wallet.
const platformAddress = "app-cajadebarrio-cop"

// clientKeyAddresses are the wallet addresses this backend acts as the resource
// owner of, and therefore the ones its signing key has to be registered on.
var clientKeyAddresses = []string{appAddress, platformAddress}

// asset is one currency of the demo the seed provisions.
type asset struct {
	code string

	// liquidity funds the asset for the demo, in its smallest unit: with scale 2
	// this is 10.000.000 COP and 3.000,00 USD. The dollar target is about what the
	// peso target is worth at the demo rate (USD 1 = COP 4.000), with margin, so a
	// remittance always has room to move.
	liquidity rafikiadmin.UInt64
}

var assets = []asset{
	{code: assetCOP, liquidity: 1_000_000_000},
	{code: assetUSD, liquidity: 300_000},
}

// actor is one wallet address the demo needs, in the currency it keeps.
type actor struct {
	address    string
	publicName string
	assetCode  string
}

var actors = []actor{
	{address: "tienda-cajadebarrio", publicName: "Don Jaime - Tienda", assetCode: assetCOP},
	{address: "laura-cajadebarrio", publicName: "Laura", assetCode: assetCOP},
	{address: appAddress, publicName: "Caja de Barrio", assetCode: assetCOP},
	// The 1% of the platform is charged on its own address, so it is not mixed
	// with the application identity.
	{address: platformAddress, publicName: "Caja de Barrio - Comision", assetCode: assetCOP},
	// Andres pays the remittance in dollars and Rosa withdraws it in pesos at the
	// store: the contract (section 4) gives both the same USD address, so there is
	// no separate rosa-* wallet address.
	{address: "andres-cajadebarrio", publicName: "Andres", assetCode: assetUSD},
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

	// Every wallet address belongs to an asset, and Rafiki refuses one whose asset
	// does not exist, so the currencies come first.
	currencies := make(map[string]rafikiadmin.Asset, len(assets))
	for _, a := range assets {
		asset, err := client.EnsureAsset(ctx, a.code, assetScale)
		if err != nil {
			return fmt.Errorf("ensuring the %s asset: %w", a.code, err)
		}

		// Without liquidity the ASE cannot move money, so no payment can complete.
		deposited, err := client.EnsureAssetLiquidity(ctx, asset.ID, a.liquidity)
		if err != nil {
			return fmt.Errorf("ensuring the %s asset liquidity: %w", a.code, err)
		}

		currencies[a.code] = asset
		fmt.Printf("asset %s scale %d id %s liquidity target %d deposited now %d\n",
			asset.Code, asset.Scale, asset.ID, a.liquidity, deposited)
	}

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
			AssetID:    currencies[a.assetCode].ID,
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
		fmt.Printf("wallet address %s asset %s id %s %s\n", addressURL, a.assetCode, address.ID, address.PublicName)
	}

	// The client identity: the key that signs this backend's Open Payments
	// requests, registered on every wallet address this backend acts as. Without it
	// no signed request from this backend can be verified.
	key, err := opkeys.LoadOrGenerate(cfg.OP.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("loading the Open Payments client key: %w", err)
	}

	kid := cfg.OP.KeyID
	if kid == "" {
		kid = key.Kid()
	}

	for _, address := range clientKeyAddresses {
		account := addresses[address]
		if err := client.EnsureWalletAddressKey(ctx, rafikiadmin.CreateWalletAddressKeyInput{
			WalletAddressID: account.ID,
			JWK:             rafikiadmin.Ed25519JWK(kid, key.PublicKeyBase64()),
			// The wallet address ID is part of the key on purpose: changing the
			// published base creates a new wallet address, and the same kid has to be
			// registered on it instead of being deduplicated against the old one.
			IdempotencyKey: "client-key:" + kid + ":" + account.ID,
		}); err != nil {
			return fmt.Errorf("registering the client key %s on %s: %w", kid, address, err)
		}
		fmt.Printf("client key %s registered on %s\n", kid, account.Address)
	}
	fmt.Printf("client key %s, private key at %s\n", kid, cfg.OP.PrivateKeyPath)

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
