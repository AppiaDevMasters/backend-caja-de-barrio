package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrTenderoExistente is returned when a number is already the storekeeper of a store.
var ErrTenderoExistente = errors.New("ese whatsapp ya es tendero de una tienda")

// TiendaNueva is a store the panel registers, pending its storekeeper's verification.
type TiendaNueva struct {
	ID            string
	Nombre        string
	Direccion     string
	Lat           float64
	Lon           float64
	Nivel         string
	WalletAddress string
	Whatsapp      string
	NombreTendero string
}

// CrearTiendaPendiente inserts a store in pendiente_verificacion. A number can keep one
// store only.
func (s *Store) CrearTiendaPendiente(ctx context.Context, tienda TiendaNueva) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: opening the store registration: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	// The lock serializes two registrations of the same number.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext($1))`, tienda.Whatsapp); err != nil {
		return fmt.Errorf("store: locking the storekeeper number: %w", err)
	}
	var existe bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from tiendas where whatsapp_tendero = $1)`,
		tienda.Whatsapp).Scan(&existe); err != nil {
		return fmt.Errorf("store: checking the storekeeper number: %w", err)
	}
	if existe {
		return ErrTenderoExistente
	}

	const query = `
		insert into tiendas (id, nombre, direccion, ubicacion, nivel, estado, wallet_address, whatsapp_tendero, nombre_tendero)
		values ($1, $2, $3, ST_SetSRID(ST_MakePoint($4, $5), 4326)::geography, $6, 'pendiente_verificacion', $7, $8, $9)`
	if _, err := tx.Exec(ctx, query, tienda.ID, tienda.Nombre, tienda.Direccion, tienda.Lon, tienda.Lat, tienda.Nivel,
		tienda.WalletAddress, tienda.Whatsapp, tienda.NombreTendero); err != nil {
		return fmt.Errorf("store: creating the store %s: %w", tienda.ID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: committing the store %s: %w", tienda.ID, err)
	}

	return nil
}

// TiendaRegistro is what the storekeeper's verification needs of a store.
type TiendaRegistro struct {
	ID              string
	Nombre          string
	Estado          string
	WalletAddress   string
	WhatsappTendero string
	NombreTendero   string
}

// TiendaRegistroPorID reads one store and says whether it exists.
func (s *Store) TiendaRegistroPorID(ctx context.Context, id string) (TiendaRegistro, bool, error) {
	const query = `
		select id, nombre, estado, wallet_address, whatsapp_tendero, nombre_tendero
		from tiendas where id = $1`

	var t TiendaRegistro
	err := s.pool.QueryRow(ctx, query, id).Scan(&t.ID, &t.Nombre, &t.Estado, &t.WalletAddress, &t.WhatsappTendero, &t.NombreTendero)
	if errors.Is(err, pgx.ErrNoRows) {
		return TiendaRegistro{}, false, nil
	}
	if err != nil {
		return TiendaRegistro{}, false, fmt.Errorf("store: reading the store %s: %w", id, err)
	}

	return t, true, nil
}

// CrearSesionVerificacionTienda records a Didit session of a store's keeper.
func (s *Store) CrearSesionVerificacionTienda(ctx context.Context, id, tiendaID string) error {
	const query = `insert into sesiones_verificacion (id, tienda_id, motivo) values ($1, $2, 'alta_tendero')`
	if _, err := s.pool.Exec(ctx, query, id, tiendaID); err != nil {
		return fmt.Errorf("store: creating the storekeeper session %s: %w", id, err)
	}

	return nil
}

// ResolverAltaTendero writes the outcome of the storekeeper's verification, only while
// the store is still waiting for it.
func (s *Store) ResolverAltaTendero(ctx context.Context, tiendaID, estado, cedula, motivoRevision string) error {
	const query = `
		update tiendas
		set estado = $2,
		    cedula_tendero = coalesce(nullif($3, ''), cedula_tendero),
		    motivo_revision = nullif($4, '')
		where id = $1 and estado = 'pendiente_verificacion'`
	if _, err := s.pool.Exec(ctx, query, tiendaID, estado, cedula, motivoRevision); err != nil {
		return fmt.Errorf("store: resolving the storekeeper of %s: %w", tiendaID, err)
	}

	return nil
}

// CambiarEstadoTienda moves a store to a new state only from one of the states given,
// and says whether it moved it.
func (s *Store) CambiarEstadoTienda(ctx context.Context, id string, desde []string, a string) (bool, error) {
	const query = `
		update tiendas set estado = $3,
		       motivo_revision = case when $3 = 'activa' then null else motivo_revision end
		where id = $1 and estado = any($2)`
	tag, err := s.pool.Exec(ctx, query, id, desde, a)
	if err != nil {
		return false, fmt.Errorf("store: changing the state of the store %s: %w", id, err)
	}

	return tag.RowsAffected() == 1, nil
}

// CrearAlertaTienda records one rule that fired about a store.
func (s *Store) CrearAlertaTienda(ctx context.Context, id, tiendaID, regla, detalle string) error {
	const query = `insert into alertas (id, tienda_id, regla, detalle) values ($1, $2, $3, $4)`
	if _, err := s.pool.Exec(ctx, query, id, tiendaID, regla, detalle); err != nil {
		return fmt.Errorf("store: creating the alert %s: %w", id, err)
	}

	return nil
}

// TiendaPanel is a store as the panel lists and opens it.
type TiendaPanel struct {
	ID                string
	Nombre            string
	Direccion         string
	Lat               float64
	Lon               float64
	Nivel             string
	Estado            string
	WalletAddress     string
	WhatsappTendero   string
	NombreTendero     string
	CedulaTendero     *string
	MotivoRevision    *string
	CreadaEn          time.Time
	Reputacion        float64
	EfectivoPublicado int64
	EfectivoReservado int64
	SaldoCOP          int64

	DiditSessionID *string
	DiditAprobada  *bool
	DiditScore     *float64
	DiditEn        *time.Time
}

const tiendaPanelSelect = `
	select t.id, t.nombre, t.direccion, ST_Y(t.ubicacion::geometry), ST_X(t.ubicacion::geometry), t.nivel, t.estado,
	       t.wallet_address, t.whatsapp_tendero, t.nombre_tendero, t.cedula_tendero, t.motivo_revision, t.creada_en,
	       t.reputacion::float8, coalesce(e.publicado_cop, 0), coalesce(e.reservado_cop, 0),
	       coalesce((select sum(m.monto) from movimientos m where m.wallet_address = t.wallet_address), 0)::bigint,
	       s.id, s.aprobada, s.score::float8, coalesce(s.resuelta_en, s.creada_en)
	from tiendas t
	left join efectivo_disponible e
	       on e.tienda_id = t.id and e.fecha = (now() at time zone 'America/Bogota')::date
	left join lateral (
		select id, aprobada, score, creada_en, resuelta_en from sesiones_verificacion
		where tienda_id = t.id order by creada_en desc limit 1
	) s on true`

func scanTiendaPanel(row pgx.Row) (TiendaPanel, error) {
	var t TiendaPanel
	err := row.Scan(&t.ID, &t.Nombre, &t.Direccion, &t.Lat, &t.Lon, &t.Nivel, &t.Estado, &t.WalletAddress,
		&t.WhatsappTendero, &t.NombreTendero, &t.CedulaTendero, &t.MotivoRevision, &t.CreadaEn, &t.Reputacion,
		&t.EfectivoPublicado, &t.EfectivoReservado, &t.SaldoCOP, &t.DiditSessionID, &t.DiditAprobada, &t.DiditScore, &t.DiditEn)

	return t, err
}

// TiendasPanel lists the stores, newest first, filtered by state and by a text that
// matches the name, the storekeeper or the id.
func (s *Store) TiendasPanel(ctx context.Context, estado, texto string) ([]TiendaPanel, error) {
	query := tiendaPanelSelect + `
		where ($1 = '' or t.estado = $1)
		  and ($2 = '' or t.nombre ilike '%' || $2 || '%' or t.nombre_tendero ilike '%' || $2 || '%' or t.id = $2)
		order by t.creada_en desc
		limit 200`
	rows, err := s.pool.Query(ctx, query, estado, texto)
	if err != nil {
		return nil, fmt.Errorf("store: listing the stores: %w", err)
	}
	defer rows.Close()

	tiendas := []TiendaPanel{}
	for rows.Next() {
		tienda, err := scanTiendaPanel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading a store of the list: %w", err)
		}
		tiendas = append(tiendas, tienda)
	}

	return tiendas, rows.Err()
}

// TiendaPanelPorID opens one store and says whether it exists.
func (s *Store) TiendaPanelPorID(ctx context.Context, id string) (TiendaPanel, bool, error) {
	tienda, err := scanTiendaPanel(s.pool.QueryRow(ctx, tiendaPanelSelect+` where t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return TiendaPanel{}, false, nil
	}
	if err != nil {
		return TiendaPanel{}, false, fmt.Errorf("store: reading the store %s: %w", id, err)
	}

	return tienda, true, nil
}
