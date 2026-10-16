package store

import (
	"context"
	"fmt"
	"time"
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
