package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/admin"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/identidad"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// usuariosPanel are the operators the backend creates from deploy/llaves.env, one per
// role, with the usernames the panel's mock already uses.
var usuariosPanel = []store.UsuarioPanel{
	{Username: "super", Nombre: "Superadmin", Rol: "superadmin"},
	{Username: "cumplimiento", Nombre: "Oficial de cumplimiento", Rol: "cumplimiento"},
	{Username: "soporte", Nombre: "Soporte", Rol: "soporte"},
}

// nuevoPanel seeds the operators and builds the /admin surface.
func nuevoPanel(ctx context.Context, cfg config.Config, database *store.Store, billeteras identidad.Billeteras) (*admin.Server, error) {
	if len(cfg.Panel.Origenes) == 0 {
		return nil, errors.New("PANEL_ORIGINS is empty")
	}

	sembrados := 0
	for _, usuario := range usuariosPanel {
		clave := cfg.Panel.Claves[usuario.Rol]
		if clave == "" {
			continue
		}
		hash, err := admin.HashClave(clave)
		if err != nil {
			return nil, err
		}
		usuario.ClaveHash = hash
		if err := database.GuardarUsuarioPanel(ctx, usuario); err != nil {
			return nil, err
		}
		sembrados++
	}
	if sembrados == 0 {
		slog.Warn("no PANEL_CLAVE_* is set: nobody can sign in to the panel")
	}

	// The session key is derived from the service token, so it needs no variable of its
	// own and changes when the token is rotated.
	secreto := sha256.Sum256([]byte("caja-de-barrio/panel/sesion:" + cfg.Internal.Token))

	var avisos admin.Avisos
	if cfg.Panel.AgenteURL != "" {
		avisos = admin.AvisosAgente{URL: cfg.Panel.AgenteURL, Token: cfg.Internal.Token}
	}
	var crear admin.Billeteras
	if billeteras != nil {
		crear = billeteras
	}

	return admin.New(admin.Config{
		Origenes:     cfg.Panel.Origenes,
		Secreto:      secreto[:],
		SaldoInicial: cfg.Didit.SaldoInicialDemo,
	}, database, crear, avisos)
}
