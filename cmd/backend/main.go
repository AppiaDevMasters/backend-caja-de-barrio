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

	return httpserver.New(cfg).Run(ctx)
}
