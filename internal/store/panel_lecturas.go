package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// diaColombia is the SQL of "today" in the demo's zone, the day the cash belongs to.
const diaColombia = `(now() at time zone 'America/Bogota')::date`

// RetiroPanel is one withdrawal as the panel lists it.
type RetiroPanel struct {
	ID                    string
	Estado                string
	TiendaID              string
	NombreTienda          string
	PersonaID             string
	NombreCliente         string
	MontoCOP              int64
	ComisionTenderoCOP    int64
	ComisionPlataformaCOP int64
	RiesgoOK              bool
	FaceMatchOK           bool
	CreadaEn              time.Time
	VenceEn               time.Time
	PagadaEn              *time.Time
	EntregadoEn           *time.Time
	MotivoCancelacion     *string
}

// RetirosPanel lists the withdrawals, newest first.
func (s *Store) RetirosPanel(ctx context.Context, estado, texto string) ([]RetiroPanel, error) {
	rows, err := s.pool.Query(ctx, `
		select r.id, r.estado, t.id, t.nombre, p.id, p.nombre, r.monto_cop, r.comision_tendero_cop,
		       r.comision_plataforma_cop, r.riesgo_ok, r.face_match_ok, r.creada_en, r.vence_en, r.pagada_en,
		       r.entregado_en, r.motivo_cancelacion
		from reservas r join tiendas t on t.id = r.tienda_id join personas p on p.id = r.persona_id
		where ($1 = '' or r.estado = $1)
		  and ($2 = '' or r.id = $2 or p.nombre ilike '%' || $2 || '%' or t.nombre ilike '%' || $2 || '%')
		order by r.creada_en desc
		limit 200`, estado, texto)
	if err != nil {
		return nil, fmt.Errorf("store: listing the withdrawals: %w", err)
	}
	defer rows.Close()

	retiros := []RetiroPanel{}
	for rows.Next() {
		var r RetiroPanel
		if err := rows.Scan(&r.ID, &r.Estado, &r.TiendaID, &r.NombreTienda, &r.PersonaID, &r.NombreCliente, &r.MontoCOP,
			&r.ComisionTenderoCOP, &r.ComisionPlataformaCOP, &r.RiesgoOK, &r.FaceMatchOK, &r.CreadaEn, &r.VenceEn,
			&r.PagadaEn, &r.EntregadoEn, &r.MotivoCancelacion); err != nil {
			return nil, fmt.Errorf("store: reading a withdrawal: %w", err)
		}
		retiros = append(retiros, r)
	}

	return retiros, rows.Err()
}

// EventoRetiro is one thing that happened to a withdrawal, from any of the tables
// that record it.
type EventoRetiro struct {
	Ts      time.Time
	Tipo    string
	Actor   string
	Detalle string
}

// RetiroExtra is what the detail of a withdrawal needs beyond ReservaRetiro.
type RetiroExtra struct {
	EntregadoEn       *time.Time
	MotivoCancelacion *string
	AprobacionID      *string
	AprobacionTexto   *string
	SesionDidit       *string
	Eventos           []EventoRetiro
}

// RetiroExtraDe reads the trail of one withdrawal: the approval, the Didit session that
// verified the person before it, and the events of every table that records it.
func (s *Store) RetiroExtraDe(ctx context.Context, reservaID string) (RetiroExtra, error) {
	var extra RetiroExtra
	err := s.pool.QueryRow(ctx, `
		select r.entregado_en, r.motivo_cancelacion,
		       (select a.id from aprobaciones_pago a where a.reserva_id = r.id order by a.aprobada_en desc limit 1),
		       (select a.texto_usuario from aprobaciones_pago a where a.reserva_id = r.id order by a.aprobada_en desc limit 1),
		       (select v.id from sesiones_verificacion v
		         where v.persona_id = r.persona_id and v.aprobada and v.creada_en <= r.creada_en
		         order by v.creada_en desc limit 1)
		from reservas r where r.id = $1`, reservaID).Scan(&extra.EntregadoEn, &extra.MotivoCancelacion,
		&extra.AprobacionID, &extra.AprobacionTexto, &extra.SesionDidit)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetiroExtra{}, nil
	}
	if err != nil {
		return RetiroExtra{}, fmt.Errorf("store: reading the trail of %s: %w", reservaID, err)
	}

	rows, err := s.pool.Query(ctx, `
		select ts, tipo, actor, detalle from (
			select r.creada_en as ts, 'reserva' as tipo, 'agente' as actor,
			       'Reserva de ' || r.monto_cop || ' COP en ' || t.nombre as detalle
			from reservas r join tiendas t on t.id = r.tienda_id where r.id = $1
			union all
			select a.aprobada_en, 'aprobacion', 'cliente', 'Escribió «' || a.texto_usuario || '» por ' || a.canal
			from aprobaciones_pago a where a.reserva_id = $1
			union all
			select c.confirmada_en, 'confirmacion_' || c.parte, c.parte,
			       case when c.recibido then 'Confirmó la entrega' else 'Dijo que no recibió: ' || coalesce(c.texto_usuario, '') end
			from confirmaciones_entrega c where c.reserva_id = $1
			union all
			select m.creado_en, m.tipo, 'caja_de_barrio', m.wallet_address || ': ' || m.monto || ' COP'
			from movimientos m where m.referencia = $1
			union all
			select x.ts, x.accion, x.actor, coalesce(x.motivo, '')
			from auditoria x where x.entidad = 'reserva' and x.entidad_id = $1
		) e
		order by ts`, reservaID)
	if err != nil {
		return RetiroExtra{}, fmt.Errorf("store: reading the events of %s: %w", reservaID, err)
	}
	defer rows.Close()

	extra.Eventos = []EventoRetiro{}
	for rows.Next() {
		var e EventoRetiro
		if err := rows.Scan(&e.Ts, &e.Tipo, &e.Actor, &e.Detalle); err != nil {
			return RetiroExtra{}, fmt.Errorf("store: reading an event of %s: %w", reservaID, err)
		}
		extra.Eventos = append(extra.Eventos, e)
	}

	return extra, rows.Err()
}

