// Command backend is the Caja de Barrio service. It plays the two roles the
// architecture needs in one binary: the ASE that Rafiki calls back (webhooks,
// exchange rates and IdP) and the Open Payments client used for withdrawals.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/ase"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/httpserver"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("backend stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The endpoints Rafiki and the identity provider call back into mount
	// themselves on the backend router, which keeps a single router and a single
	// health endpoint that the compose healthcheck waits on.
	aseServer, err := ase.New(ase.Config{
		SignatureSecret:      cfg.Rafiki.SignatureSecret,
		IdentityServerSecret: cfg.Rafiki.IdentityServerSecret,
		// The identity provider needs the authorization server twice: the address
		// that resolves inside the compose network for the server to server calls,
		// and the one the user's browser opens for the final redirect.
		AuthServerURL:       cfg.Rafiki.AuthServerURL,
		AuthServerPublicURL: cfg.Rafiki.AuthServerPublicURL,
	})
	if err != nil {
		return err
	}

	// The data layer arrives with its own task. Without DATABASE_URL the backend
	// still serves what Rafiki calls, which is what the running stack needs, and the
	// routes that need the database report it themselves when they arrive.
	if cfg.Database.URL != "" {
		database, err := store.Open(ctx, store.Config{DatabaseURL: cfg.Database.URL})
		if err != nil {
			return err
		}
		defer database.Close()

		if _, err := database.Migrate(ctx); err != nil {
			return err
		}
	}

	return httpserver.New(cfg, aseServer.Register).Run(ctx)
}
