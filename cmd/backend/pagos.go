package main

import (
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
		FinishURL:        cfg.OP.FinishURL,
		WalletPlataforma: cfg.OP.WalletPlataforma,
		AuthServerHash:   authServer + "/" + cfg.Rafiki.TenantID,
		HostASE:          host,
		Secreto:          []byte(cfg.Internal.Token),
	}

	return deps
}
