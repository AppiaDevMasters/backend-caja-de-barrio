package store

import (
	"context"
	"fmt"
	"time"
)

// PagoExterno is one withdrawal paid from a wallet of another ASE (the testnet mode):
// what Caja de Barrio knows of it, because the wallet's balance belongs to its own ASE.
type PagoExterno struct {
	ReservaID   string
	Debito      float64
	Moneda      string
	RecibidoCOP int64
	Tienda      string
	Cliente     string
	En          time.Time
}

// PagosExternosDe returns who the number is (tienda or persona) and the withdrawals it
// paid or collected through outgoing payments of the given resource server, newest first.
func (s *Store) PagosExternosDe(ctx context.Context, whatsapp, prefijoPago string) (string, []PagoExterno, error) {
	var tipo string
	err := s.pool.QueryRow(ctx, `
		select case
		  when exists (select 1 from tiendas where whatsapp_tendero = $1) then 'tienda'
		  when exists (select 1 from personas where whatsapp = $1) then 'persona'
		  else '' end`, whatsapp).Scan(&tipo)
	if err != nil {
		return "", nil, fmt.Errorf("store: reading who %s is: %w", whatsapp, err)
	}
	if tipo == "" {
		return "", nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		select r.id, coalesce(x.debito, 0)::float8, coalesce(x.moneda, ''), r.monto_cop + r.comision_tendero_cop,
		       t.nombre, p.nombre, coalesce(r.pagada_en, r.creada_en)
		from reservas r
		join retiros x on x.reserva_id = r.id
		join tiendas t on t.id = r.tienda_id
		join personas p on p.id = r.persona_id
		where r.estado in ('pagada', 'entregada')
		  and x.outgoing_payment_id like $2 || '%'
		  and (($3 = 'tienda' and t.whatsapp_tendero = $1) or ($3 = 'persona' and p.whatsapp = $1))
		order by coalesce(r.pagada_en, r.creada_en) desc
		limit 10`, whatsapp, prefijoPago, tipo)
	if err != nil {
		return "", nil, fmt.Errorf("store: reading the external payments of %s: %w", whatsapp, err)
	}
	defer rows.Close()

	pagos := []PagoExterno{}
	for rows.Next() {
		var p PagoExterno
		if err := rows.Scan(&p.ReservaID, &p.Debito, &p.Moneda, &p.RecibidoCOP, &p.Tienda, &p.Cliente, &p.En); err != nil {
			return "", nil, fmt.Errorf("store: reading an external payment: %w", err)
		}
		pagos = append(pagos, p)
	}

	return tipo, pagos, rows.Err()
}
