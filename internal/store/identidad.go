package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrYaRegistrada says the phone number already has a person, which the contract
// answers as YA_REGISTRADA.
var ErrYaRegistrada = errors.New("ese whatsapp ya esta registrado")

// Persona is one person as the contract exposes her (5.1 and 5.4). The cedula is
// stored complete and the route masks it: the rule of the contract is about what is
// SHOWN.
type Persona struct {
	ID                    string
	Whatsapp              string
	Cedula                string
	Nombre                string
	WalletAddress         string
	Nivel                 int
	EstadoRegistro        string
	VerificacionSessionID *string
	VerificadoHasta       *time.Time
}

// Elegibilidad answers the three questions of the contract's 7.1, which a
// reservation has to pass before it takes a store's cash.
//
// The state travels as it is stored and not as a boolean, because the route needs to
// tell "blocked, rejected or unfinished" from "approved but without a current
// verification": the contract gives each one its own code and its own sentence.
type Elegibilidad struct {
	Existe              bool
	EstadoRegistro      string
	PoliticasAceptadas  bool
	VerificacionVigente bool
}

// ElegibilidadDe reads the eligibility of one person: her registration state, whether
// she accepted the current policies, and whether her verification is still inside its
// minutes.
func (s *Store) ElegibilidadDe(ctx context.Context, personaID, versionPoliticas string, ahora time.Time) (Elegibilidad, error) {
	const query = `
		select p.estado_registro,
		       p.verificado_hasta,
		       exists (
		           select 1
		           from aceptaciones_politicas a
		           where a.whatsapp = p.whatsapp and a.version = $2
		       )
		from personas p
		where p.id = $1`

	var (
		estado          string
		verificadoHasta *time.Time
		politicas       bool
	)
	err := s.pool.QueryRow(ctx, query, personaID, versionPoliticas).Scan(&estado, &verificadoHasta, &politicas)
	if errors.Is(err, pgx.ErrNoRows) {
		return Elegibilidad{Existe: false}, nil
	}
	if err != nil {
		return Elegibilidad{}, fmt.Errorf("store: reading the eligibility of %s: %w", personaID, err)
	}

	elegibilidad := Elegibilidad{
		Existe:              true,
		EstadoRegistro:      estado,
		PoliticasAceptadas:  politicas,
		VerificacionVigente: verificadoHasta != nil && verificadoHasta.After(ahora),
	}

	return elegibilidad, nil
}

// PersonaPorID returns one person and says whether she exists.
func (s *Store) PersonaPorID(ctx context.Context, id string) (Persona, bool, error) {
	return s.persona(ctx, "p.id = $1", id)
}

// PersonaPorWhatsapp returns the person that number belongs to, which is what the
// contract's 5.1 asks first: it is how the agent knows who is writing.
func (s *Store) PersonaPorWhatsapp(ctx context.Context, whatsapp string) (Persona, bool, error) {
	return s.persona(ctx, "p.whatsapp = $1", whatsapp)
}

func (s *Store) persona(ctx context.Context, condition, value string) (Persona, bool, error) {
	query := `
		select p.id, p.whatsapp, p.cedula, p.nombre, p.wallet_address, p.nivel,
		       p.estado_registro, p.verificacion_session_id, p.verificado_hasta
		from personas p
		where ` + condition

	var persona Persona
	err := s.pool.QueryRow(ctx, query, value).Scan(
		&persona.ID,
		&persona.Whatsapp,
		&persona.Cedula,
		&persona.Nombre,
		&persona.WalletAddress,
		&persona.Nivel,
		&persona.EstadoRegistro,
		&persona.VerificacionSessionID,
		&persona.VerificadoHasta,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Persona{}, false, nil
	}
	if err != nil {
		return Persona{}, false, fmt.Errorf("store: reading the persona %s: %w", value, err)
	}

	return persona, true, nil
}

// Tendero is the storekeeper of one store, which is the other shape of the contract's
// 5.1: the same phone number can be the store's and not a person's.
type Tendero struct {
	TiendaID      string
	NombreTienda  string
	Nombre        string
	EfectivoLibre int64
}

// TenderoPorWhatsapp returns the store of that storekeeper's number and the cash it
// still has free that day.
func (s *Store) TenderoPorWhatsapp(ctx context.Context, whatsapp string, fecha time.Time) (Tendero, bool, error) {
	const query = `
		select t.id, t.nombre, t.nombre_tendero,
		       coalesce(e.publicado_cop - e.reservado_cop, 0)
		from tiendas t
		left join efectivo_disponible e
		       on e.tienda_id = t.id and e.fecha = $2::date
		where t.whatsapp_tendero = $1
		order by t.id
		limit 1`

	var tendero Tendero
	err := s.pool.QueryRow(ctx, query, whatsapp, fecha).Scan(
		&tendero.TiendaID,
		&tendero.NombreTienda,
		&tendero.Nombre,
		&tendero.EfectivoLibre,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tendero{}, false, nil
	}
	if err != nil {
		return Tendero{}, false, fmt.Errorf("store: reading the tendero of %s: %w", whatsapp, err)
	}

	return tendero, true, nil
}

// PoliticasAceptadas says whether that number accepted that version of the policies.
//
// It is asked by the NUMBER and not by the person, because the acceptance comes BEFORE
// the registration: that is the order the contract fixes, and it is why the table is
// keyed by the phone number and not by an identifier that does not exist yet.
func (s *Store) PoliticasAceptadas(ctx context.Context, whatsapp, versionPoliticas string) (bool, error) {
	var aceptadas bool
	err := s.pool.QueryRow(ctx, `
		select exists (
		    select 1 from aceptaciones_politicas
		    where whatsapp = $1 and version = $2
		)`, whatsapp, versionPoliticas).Scan(&aceptadas)
	if err != nil {
		return false, fmt.Errorf("store: reading the acceptance of the policies by %s: %w", whatsapp, err)
	}

	return aceptadas, nil
}

// CrearPersona registers one person. The phone number is unique in the schema, so a
// second registration of the same number comes back as ErrYaRegistrada instead of a
// database error the route would have to read.
func (s *Store) CrearPersona(ctx context.Context, persona Persona) error {
	const query = `
		insert into personas (id, whatsapp, cedula, nombre, wallet_address, nivel, estado_registro)
		values ($1, $2, $3, $4, $5, $6, $7)`

	_, err := s.pool.Exec(ctx, query,
		persona.ID,
		persona.Whatsapp,
		persona.Cedula,
		persona.Nombre,
		persona.WalletAddress,
		persona.Nivel,
		persona.EstadoRegistro,
	)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == pgUniqueViolation {
			return fmt.Errorf("store: %w: %s", ErrYaRegistrada, persona.Whatsapp)
		}

		return fmt.Errorf("store: registering the persona %s: %w", persona.ID, err)
	}

	return nil
}

// AceptarPoliticas records that a number accepted a version of the policies.
//
// It inserts once per number and version: accepting the same version twice adds
// nothing to the trail, and the table does not allow changing or deleting what is
// already there.
func (s *Store) AceptarPoliticas(ctx context.Context, id, whatsapp, version, textoUsuario string) error {
	const query = `
		insert into aceptaciones_politicas (id, whatsapp, version, texto_usuario)
		select $1, $2, $3, $4
		where not exists (
		    select 1 from aceptaciones_politicas
		    where whatsapp = $2 and version = $3
		)`

	if _, err := s.pool.Exec(ctx, query, id, whatsapp, version, textoUsuario); err != nil {
		return fmt.Errorf("store: recording the acceptance of %s by %s: %w", version, whatsapp, err)
	}

	return nil
}
