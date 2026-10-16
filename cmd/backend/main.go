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
	aseServer := ase.New(ase.Config{
		SignatureSecret:      cfg.Rafiki.SignatureSecret,
		IdentityServerSecret: cfg.Rafiki.IdentityServerSecret,
	})

	return httpserver.New(cfg, aseServer.Register).Run(ctx)
}
