package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TiendaCercana is one store the nearest-store search returned, with the cash it
// still has free that day.
type TiendaCercana struct {
	ID            string
	Nombre        string
	Direccion     string
	Metros        float64
	Lat           float64
	Lon           float64
	WalletAddress string
	EfectivoLibre int64
}

// TiendasCercanasParams is the search of the contract (6.1): where the person is,
// how much they want, how far they will go, which store levels can serve that
// amount, and how many stores are worth answering with.
type TiendasCercanasParams struct {
	Lat      float64
	Lon      float64
	MontoCOP int64
	RadioM   int

	// Niveles are the store levels whose ceiling is at least the amount. The
	// ceilings are a business rule and live in the code that calls this, so they
	// arrive resolved instead of being duplicated here.
	Niveles []string

	// Fecha is the day whose published cash counts. A store publishes its cash per
	// day and the day depends on the zone of the demo, so the caller decides it
	// rather than letting the database decide with its own clock.
	Fecha time.Time

	Maximo int
}

// TiendasCercanas returns at most Maximo active stores of those levels that can
// serve the amount that day, ordered by distance, closest first.
//
// The radius and the order are PostGIS work, and the cash a store has free is what
// it published minus what it already reserved: a store that cannot cover the
// withdrawal is not a store the agent should offer.
func (s *Store) TiendasCercanas(ctx context.Context, params TiendasCercanasParams) ([]TiendaCercana, error) {
	if len(params.Niveles) == 0 {
		return nil, fmt.Errorf("store: Niveles is empty, and no store level can serve the amount")
	}
	if params.Maximo <= 0 {
		return nil, fmt.Errorf("store: Maximo=%d, want a positive number of stores", params.Maximo)
	}
	if params.RadioM <= 0 {
		return nil, fmt.Errorf("store: RadioM=%d, want a positive radius", params.RadioM)
	}

	// The point is written twice on purpose: the comparison and the order have to
	// use the same one, and the planner resolves the expression once.
	const query = `
		select t.id,
		       t.nombre,
		       t.direccion,
		       ST_Distance(t.ubicacion, ST_MakePoint($1, $2)::geography) as metros,
		       ST_Y(t.ubicacion::geometry) as lat,
		       ST_X(t.ubicacion::geometry) as lon,
		       t.wallet_address,
		       coalesce(e.publicado_cop - e.reservado_cop, 0) as efectivo_libre_cop
		from tiendas t
		left join efectivo_disponible e
		       on e.tienda_id = t.id and e.fecha = $6::date
		where t.estado = 'activa'
		  and t.nivel = any($5)
		  and coalesce(e.publicado_cop - e.reservado_cop, 0) >= $4
		  and ST_DWithin(t.ubicacion, ST_MakePoint($1, $2)::geography, $3)
		order by t.ubicacion <-> ST_MakePoint($1, $2)::geography
		limit $7`

	rows, err := s.pool.Query(ctx, query,
		params.Lon,
		params.Lat,
		float64(params.RadioM),
		params.MontoCOP,
		params.Niveles,
		params.Fecha,
		params.Maximo,
	)
	if err != nil {
		return nil, fmt.Errorf("store: looking for the nearest stores: %w", err)
	}
	defer rows.Close()

	var stores []TiendaCercana
	for rows.Next() {
		var store TiendaCercana
		if err := rows.Scan(
			&store.ID,
			&store.Nombre,
			&store.Direccion,
			&store.Metros,
			&store.Lat,
			&store.Lon,
			&store.WalletAddress,
			&store.EfectivoLibre,
		); err != nil {
			return nil, fmt.Errorf("store: reading the nearest stores: %w", err)
		}
		stores = append(stores, store)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the nearest stores: %w", err)
	}

	return stores, nil
}

// ErrTiendaNoEncontrada says the store does not exist, which the route answers as
// the contract's NO_ENCONTRADO.
var ErrTiendaNoEncontrada = errors.New("la tienda no existe")

// ErrEfectivoMenorQueReservado says the store tried to publish less cash than it
// already reserved, which the contract refuses (6.3).
var ErrEfectivoMenorQueReservado = errors.New("el efectivo publicado no puede ser menor que lo ya reservado")

// Tienda is one store as the detail of the contract (6.2) needs it.
type Tienda struct {
	ID            string
	Nombre        string
	Direccion     string
	Lat           float64
	Lon           float64
	Nivel         string
	WalletAddress string
	EfectivoLibre int64
}

// Tienda returns one store and the cash it still has free that day.
func (s *Store) Tienda(ctx context.Context, id string, fecha time.Time) (Tienda, error) {
	const query = `
		select t.id,
		       t.nombre,
		       t.direccion,
		       ST_Y(t.ubicacion::geometry),
		       ST_X(t.ubicacion::geometry),
		       t.nivel,
		       t.wallet_address,
		       coalesce(e.publicado_cop - e.reservado_cop, 0)
		from tiendas t
		left join efectivo_disponible e
		       on e.tienda_id = t.id and e.fecha = $2::date
		where t.id = $1`

	var store Tienda
	err := s.pool.QueryRow(ctx, query, id, fecha).Scan(
		&store.ID,
		&store.Nombre,
		&store.Direccion,
		&store.Lat,
		&store.Lon,
		&store.Nivel,
		&store.WalletAddress,
		&store.EfectivoLibre,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tienda{}, fmt.Errorf("store: %w: %s", ErrTiendaNoEncontrada, id)
	}
	if err != nil {
		return Tienda{}, fmt.Errorf("store: reading the store %s: %w", id, err)
	}

	return store, nil
}

// PublicarEfectivo publishes the cash of one store for one day and returns what is
// still free (contract 6.3).
//
// Publishing less than what is already reserved is refused by the database itself,
// and that refusal comes back as ErrEfectivoMenorQueReservado so the route can
// answer with the contract's code instead of a server error.
func (s *Store) PublicarEfectivo(ctx context.Context, tiendaID string, montoCOP int64, fecha time.Time) (int64, error) {
	if montoCOP < 0 {
		return 0, fmt.Errorf("store: montoCOP=%d, and cash is not negative", montoCOP)
	}

	const query = `
		insert into efectivo_disponible (tienda_id, fecha, publicado_cop, reservado_cop)
		values ($1, $2::date, $3, 0)
		on conflict (tienda_id, fecha) do update set publicado_cop = excluded.publicado_cop
		returning publicado_cop - reservado_cop`

	var libre int64
	if err := s.pool.QueryRow(ctx, query, tiendaID, fecha, montoCOP).Scan(&libre); err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) {
			switch databaseError.Code {
			case pgForeignKeyViolation:
				return 0, fmt.Errorf("store: %w: %s", ErrTiendaNoEncontrada, tiendaID)
			case pgCheckViolation:
				return 0, fmt.Errorf("store: %w: %s", ErrEfectivoMenorQueReservado, tiendaID)
			}
		}
		return 0, fmt.Errorf("store: publishing the cash of %s: %w", tiendaID, err)
	}

	return libre, nil
}

// The PostgreSQL error codes this package reads: a foreign key that points at
// nothing is a store that does not exist, a check that fails here is a rule of the
// contract, and a unique that fails is something that was already registered.
const (
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
	pgUniqueViolation     = "23505"
)
