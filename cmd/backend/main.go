// Command backend is the Caja de Barrio service. It plays the two roles the
// architecture needs in one binary: the ASE that Rafiki calls back (webhooks,
// exchange rates and IdP) and the Open Payments client used for withdrawals.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/ase"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/httpserver"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/identidad"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/mapa"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/verifik"
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
		// The identity provider reads and resolves the interaction on the interaction server,
		// which is a different address and a different path than the browser's.
		InteractionServerURL: cfg.Rafiki.InteractionServerURL,
	})
	if err != nil {
		return err
	}

	// The routes of this backend: the endpoints Rafiki calls, and the internal surface
	// of the contract when its two requirements are configured.
	mounts := []func(*http.ServeMux){aseServer.Register}

	// The data layer arrives with its own task. Without DATABASE_URL the backend
	// still serves what Rafiki calls, which is what the running stack needs.
	if cfg.Database.URL != "" {
		database, err := store.Open(ctx, store.Config{DatabaseURL: cfg.Database.URL})
		if err != nil {
			return err
		}
		defer database.Close()

		if _, err := database.Migrate(ctx); err != nil {
			return err
		}

		// The internal surface needs the token of every call and a data layer for its
		// routes, so it appears only when both are configured: mounted without a token
		// it would be an open surface, and without a database it would be routes that
		// cannot answer.
		if cfg.Internal.Token != "" {
			internal, err := api.New(api.Config{ServiceToken: cfg.Internal.Token})
			if err != nil {
				return err
			}

			// The cédula validation needs the registry. Without its token the route is still
			// mounted and answers that it is not configured, which is better than leaving a
			// caller with a path that does not exist.
			var cedulas identidad.Cedulas
			if cfg.Verifik.Token != "" {
				client, err := verifik.New(cfg.Verifik.Token)
				if err != nil {
					return err
				}
				cedulas = client
			}

			// The wallet address of the client, which is what resolves a wallet before a person
			// is registered. It signs with the key the seed registered, so the key has to be
			// there: without it this route could not check anything and the surface is the
			// thing that exists to answer.
			cliente, err := opclient.New(opclient.Config{
				WalletAddressURL: cfg.OP.ClientWalletAddress,
				PrivateKeyPath:   cfg.OP.PrivateKeyPath,
				KeyID:            cfg.OP.KeyID,
				HostRewrites:     cfg.HostRewrites(),
			})
			if err != nil {
				return err
			}

			mapa.Register(internal, database)
			identidad.Register(internal, identidad.Dependencias{Datos: database, Cedulas: cedulas, Carteras: cliente})
			mounts = append(mounts, internal.Register)
		}
	}

	return httpserver.New(cfg, mounts...).Run(ctx)
}
