package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SesionVerificacion is one Didit session of the contract's 5.5: who it belongs to,
// why it was opened and, once decided, its verdict. Only the provider's reference is
// kept: the contract forbids storing selfies or document photos.
type SesionVerificacion struct {
	ID             string
	PersonaID      string
	TiendaID       string
	Motivo         string
	DiditSessionID string
	Aprobada       *bool
	Score          *float64
}

// CrearSesionVerificacion records a session before the provider is called, so the
// provider's vendor_data points at a row that already exists.
func (s *Store) CrearSesionVerificacion(ctx context.Context, id, personaID, motivo string) error {
	const query = `insert into sesiones_verificacion (id, persona_id, motivo) values ($1, $2, $3)`
	if _, err := s.pool.Exec(ctx, query, id, personaID, motivo); err != nil {
		return fmt.Errorf("store: creating the verification session %s: %w", id, err)
	}

	return nil
}

// AsignarSesionDidit links our session to the provider's one.
func (s *Store) AsignarSesionDidit(ctx context.Context, id, diditSessionID string) error {
	const query = `update sesiones_verificacion set didit_session_id = $2 where id = $1`
	if _, err := s.pool.Exec(ctx, query, id, diditSessionID); err != nil {
		return fmt.Errorf("store: linking the verification session %s: %w", id, err)
	}

	return nil
}

// SesionVerificacionPorID returns one session and says whether it exists.
func (s *Store) SesionVerificacionPorID(ctx context.Context, id string) (SesionVerificacion, bool, error) {
	const query = `
		select id, coalesce(persona_id, ''), coalesce(tienda_id, ''), motivo, coalesce(didit_session_id, ''),
		       aprobada, score::float8
		from sesiones_verificacion
		where id = $1`

	var sesion SesionVerificacion
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&sesion.ID, &sesion.PersonaID, &sesion.TiendaID, &sesion.Motivo, &sesion.DiditSessionID, &sesion.Aprobada, &sesion.Score,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SesionVerificacion{}, false, nil
	}
	if err != nil {
		return SesionVerificacion{}, false, fmt.Errorf("store: reading the verification session %s: %w", id, err)
	}

	return sesion, true, nil
}

// SesionVerificacionPorDiditID finds our session by the provider's session id.
func (s *Store) SesionVerificacionPorDiditID(ctx context.Context, diditSessionID string) (SesionVerificacion, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `select id from sesiones_verificacion where didit_session_id = $1`, diditSessionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return SesionVerificacion{}, false, nil
	}
	if err != nil {
		return SesionVerificacion{}, false, fmt.Errorf("store: finding the session of Didit %s: %w", diditSessionID, err)
	}

	return s.SesionVerificacionPorID(ctx, id)
}

// UltimaAltaAprobada returns the id of the registration session that approved the
// person: Didit keeps her face under that vendor_data, and every withdrawal's face
// match is made against it.
func (s *Store) UltimaAltaAprobada(ctx context.Context, personaID string) (string, bool, error) {
	const query = `
		select id from sesiones_verificacion
		where persona_id = $1 and motivo = 'alta' and aprobada
		order by resuelta_en desc nulls last
		limit 1`

	var id string
	err := s.pool.QueryRow(ctx, query, personaID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: finding the registration of %s: %w", personaID, err)
	}

	return id, true, nil
}

// ResolverSesionVerificacion records the verdict ONCE. It answers true only to the
// caller that resolved it: the webhook and the query after the browser comes back can
// arrive together, and the effects of a verdict (Verifik, the registration, the
// thirty minutes) must happen a single time.
func (s *Store) ResolverSesionVerificacion(ctx context.Context, id string, aprobada bool, score *float64) (bool, error) {
	const query = `
		update sesiones_verificacion
		set aprobada = $2, score = $3, resuelta_en = now()
		where id = $1 and aprobada is null`

	tag, err := s.pool.Exec(ctx, query, id, aprobada, score)
	if err != nil {
		return false, fmt.Errorf("store: resolving the verification session %s: %w", id, err)
	}

	return tag.RowsAffected() == 1, nil
}

// MarcarVerificacion opens (or closes, with a nil hasta) the window in which the
// person counts as verified: thirty minutes in the contract.
func (s *Store) MarcarVerificacion(ctx context.Context, personaID, sesionID string, hasta *time.Time) error {
	const query = `
		update personas
		set verificacion_session_id = case when $3::timestamptz is null then null else $2 end,
		    verificado_hasta = $3
		where id = $1`
	if _, err := s.pool.Exec(ctx, query, personaID, sesionID, hasta); err != nil {
		return fmt.Errorf("store: marking the verification of %s: %w", personaID, err)
	}

	return nil
}

// CompletarRegistro writes what the registration learns after Didit and Verifik: the
// document, the name the registry holds, the wallet and the new state.
func (s *Store) CompletarRegistro(ctx context.Context, personaID, cedula, nombre, wallet, estado string) error {
	const query = `
		update personas
		set cedula = coalesce(nullif($2, ''), cedula),
		    nombre = coalesce(nullif($3, ''), nombre),
		    wallet_address = coalesce(nullif($4, ''), wallet_address),
		    estado_registro = $5
		where id = $1`
	if _, err := s.pool.Exec(ctx, query, personaID, cedula, nombre, wallet, estado); err != nil {
		return fmt.Errorf("store: completing the registration of %s: %w", personaID, err)
	}

	return nil
}

// CrearAlerta records one rule that fired about a person.
func (s *Store) CrearAlerta(ctx context.Context, id, personaID, regla, detalle string) error {
	const query = `insert into alertas (id, persona_id, regla, detalle) values ($1, $2, $3, $4)`
	if _, err := s.pool.Exec(ctx, query, id, personaID, regla, detalle); err != nil {
		return fmt.Errorf("store: creating the alert %s: %w", id, err)
	}

	return nil
}
