package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/caja"
)

// The rules a reservation can break. The route turns each one into the code of the
// contract that describes it, so nobody has to read a message to classify it.
var (
	// ErrSinEfectivoLibre is a store that is not active or that cannot cover the
	// amount with the cash it still has free.
	ErrSinEfectivoLibre = errors.New("la tienda no puede cubrir el monto con su efectivo libre")

	// ErrTopeTienda is an amount above the ceiling of the store's level.
	ErrTopeTienda = errors.New("el monto supera el tope de la tienda")
)

// Reserva is one reservation of cash, with what it costs.
type Reserva struct {
	ID                    string
	TiendaID              string
	PersonaID             string
	MontoCOP              int64
	ComisionTenderoCOP    int64
	ComisionPlataformaCOP int64
	Codigo                string
	Estado                string
	CreadaEn              time.Time
	VenceEn               time.Time
}

// ReservarParams is what a reservation of cash needs from its caller.
type ReservarParams struct {
	// ID names the reservation: the identifier travels in the answer and in every
	// log line of a withdrawal, so the caller mints it.
	ID        string
	TiendaID  string
	PersonaID string
	MontoCOP  int64

	// Codigo is the four digits only the client sees.
	Codigo string

	// Ahora is when the reservation is made and it decides the expiry, because the
	// clock belongs to the caller and not to the database.
	Ahora time.Time

	// Vigencia is how long the cash stays taken. The contract fixes fifteen
	// minutes; passing another value is the caller's business, not this layer's.
	Vigencia time.Duration
}

// Reservar takes the cash of one store for one person, inside a single transaction.
//
// Two things happen here and not in the route, because both need the locked row:
//
//   - the COMMISSION is computed with the cash the store has free AT THAT MOMENT,
//     which is what the contract fixes. Reading the cash outside the transaction and
//     charging with it inside would be a race that charges a person a number that was
//     true a moment ago.
//   - the store's own row is locked before the cash is read, so two reservations of
//     the same cash cannot both succeed: the second one waits, reads the cash the
//     first one already reserved, and is refused.
//
// zonaDemo is the zone whose calendar day the cash belongs to. pgx sends a time to a
// ::date parameter as the day of the time's own location, so a UTC time after 19:00
// in Bogotá would name tomorrow, a day with no published cash.
var zonaDemo = time.FixedZone("America/Bogota", -5*60*60)

func (s *Store) Reservar(ctx context.Context, params ReservarParams) (Reserva, error) {
	dia := params.Ahora.In(zonaDemo)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Reserva{}, fmt.Errorf("store: starting the reservation: %w", err)
	}
	// The rollback is a no-op once the transaction is committed, and it is the only
	// thing that releases the lock when an error returns early.
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// The store's row is the lock of the whole operation: every reservation of this
	// store waits here, so the cash read below cannot be taken by anybody else.
	var (
		estado string
		nivel  string
	)
	err = tx.QueryRow(ctx, `select estado, nivel from tiendas where id = $1 for update`, params.TiendaID).
		Scan(&estado, &nivel)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reserva{}, fmt.Errorf("store: %w: %s", ErrTiendaNoEncontrada, params.TiendaID)
	}
	if err != nil {
		return Reserva{}, fmt.Errorf("store: locking the store %s: %w", params.TiendaID, err)
	}

	if estado != "activa" {
		return Reserva{}, fmt.Errorf("store: %w: la tienda %s está %s", ErrSinEfectivoLibre, params.TiendaID, estado)
	}
	if tope := caja.TopeTienda(nivel); tope < params.MontoCOP {
		return Reserva{}, fmt.Errorf("store: %w: la tienda es %s y su tope es %d", ErrTopeTienda, nivel, tope)
	}

	var libre int64
	err = tx.QueryRow(ctx, `
		select coalesce(publicado_cop - reservado_cop, 0)
		from efectivo_disponible
		where tienda_id = $1 and fecha = $2::date`, params.TiendaID, dia).
		Scan(&libre)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Reserva{}, fmt.Errorf("store: reading the cash of %s: %w", params.TiendaID, err)
	}
	if libre < params.MontoCOP {
		return Reserva{}, fmt.Errorf("store: %w: %s tiene %d libres y el monto es %d", ErrSinEfectivoLibre, params.TiendaID, libre, params.MontoCOP)
	}

	// The money rules of the contract, applied to the cash of this moment.
	comisiones := caja.ComisionesDe(params.MontoCOP, libre)

	vencimiento := params.Ahora.Add(params.Vigencia)
	reserva := Reserva{
		ID:                    params.ID,
		TiendaID:              params.TiendaID,
		PersonaID:             params.PersonaID,
		MontoCOP:              params.MontoCOP,
		ComisionTenderoCOP:    comisiones.Tendero,
		ComisionPlataformaCOP: comisiones.Plataforma,
		Codigo:                params.Codigo,
		Estado:                "reservada",
		CreadaEn:              params.Ahora,
		VenceEn:               vencimiento,
	}

	_, err = tx.Exec(ctx, `
		insert into reservas (
			id, tienda_id, persona_id, monto_cop, comision_tendero_cop,
			comision_plataforma_cop, codigo, estado, creada_en, vence_en, fecha_efectivo, face_match_ok
		) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, true)`,
		reserva.ID,
		reserva.TiendaID,
		reserva.PersonaID,
		reserva.MontoCOP,
		reserva.ComisionTenderoCOP,
		reserva.ComisionPlataformaCOP,
		reserva.Codigo,
		reserva.Estado,
		reserva.CreadaEn,
		reserva.VenceEn,
		// The day of the cash it took, with the same expression the cash was found
		// with. Recomputing it later from the timestamp is what a release cannot do
		// without assuming a timezone.
		dia,
	)
	if err != nil {
		return Reserva{}, fmt.Errorf("store: writing the reservation %s: %w", reserva.ID, err)
	}

	// The cash is reserved with a relative update and not with a value read before,
	// so a publication of cash that happened while this transaction waited is not
	// lost.
	tag, err := tx.Exec(ctx, `
		update efectivo_disponible
		set reservado_cop = reservado_cop + $3
		where tienda_id = $1 and fecha = $2::date`, params.TiendaID, dia, params.MontoCOP)
	if err != nil {
		return Reserva{}, fmt.Errorf("store: reserving the cash of %s: %w", params.TiendaID, err)
	}
	if tag.RowsAffected() != 1 {
		return Reserva{}, fmt.Errorf("store: reserving the cash of %s updated %d rows, and it had to update one", params.TiendaID, tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return Reserva{}, fmt.Errorf("store: committing the reservation %s: %w", reserva.ID, err)
	}

	return reserva, nil
}

