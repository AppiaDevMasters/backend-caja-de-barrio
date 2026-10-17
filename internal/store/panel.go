package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// UsuarioPanel is one operator of the control panel.
type UsuarioPanel struct {
	Username  string
	Nombre    string
	Rol       string
	ClaveHash string
}

// GuardarUsuarioPanel creates or updates one operator. The backend calls it at start-up
// with the passwords of deploy/llaves.env, already hashed.
func (s *Store) GuardarUsuarioPanel(ctx context.Context, usuario UsuarioPanel) error {
	const query = `
		insert into usuarios_panel (username, nombre, rol, clave_hash)
		values ($1, $2, $3, $4)
		on conflict (username) do update set nombre = $2, rol = $3, clave_hash = $4`
	if _, err := s.pool.Exec(ctx, query, usuario.Username, usuario.Nombre, usuario.Rol, usuario.ClaveHash); err != nil {
		return fmt.Errorf("store: saving the panel user %s: %w", usuario.Username, err)
	}

	return nil
}

// UsuarioPanelPorUsername returns one operator and says whether it exists.
func (s *Store) UsuarioPanelPorUsername(ctx context.Context, username string) (UsuarioPanel, bool, error) {
	const query = `select username, nombre, rol, clave_hash from usuarios_panel where username = $1`

	var usuario UsuarioPanel
	err := s.pool.QueryRow(ctx, query, username).Scan(&usuario.Username, &usuario.Nombre, &usuario.Rol, &usuario.ClaveHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return UsuarioPanel{}, false, nil
	}
	if err != nil {
		return UsuarioPanel{}, false, fmt.Errorf("store: reading the panel user %s: %w", username, err)
	}

	return usuario, true, nil
}

// Auditoria is one write of the panel.
type Auditoria struct {
	ID        string
	Actor     string
	Accion    string
	Entidad   string
	EntidadID string
	Motivo    string
	Detalle   map[string]any
}

// Auditar records one write of the panel.
func (s *Store) Auditar(ctx context.Context, entrada Auditoria) error {
	detalle, err := json.Marshal(entrada.Detalle)
	if err != nil || entrada.Detalle == nil {
		detalle = []byte("{}")
	}

	const query = `
		insert into auditoria (id, actor, accion, entidad, entidad_id, motivo, detalle)
		values ($1, $2, $3, $4, $5, nullif($6, ''), $7)`
	if _, err := s.pool.Exec(ctx, query, entrada.ID, entrada.Actor, entrada.Accion, entrada.Entidad,
		entrada.EntidadID, entrada.Motivo, detalle); err != nil {
		return fmt.Errorf("store: recording the audit entry %s: %w", entrada.ID, err)
	}

	return nil
}

// EntradaHistorial is one line of the history of an entity, as the panel shows it.
type EntradaHistorial struct {
	Ts     time.Time
	Actor  string
	Accion string
	Motivo *string
}

// HistorialDe returns the audit trail of one entity, newest first.
func (s *Store) HistorialDe(ctx context.Context, entidad, entidadID string) ([]EntradaHistorial, error) {
	const query = `
		select ts, actor, accion, motivo from auditoria
		where entidad = $1 and entidad_id = $2
		order by ts desc
		limit 50`
	rows, err := s.pool.Query(ctx, query, entidad, entidadID)
	if err != nil {
		return nil, fmt.Errorf("store: reading the history of %s %s: %w", entidad, entidadID, err)
	}
	defer rows.Close()

	historial := []EntradaHistorial{}
	for rows.Next() {
		var entrada EntradaHistorial
		if err := rows.Scan(&entrada.Ts, &entrada.Actor, &entrada.Accion, &entrada.Motivo); err != nil {
			return nil, fmt.Errorf("store: reading a history line: %w", err)
		}
		historial = append(historial, entrada)
	}

	return historial, rows.Err()
}

// PersonaPanel is a person as the panel lists and opens her.
type PersonaPanel struct {
	ID             string
	Nombre         string
	Cedula         string
	Whatsapp       string
	EstadoRegistro string
	WalletAddress  string
	MotivoRevision *string
	CreadaEn       time.Time
	SaldoCOP       int64

	// The last Didit session, when there is one.
	DiditSessionID *string
	DiditAprobada  *bool
	DiditScore     *float64
	DiditEn        *time.Time

	PoliticasVersion *string
	PoliticasEn      *time.Time
}

const personaPanelColumnas = `
	p.id, p.nombre, p.cedula, p.whatsapp, p.estado_registro, p.wallet_address, p.motivo_revision, p.creada_en,
	coalesce((select sum(m.monto) from movimientos m where m.wallet_address = p.wallet_address and p.wallet_address <> ''), 0)::bigint,
	s.id, s.aprobada, s.score::float8, coalesce(s.resuelta_en, s.creada_en),
	a.version, a.aceptada_en`

