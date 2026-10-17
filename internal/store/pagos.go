package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Cupo is the interactive grant of one person (contract 8.2): the ceiling the
// authorization server imposed, what has already been used, in which currency, and
// whether the person ever approved it.
//
// The ceiling and what was used are numeric and not whole pesos, because the cupo
// lives in the client's own currency and a dollar cupo has decimals.
type Cupo struct {
	Aprobado bool
	Limite   float64
	Usado    float64
	Moneda   string
	// WalletAddress is the wallet the grant was asked on: a cupo serves that wallet only.
	WalletAddress string
}

// CupoDe returns the cupo of one person and says whether she has one.
func (s *Store) CupoDe(ctx context.Context, personaID string) (Cupo, bool, error) {
	const query = `
		select limite, usado, moneda, aprobado_en, wallet_address
		from cupos
		where persona_id = $1`

	var (
		cupo       Cupo
		aprobadoEn *string
	)
	err := s.pool.QueryRow(ctx, query, personaID).Scan(&cupo.Limite, &cupo.Usado, &cupo.Moneda, &aprobadoEn, &cupo.WalletAddress)
	if errors.Is(err, pgx.ErrNoRows) {
		return Cupo{}, false, nil
	}
	if err != nil {
		return Cupo{}, false, fmt.Errorf("store: reading the cupo of %s: %w", personaID, err)
	}
	cupo.Aprobado = aprobadoEn != nil

	return cupo, true, nil
}