// LiberarVencidas releases the cash of the reservations whose minutes ran out and
// marks them expired, in one transaction: a reservation that expired without a
// payment cannot keep holding a store's cash.
//
// It returns how many it released, so a caller that runs it on a timer can say what
// it did, and running it twice is a no-op the second time.
func (s *Store) LiberarVencidas(ctx context.Context, ahora time.Time) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: starting the release of the expired reservations: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// The rows are locked before they are read, so two releases cannot both give the
	// same cash back to the store.
	rows, err := tx.Query(ctx, `
		select id, tienda_id, monto_cop, fecha_efectivo
		from reservas
		where estado = 'reservada' and vence_en <= $1
		for update`, ahora)
	if err != nil {
		return 0, fmt.Errorf("store: looking for the expired reservations: %w", err)
	}

	type expirada struct {
		tiendaID string
		montoCOP int64
		fecha    time.Time
	}
	var (
		ids       []string
		porTienda = map[string]map[time.Time]int64{}
	)
	for rows.Next() {
		var (
			id       string
			expirada expirada
		)
		if err := rows.Scan(&id, &expirada.tiendaID, &expirada.montoCOP, &expirada.fecha); err != nil {
			rows.Close()

			return 0, fmt.Errorf("store: reading the expired reservations: %w", err)
		}
		ids = append(ids, id)

		// The day is the one the reservation STORED, not one computed here: the cash is
		// given back to the day it was taken from, and recomputing that day would need
		// the timezone the caller used, which this layer does not know.
		if porTienda[expirada.tiendaID] == nil {
			porTienda[expirada.tiendaID] = map[time.Time]int64{}
		}
		porTienda[expirada.tiendaID][expirada.fecha] += expirada.montoCOP
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: reading the expired reservations: %w", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}

	if _, err := tx.Exec(ctx, `update reservas set estado = 'vencida' where id = any($1)`, ids); err != nil {
		return 0, fmt.Errorf("store: marking the expired reservations: %w", err)
	}

	for tiendaID, porDia := range porTienda {
		for dia, monto := range porDia {
			// No cast on the day: the value came out of a date column, so comparing it
			// with the date column keeps the two sides of the comparison in the same
			// type and out of reach of a session timezone.
			tag, err := tx.Exec(ctx, `
				update efectivo_disponible
				set reservado_cop = reservado_cop - $3
				where tienda_id = $1 and fecha = $2`, tiendaID, dia, monto)
			if err != nil {
				return 0, fmt.Errorf("store: giving the cash of %s back: %w", tiendaID, err)
			}
			// Marking a reservation expired while its cash stays taken is the failure
			// this whole day exists to prevent, and it would be silent: the money would
			// stay locked with no error anywhere. So a day with no cash row is loud.
			if tag.RowsAffected() != 1 {
				return 0, fmt.Errorf("store: giving the cash of %s back on %s affected %d rows, and it had to affect one",
					tiendaID, dia.Format("2006-01-02"), tag.RowsAffected())
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: committing the release of the expired reservations: %w", err)
	}

	return len(ids), nil
}