// BilleteraPanel is one virtual wallet and its owner.
type BilleteraPanel struct {
	WalletAddress      string
	Tipo               string
	DuenioID           string
	Nombre             string
	SaldoCOP           int64
	UltimoMovimientoEn *time.Time
}

// SaldosPanel lists every virtual wallet: people, stores and Caja de Barrio's own.
func (s *Store) SaldosPanel(ctx context.Context) ([]BilleteraPanel, error) {
	rows, err := s.pool.Query(ctx, `
		with duenios as (
			select wallet_address, 'persona' as tipo, id, nombre from personas where wallet_address <> ''
			union all
			select wallet_address, 'tienda', id, nombre from tiendas where wallet_address <> ''
		),
		otras as (
			select distinct m.wallet_address, 'plataforma' as tipo, '' as id, 'Caja de Barrio' as nombre
			from movimientos m where not exists (select 1 from duenios d where d.wallet_address = m.wallet_address)
		)
		select w.wallet_address, w.tipo, w.id, w.nombre,
		       coalesce(sum(m.monto), 0)::bigint, max(m.creado_en)
		from (select * from duenios union all select * from otras) w
		left join movimientos m on m.wallet_address = w.wallet_address
		group by w.wallet_address, w.tipo, w.id, w.nombre
		order by max(m.creado_en) desc nulls last, w.nombre`)
	if err != nil {
		return nil, fmt.Errorf("store: listing the wallets: %w", err)
	}
	defer rows.Close()

	billeteras := []BilleteraPanel{}
	for rows.Next() {
		var b BilleteraPanel
		if err := rows.Scan(&b.WalletAddress, &b.Tipo, &b.DuenioID, &b.Nombre, &b.SaldoCOP, &b.UltimoMovimientoEn); err != nil {
			return nil, fmt.Errorf("store: reading a wallet: %w", err)
		}
		billeteras = append(billeteras, b)
	}

	return billeteras, rows.Err()
}

// AlertaPanel is one alert as the panel lists it.
type AlertaPanel struct {
	ID            string
	Regla         string
	Detalle       string
	Estado        string
	PersonaID     *string
	NombrePersona *string
	TiendaID      *string
	NombreTienda  *string
	CreadaEn      time.Time
	Resolucion    *string
	ResueltaEn    *time.Time
}

// AlertasPanel lists the alerts, newest first. estado is abierta, resuelta or empty.
func (s *Store) AlertasPanel(ctx context.Context, estado, regla string) ([]AlertaPanel, error) {
	rows, err := s.pool.Query(ctx, `
		select a.id, a.regla, a.detalle, a.estado, a.persona_id, p.nombre, a.tienda_id, t.nombre, a.creada_en,
		       a.resolucion, a.resuelta_en
		from alertas a left join personas p on p.id = a.persona_id left join tiendas t on t.id = a.tienda_id
		where ($1 = '' or a.estado = $1) and ($2 = '' or a.regla = $2)
		order by a.creada_en desc
		limit 200`, estado, regla)
	if err != nil {
		return nil, fmt.Errorf("store: listing the alerts: %w", err)
	}
	defer rows.Close()

	alertas := []AlertaPanel{}
	for rows.Next() {
		var a AlertaPanel
		if err := rows.Scan(&a.ID, &a.Regla, &a.Detalle, &a.Estado, &a.PersonaID, &a.NombrePersona, &a.TiendaID,
			&a.NombreTienda, &a.CreadaEn, &a.Resolucion, &a.ResueltaEn); err != nil {
			return nil, fmt.Errorf("store: reading an alert: %w", err)
		}
		alertas = append(alertas, a)
	}

	return alertas, rows.Err()
}

