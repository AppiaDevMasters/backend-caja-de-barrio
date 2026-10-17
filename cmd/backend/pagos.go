package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/pagos"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// nuevosPagos wires the payment routes (contract 8.x) to Open Payments and to the
// Admin API that puts up the liquidity of the wallets this ASE serves.
func nuevosPagos(cfg config.Config, database *store.Store, cliente *opclient.Client) pagos.Dependencias {
	deps := pagos.Dependencias{Datos: database, Cobros: pagos.LeerCobroPublico(nil)}
	if cliente == nil {
		return deps
	}
	deps.OpenPayments = cliente

	if admin, err := rafikiadmin.New(rafikiadmin.Config{
		URL:       cfg.Rafiki.AdminURL,
		TenantID:  cfg.Rafiki.TenantID,
		APISecret: cfg.Rafiki.AdminAPISecret,
	}); err == nil {
		deps.Liquidez = admin
	} else {
		slog.Warn("payments of this ASE's wallets will not get liquidity", "error", err)
	}

	// The callback hash binds the URL the authorization server has for itself: its public
	// base plus the tenant (cmd/retiro proved it against Rafiki).
	authServer := strings.TrimSuffix(cfg.Rafiki.AuthServerPublicURL, "/")
	if authServer == "" {
		authServer = strings.TrimSuffix(cfg.Rafiki.AuthServerURL, "/")
	}
	host := ""
	if base, err := url.Parse(cfg.Rafiki.OpenPaymentsURL); err == nil {
		host = base.Host
	}

	deps.Config = pagos.Config{
		FinishURL:            cfg.OP.FinishURL,
		WalletPlataforma:     cfg.OP.WalletPlataforma,
		AuthServerHash:       authServer + "/" + cfg.Rafiki.TenantID,
		HostASE:              host,
		Secreto:              []byte(cfg.Internal.Token),
		Testnet:              cfg.OP.Modo == config.ModoTestnet,
		WalletClienteTestnet: cfg.OP.TestnetWalletCliente,
		WalletTiendaTestnet:  cfg.OP.TestnetWalletTienda,
		CupoTestnet:          cfg.OP.TestnetCupo,
	}

	return deps
}

// validarModoTestnet resolves the two testnet wallets before the service starts: a
// demo that would fail at the first payment fails at start, saying which wallet.
func validarModoTestnet(ctx context.Context, cfg config.Config, cliente *opclient.Client) error {
	if cfg.OP.Modo != config.ModoTestnet {
		return nil
	}
	for _, w := range []struct{ nombre, url string }{
		{"OP_TESTNET_WALLET_CLIENTE", cfg.OP.TestnetWalletCliente},
		{"OP_TESTNET_WALLET_TIENDA", cfg.OP.TestnetWalletTienda},
	} {
		documento, err := cliente.ResolveWalletAddress(ctx, w.url)
		if err != nil {
			return fmt.Errorf("OP_MODO=testnet: %s=%s does not resolve by Open Payments: %w", w.nombre, w.url, err)
		}
		slog.Info("testnet wallet ready", "variable", w.nombre, "wallet", documento.URL, "asset", documento.AssetCode)
	}

	return nil
}
