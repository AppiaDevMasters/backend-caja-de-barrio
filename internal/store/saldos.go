package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Movimiento is one entry of the demo's virtual wallet.
type Movimiento struct {
	Tipo  string
	Monto int64
	En    time.Time
}

// Billetera is the virtual wallet of one number: the store's one when the number
// is a storekeeper's, the person's otherwise.
type Billetera struct {
	WalletAddress string
	Saldo         int64
	Movimientos   []Movimiento
}

// RegistrarMovimiento adds one entry ONCE: the same wallet, kind and reference
// are recorded a single time.
func (s *Store) RegistrarMovimiento(ctx context.Context, id, wallet, tipo string, monto int64, referencia string) error {
	const query = `
		insert into movimientos (id, wallet_address, tipo, monto, referencia)
		values ($1, $2, $3, $4, $5)
		on conflict (wallet_address, tipo, referencia) do nothing`
	if _, err := s.pool.Exec(ctx, query, id, wallet, tipo, monto, referencia); err != nil {
		return fmt.Errorf("store: recording the movement %s: %w", id, err)
	}

	return nil
}

// BilleteraDe returns the wallet of a number, its balance and its last five
// movements, and says whether the number has a wallet at all.
func (s *Store) BilleteraDe(ctx context.Context, whatsapp string) (Billetera, bool, error) {
	const wallets = `
		select wallet_address from (
			select wallet_address, 1 as orden from tiendas where whatsapp_tendero = $1 and wallet_address <> ''
			union all
			select wallet_address, 2 from personas where whatsapp = $1 and wallet_address <> ''
		) w
		order by orden
		limit 1`

	var billetera Billetera
	err := s.pool.QueryRow(ctx, wallets, whatsapp).Scan(&billetera.WalletAddress)
	if errors.Is(err, pgx.ErrNoRows) {
		return Billetera{}, false, nil
	}
	if err != nil {
		return Billetera{}, false, fmt.Errorf("store: reading the wallet of a number: %w", err)
	}

	const saldo = `select coalesce(sum(monto), 0)::bigint from movimientos where wallet_address = $1`
	if err := s.pool.QueryRow(ctx, saldo, billetera.WalletAddress).Scan(&billetera.Saldo); err != nil {
		return Billetera{}, false, fmt.Errorf("store: adding up the balance: %w", err)
	}

	const ultimos = `
		select tipo, monto, creado_en from movimientos
		where wallet_address = $1
		order by creado_en desc
		limit 5`
	rows, err := s.pool.Query(ctx, ultimos, billetera.WalletAddress)
	if err != nil {
		return Billetera{}, false, fmt.Errorf("store: reading the movements: %w", err)
	}
	defer rows.Close()

	billetera.Movimientos = []Movimiento{}
	for rows.Next() {
		var movimiento Movimiento
		if err := rows.Scan(&movimiento.Tipo, &movimiento.Monto, &movimiento.En); err != nil {
			return Billetera{}, false, fmt.Errorf("store: reading a movement: %w", err)
		}
		billetera.Movimientos = append(billetera.Movimientos, movimiento)
	}

	return billetera, true, rows.Err()
}
