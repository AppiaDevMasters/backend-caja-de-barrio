// Command migrate applies the schema migrations of the data layer and exits.
//
// The backend migrates on start-up too; this command is the operational entry
// point, so a database can be prepared without starting the service, and it prints
// what it applied, which is what makes a second run show that it applied nothing.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migrate stopped with an error", "error", err)
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

	database, err := store.Open(ctx, store.Config{DatabaseURL: cfg.Database.URL})
	if err != nil {
		return err
	}
	defer database.Close()

	applied, err := database.Migrate(ctx)
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		fmt.Println("nada por aplicar: la base ya tiene el esquema de este binario")
		return nil
	}
	for _, name := range applied {
		fmt.Printf("aplicada %s\n", name)
	}

	return nil
}
