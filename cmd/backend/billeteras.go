package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

// billeterasRafiki creates the wallet a newly registered person pays from, in this
// backend's own ASE: the same Admin API calls the seed makes for the demo actors.
type billeterasRafiki struct {
	cliente *rafikiadmin.Client
	base    string

	mu  sync.Mutex
	cop string // id of the COP asset, read once
}

func nuevasBilleteras(cfg config.Config) (*billeterasRafiki, error) {
	base := strings.TrimSuffix(strings.TrimSpace(cfg.Rafiki.OpenPaymentsURL), "/")
	if !strings.HasPrefix(base, "https://") {
		return nil, errors.New("RAFIKI_OPEN_PAYMENTS_URL must be https to create wallet addresses")
	}
	cliente, err := rafikiadmin.New(rafikiadmin.Config{
		URL:       cfg.Rafiki.AdminURL,
		TenantID:  cfg.Rafiki.TenantID,
		APISecret: cfg.Rafiki.AdminAPISecret,
	})
	if err != nil {
		return nil, err
	}

	return &billeterasRafiki{cliente: cliente, base: base}, nil
}

// CrearBilletera creates (idempotently) https://<base>/<slug>-<id suffix> in COP.
func (b *billeterasRafiki) CrearBilletera(ctx context.Context, personaID, nombre string) (string, error) {
	activo, err := b.activoCOP(ctx)
	if err != nil {
		return "", err
	}

	sufijo := personaID
	if i := strings.LastIndex(personaID, "_"); i >= 0 {
		sufijo = personaID[i+1:]
	}
	if len(sufijo) > 6 {
		sufijo = sufijo[:6]
	}
	direccion := b.base + "/" + slug(nombre) + "-" + sufijo

	nombrePublico := strings.TrimSpace(nombre)
	if nombrePublico == "" {
		nombrePublico = "Persona " + sufijo
	}
	wallet, err := b.cliente.EnsureWalletAddress(ctx, rafikiadmin.CreateWalletAddressInput{
		AssetID:        activo,
		Address:        direccion,
		PublicName:     nombrePublico,
		IdempotencyKey: "registro:wallet-address:" + direccion,
	})
	if err != nil {
		return "", fmt.Errorf("creating the wallet address %s: %w", direccion, err)
	}

	return wallet.Address, nil
}

func (b *billeterasRafiki) activoCOP(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cop != "" {
		return b.cop, nil
	}
	activo, existe, err := b.cliente.AssetByCodeAndScale(ctx, "COP", 2)
	if err != nil {
		return "", err
	}
	if !existe {
		return "", errors.New("the COP asset does not exist: run the seed")
	}
	b.cop = activo.ID

	return b.cop, nil
}

// slug turns a name into the path of a wallet address: lowercase ASCII letters and
// digits, joined by dashes.
func slug(nombre string) string {
	reemplazos := strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ñ", "n", "ü", "u")
	limpio := reemplazos.Replace(strings.ToLower(strings.TrimSpace(nombre)))

	var partes []string
	actual := strings.Builder{}
	for _, r := range limpio {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			actual.WriteRune(r)
			continue
		}
		if actual.Len() > 0 {
			partes = append(partes, actual.String())
			actual.Reset()
		}
	}
	if actual.Len() > 0 {
		partes = append(partes, actual.String())
	}
	if len(partes) == 0 {
		return "persona"
	}
	if len(partes) > 3 {
		partes = partes[:3]
	}

	return strings.Join(partes, "-")
}