// ResolverAlerta closes an open alert with what was done, and says whether it did.
func (s *Store) ResolverAlerta(ctx context.Context, id, resolucion string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		update alertas set estado = 'resuelta', resolucion = $2, resuelta_en = now()
		where id = $1 and estado = 'abierta'`, id, resolucion)
	if err != nil {
		return false, fmt.Errorf("store: resolving the alert %s: %w", id, err)
	}

	return tag.RowsAffected() == 1, nil
}

// Resumen are the numbers of the panel's home page, for today in Colombia.
type Resumen struct {
	RetirosEntregadosHoy int64
	MontoEntregadoHoy    int64
	RegistrosPorRevisar  int64
	AlertasAbiertas      int64
	TiendasActivas       int64
	TiendasTotal         int64
	EfectivoLibre        int64
	PorHora              map[string]int64
}

// ResumenPanel reads the home page numbers.
func (s *Store) ResumenPanel(ctx context.Context) (Resumen, error) {
	var (
		r       Resumen
		porHora string
	)
	err := s.pool.QueryRow(ctx, `
		select
		  (select count(*) from reservas where estado = 'entregada' and (entregado_en at time zone 'America/Bogota')::date = `+diaColombia+`),
		  (select coalesce(sum(monto_cop), 0) from reservas where estado = 'entregada' and (entregado_en at time zone 'America/Bogota')::date = `+diaColombia+`),
		  (select count(*) from personas where estado_registro = 'revision_manual'),
		  (select count(*) from alertas where estado = 'abierta'),
		  (select count(*) from tiendas where estado = 'activa'),
		  (select count(*) from tiendas),
		  (select coalesce(sum(e.publicado_cop - e.reservado_cop), 0) from efectivo_disponible e
		     join tiendas t on t.id = e.tienda_id where t.estado = 'activa' and e.fecha = `+diaColombia+`),
		  (select coalesce(json_object_agg(h, n), '{}'::json)::text from (
		     select to_char(entregado_en at time zone 'America/Bogota', 'HH24') as h, count(*) as n
		     from reservas where estado = 'entregada' and (entregado_en at time zone 'America/Bogota')::date = `+diaColombia+`
		     group by 1) x)`).Scan(&r.RetirosEntregadosHoy, &r.MontoEntregadoHoy, &r.RegistrosPorRevisar,
		&r.AlertasAbiertas, &r.TiendasActivas, &r.TiendasTotal, &r.EfectivoLibre, &porHora)
	if err != nil {
		return Resumen{}, fmt.Errorf("store: reading the summary: %w", err)
	}
	r.PorHora = map[string]int64{}
	if err := json.Unmarshal([]byte(porHora), &r.PorHora); err != nil {
		return Resumen{}, fmt.Errorf("store: reading the withdrawals by hour: %w", err)
	}

	return r, nil
}

// EntradaAuditoria is one line of the audit trail.
type EntradaAuditoria struct {
	ID        string
	Ts        time.Time
	Actor     string
	Accion    string
	Entidad   string
	EntidadID string
	Motivo    *string
	Detalle   []byte
}

// AuditoriaPanel lists the audit trail, newest first.
func (s *Store) AuditoriaPanel(ctx context.Context, actor, accion, entidad, texto string) ([]EntradaAuditoria, error) {
	rows, err := s.pool.Query(ctx, `
		select id, ts, actor, accion, entidad, entidad_id, motivo, detalle
		from auditoria
		where ($1 = '' or actor = $1) and ($2 = '' or accion = $2) and ($3 = '' or entidad = $3)
		  and ($4 = '' or entidad_id = $4 or coalesce(motivo, '') ilike '%' || $4 || '%')
		order by ts desc
		limit 300`, actor, accion, entidad, texto)
	if err != nil {
		return nil, fmt.Errorf("store: listing the audit trail: %w", err)
	}
	defer rows.Close()

	entradas := []EntradaAuditoria{}
	for rows.Next() {
		var e EntradaAuditoria
		if err := rows.Scan(&e.ID, &e.Ts, &e.Actor, &e.Accion, &e.Entidad, &e.EntidadID, &e.Motivo, &e.Detalle); err != nil {
			return nil, fmt.Errorf("store: reading an audit line: %w", err)
		}
		entradas = append(entradas, e)
	}

	return entradas, rows.Err()
}