const personaPanelDesde = `
	from personas p
	left join lateral (
		select id, aprobada, score, creada_en, resuelta_en from sesiones_verificacion
		where persona_id = p.id order by creada_en desc limit 1
	) s on true
	left join lateral (
		select version, aceptada_en from aceptaciones_politicas
		where whatsapp = p.whatsapp order by aceptada_en desc limit 1
	) a on true`

func scanPersonaPanel(row pgx.Row) (PersonaPanel, error) {
	var p PersonaPanel
	err := row.Scan(&p.ID, &p.Nombre, &p.Cedula, &p.Whatsapp, &p.EstadoRegistro, &p.WalletAddress, &p.MotivoRevision,
		&p.CreadaEn, &p.SaldoCOP, &p.DiditSessionID, &p.DiditAprobada, &p.DiditScore, &p.DiditEn,
		&p.PoliticasVersion, &p.PoliticasEn)

	return p, err
}

// PersonasPanel lists the people, newest first, filtered by state and by a text that
// matches the name, the number or the id. Empty filters list everyone.
func (s *Store) PersonasPanel(ctx context.Context, estado, texto string) ([]PersonaPanel, error) {
	query := `select ` + personaPanelColumnas + personaPanelDesde + `
		where ($1 = '' or p.estado_registro = $1)
		  and ($2 = '' or p.nombre ilike '%' || $2 || '%' or p.whatsapp like '%' || $2 || '%' or p.id = $2)
		order by p.creada_en desc
		limit 200`
	rows, err := s.pool.Query(ctx, query, estado, texto)
	if err != nil {
		return nil, fmt.Errorf("store: listing the people: %w", err)
	}
	defer rows.Close()

	personas := []PersonaPanel{}
	for rows.Next() {
		persona, err := scanPersonaPanel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading a person of the list: %w", err)
		}
		personas = append(personas, persona)
	}

	return personas, rows.Err()
}

// PersonaPanelPorID opens one person and says whether she exists.
func (s *Store) PersonaPanelPorID(ctx context.Context, id string) (PersonaPanel, bool, error) {
	query := `select ` + personaPanelColumnas + personaPanelDesde + ` where p.id = $1`
	persona, err := scanPersonaPanel(s.pool.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return PersonaPanel{}, false, nil
	}
	if err != nil {
		return PersonaPanel{}, false, fmt.Errorf("store: reading the person %s: %w", id, err)
	}

	return persona, true, nil
}

// CambiarEstadoPersona moves a person to a new registration state only from one of the
// states given, and says whether it moved her: two operators deciding at the same
// time cannot both win.
func (s *Store) CambiarEstadoPersona(ctx context.Context, id string, desde []string, a string) (bool, error) {
	const query = `
		update personas set estado_registro = $3,
		       motivo_revision = case when $3 = 'aprobada' then null else motivo_revision end
		where id = $1 and estado_registro = any($2)`
	tag, err := s.pool.Exec(ctx, query, id, desde, a)
	if err != nil {
		return false, fmt.Errorf("store: changing the state of %s: %w", id, err)
	}

	return tag.RowsAffected() == 1, nil
}

// AsignarWallet sets the wallet of a person that has none.
func (s *Store) AsignarWallet(ctx context.Context, personaID, wallet string) error {
	const query = `update personas set wallet_address = $2 where id = $1 and wallet_address = ''`
	if _, err := s.pool.Exec(ctx, query, personaID, wallet); err != nil {
		return fmt.Errorf("store: setting the wallet of %s: %w", personaID, err)
	}

	return nil
}

// AnotarMotivoRevision records why a registration went to manual review.
func (s *Store) AnotarMotivoRevision(ctx context.Context, personaID, motivo string) error {
	const query = `update personas set motivo_revision = $2 where id = $1`
	if _, err := s.pool.Exec(ctx, query, personaID, motivo); err != nil {
		return fmt.Errorf("store: recording the review reason of %s: %w", personaID, err)
	}

	return nil
}

// MovimientosDe returns the balance and the last five movements of one wallet.
func (s *Store) MovimientosDe(ctx context.Context, wallet string) (Billetera, error) {
	billetera := Billetera{WalletAddress: wallet, Movimientos: []Movimiento{}}
	if wallet == "" {
		return billetera, nil
	}

	const saldo = `select coalesce(sum(monto), 0)::bigint from movimientos where wallet_address = $1`
	if err := s.pool.QueryRow(ctx, saldo, wallet).Scan(&billetera.Saldo); err != nil {
		return Billetera{}, fmt.Errorf("store: adding up the balance: %w", err)
	}

	const ultimos = `
		select tipo, monto, creado_en from movimientos
		where wallet_address = $1
		order by creado_en desc
		limit 5`
	rows, err := s.pool.Query(ctx, ultimos, wallet)
	if err != nil {
		return Billetera{}, fmt.Errorf("store: reading the movements: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var movimiento Movimiento
		if err := rows.Scan(&movimiento.Tipo, &movimiento.Monto, &movimiento.En); err != nil {
			return Billetera{}, fmt.Errorf("store: reading a movement: %w", err)
		}
		billetera.Movimientos = append(billetera.Movimientos, movimiento)
	}

	return billetera, rows.Err()
}
