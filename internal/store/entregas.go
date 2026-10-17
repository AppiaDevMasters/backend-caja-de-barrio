package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrReservaEnOtroEstado is returned when a delivery step finds the reservation in a
// state it cannot act on; the error carries that state.
type ErrReservaEnOtroEstado struct{ Estado string }

func (e ErrReservaEnOtroEstado) Error() string {
	return "la reserva está " + e.Estado
}

// Entrega is where a delivery stands after one confirmation.
type Entrega struct {
	Estado       string
	FaltaTendero bool
	FaltaCliente bool
}

// ConfirmarEntrega records one side of the double confirmation (contract 7.6 and 7.7).
// The storekeeper confirms with the code; the client says whether she received the
// cash. When both did, the withdrawal closes: the reservation is entregada and the
// cash leaves the store's published cash for the day it was taken from. A client who
// says she did not receive it opens a dispute instead.
func (s *Store) ConfirmarEntrega(ctx context.Context, confirmacionID, reservaID, parte string, recibido bool,
	texto, alertaID string) (Entrega, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Entrega{}, fmt.Errorf("store: confirming the delivery of %s: %w", reservaID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	var (
		estado, tiendaID, personaID string
		tendero, cliente            bool
	)
	err = tx.QueryRow(ctx, `
		select estado, tienda_id, persona_id, entrega_tendero_en is not null, recibido_cliente_en is not null
		from reservas where id = $1 for update`, reservaID).Scan(&estado, &tiendaID, &personaID, &tendero, &cliente)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entrega{}, ErrReservaEnOtroEstado{Estado: "inexistente"}
	}
	if err != nil {
		return Entrega{}, fmt.Errorf("store: reading the reservation %s: %w", reservaID, err)
	}
	if estado != "pagada" {
		return Entrega{}, ErrReservaEnOtroEstado{Estado: estado}
	}

	if _, err := tx.Exec(ctx, `
		insert into confirmaciones_entrega (id, reserva_id, parte, recibido, texto_usuario)
		values ($1, $2, $3, $4, nullif($5, ''))`, confirmacionID, reservaID, parte, recibido, texto); err != nil {
		return Entrega{}, fmt.Errorf("store: recording the confirmation of %s: %w", reservaID, err)
	}

	if parte == "cliente" && !recibido {
		if _, err := tx.Exec(ctx, `update reservas set estado = 'en_disputa' where id = $1`, reservaID); err != nil {
			return Entrega{}, fmt.Errorf("store: opening the dispute of %s: %w", reservaID, err)
		}
		if _, err := tx.Exec(ctx, `
			insert into alertas (id, persona_id, tienda_id, regla, detalle) values ($1, $2, $3, 'entrega_no_recibida', $4)`,
			alertaID, personaID, tiendaID, "reserva "+reservaID+": "+texto); err != nil {
			return Entrega{}, fmt.Errorf("store: raising the dispute alert of %s: %w", reservaID, err)
		}

		return Entrega{Estado: "en_disputa"}, tx.Commit(ctx)
	}

	columna := "entrega_tendero_en"
	if parte == "cliente" {
		columna = "recibido_cliente_en"
		cliente = true
	} else {
		tendero = true
	}
	if _, err := tx.Exec(ctx, `update reservas set `+columna+` = coalesce(`+columna+`, now()) where id = $1`, reservaID); err != nil {
		return Entrega{}, fmt.Errorf("store: recording the %s side of %s: %w", parte, reservaID, err)
	}

	if tendero && cliente {
		estado = "entregada"
		if _, err := tx.Exec(ctx, `update reservas set estado = 'entregada', entregado_en = now() where id = $1`, reservaID); err != nil {
			return Entrega{}, fmt.Errorf("store: closing %s: %w", reservaID, err)
		}
		// The cash left the drawer: it is no longer reserved nor published for that day.
		if _, err := tx.Exec(ctx, `
			update efectivo_disponible e
			set reservado_cop = greatest(e.reservado_cop - r.monto_cop, 0),
			    publicado_cop = greatest(e.publicado_cop - r.monto_cop, 0)
			from reservas r
			where r.id = $1 and e.tienda_id = r.tienda_id and e.fecha = r.fecha_efectivo`, reservaID); err != nil {
			return Entrega{}, fmt.Errorf("store: taking the delivered cash of %s: %w", reservaID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Entrega{}, fmt.Errorf("store: committing the delivery of %s: %w", reservaID, err)
	}

	return Entrega{Estado: estado, FaltaTendero: !tendero, FaltaCliente: !cliente}, nil
}

// CancelarReserva cancels a reservation that has not paid anything and gives its cash
// back to the store, once.
func (s *Store) CancelarReserva(ctx context.Context, reservaID, motivo string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: cancelling %s: %w", reservaID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	var (
		estado string
		pagos  bool
	)
	err = tx.QueryRow(ctx, `
		select r.estado, x.outgoing_payment_id is not null
		from reservas r left join retiros x on x.reserva_id = r.id
		where r.id = $1 for update of r`, reservaID).Scan(&estado, &pagos)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrReservaEnOtroEstado{Estado: "inexistente"}
	}
	if err != nil {
		return fmt.Errorf("store: reading %s: %w", reservaID, err)
	}
	if estado != "reservada" {
		return ErrReservaEnOtroEstado{Estado: estado}
	}
	if pagos {
		// A payment was already sent: cancelling would keep the money and free the cash.
		return ErrReservaEnOtroEstado{Estado: "con un pago en curso"}
	}

	if _, err := tx.Exec(ctx, `update reservas set estado = 'cancelada', motivo_cancelacion = $2 where id = $1`,
		reservaID, motivo); err != nil {
		return fmt.Errorf("store: cancelling %s: %w", reservaID, err)
	}
	if _, err := tx.Exec(ctx, `
		update efectivo_disponible e
		set reservado_cop = greatest(e.reservado_cop - r.monto_cop, 0)
		from reservas r
		where r.id = $1 and e.tienda_id = r.tienda_id and e.fecha = r.fecha_efectivo`, reservaID); err != nil {
		return fmt.Errorf("store: giving back the cash of %s: %w", reservaID, err)
	}

	return tx.Commit(ctx)
}

// ReservaActiva is one withdrawal a storekeeper is waiting for.
type ReservaActiva struct {
	ID                 string
	Estado             string
	MontoCOP           int64
	ComisionTenderoCOP int64
	Cliente            string
}

// ReservasActivasDeTienda lists the withdrawals of a store that are still open.
func (s *Store) ReservasActivasDeTienda(ctx context.Context, tiendaID string) ([]ReservaActiva, error) {
	rows, err := s.pool.Query(ctx, `
		select r.id, r.estado, r.monto_cop, r.comision_tendero_cop, split_part(p.nombre, ' ', 1)
		from reservas r join personas p on p.id = r.persona_id
		where r.tienda_id = $1 and r.estado in ('reservada', 'pagada', 'en_disputa')
		order by r.creada_en`, tiendaID)
	if err != nil {
		return nil, fmt.Errorf("store: listing the reservations of %s: %w", tiendaID, err)
	}
	defer rows.Close()

	reservas := []ReservaActiva{}
	for rows.Next() {
		var r ReservaActiva
		if err := rows.Scan(&r.ID, &r.Estado, &r.MontoCOP, &r.ComisionTenderoCOP, &r.Cliente); err != nil {
			return nil, fmt.Errorf("store: reading a reservation of %s: %w", tiendaID, err)
		}
		reservas = append(reservas, r)
	}

	return reservas, rows.Err()
}
