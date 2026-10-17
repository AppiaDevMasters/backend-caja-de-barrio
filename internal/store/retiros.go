package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReservaRetiro is a reservation with everything its payment needs: the store, the
// person, the collection points, the quote and the result of the payment.
type ReservaRetiro struct {
	ID                    string
	Estado                string
	MontoCOP              int64
	ComisionTenderoCOP    int64
	ComisionPlataformaCOP int64
	Codigo                string
	RiesgoOK              bool
	FaceMatchOK           bool
	AprobacionEn          *time.Time
	PagadaEn              *time.Time
	EntregaTenderoEn      *time.Time
	RecibidoClienteEn     *time.Time
	CreadaEn              time.Time
	VenceEn               time.Time

	TiendaID        string
	NombreTienda    string
	WalletTienda    string
	WhatsappTendero string

	PersonaID       string
	NombreCliente   string
	WhatsappCliente string
	WalletCliente   string
	NivelCliente    int
	EstadoCliente   string

	IncomingPaymentURL   *string
	CobroPlataformaURL   *string
	QuoteURL             *string
	QuotePlataformaURL   *string
	OutgoingPaymentID    *string
	OutgoingPlataformaID *string
	Debito               *float64
	Moneda               *string
	QuoteVenceEn         *time.Time
	Resultado            []byte
	LiquidezEn           *time.Time
}

// TotalCOP is what the person pays: the cash plus both commissions.
func (r ReservaRetiro) TotalCOP() int64 {
	return r.MontoCOP + r.ComisionTenderoCOP + r.ComisionPlataformaCOP
}

const reservaRetiroSelect = `
	select r.id, r.estado, r.monto_cop, r.comision_tendero_cop, r.comision_plataforma_cop, r.codigo,
	       r.riesgo_ok, r.face_match_ok, r.aprobacion_en, r.pagada_en, r.entrega_tendero_en, r.recibido_cliente_en,
	       r.creada_en, r.vence_en,
	       t.id, t.nombre, t.wallet_address, t.whatsapp_tendero,
	       p.id, p.nombre, p.whatsapp, p.wallet_address, p.nivel, p.estado_registro,
	       x.incoming_payment_url, x.cobro_plataforma_url, x.quote_url, x.quote_plataforma_url,
	       x.outgoing_payment_id, x.outgoing_plataforma_id, x.debito::float8, x.moneda, x.quote_vence_en,
	       x.resultado, x.liquidez_en
	from reservas r
	join tiendas t on t.id = r.tienda_id
	join personas p on p.id = r.persona_id
	left join retiros x on x.reserva_id = r.id`

func scanReservaRetiro(row pgx.Row) (ReservaRetiro, error) {
	var r ReservaRetiro
	err := row.Scan(&r.ID, &r.Estado, &r.MontoCOP, &r.ComisionTenderoCOP, &r.ComisionPlataformaCOP, &r.Codigo,
		&r.RiesgoOK, &r.FaceMatchOK, &r.AprobacionEn, &r.PagadaEn, &r.EntregaTenderoEn, &r.RecibidoClienteEn,
		&r.CreadaEn, &r.VenceEn,
		&r.TiendaID, &r.NombreTienda, &r.WalletTienda, &r.WhatsappTendero,
		&r.PersonaID, &r.NombreCliente, &r.WhatsappCliente, &r.WalletCliente, &r.NivelCliente, &r.EstadoCliente,
		&r.IncomingPaymentURL, &r.CobroPlataformaURL, &r.QuoteURL, &r.QuotePlataformaURL,
		&r.OutgoingPaymentID, &r.OutgoingPlataformaID, &r.Debito, &r.Moneda, &r.QuoteVenceEn,
		&r.Resultado, &r.LiquidezEn)

	return r, err
}

// ReservaRetiroPorID reads one reservation and says whether it exists.
func (s *Store) ReservaRetiroPorID(ctx context.Context, id string) (ReservaRetiro, bool, error) {
	r, err := scanReservaRetiro(s.pool.QueryRow(ctx, reservaRetiroSelect+` where r.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReservaRetiro{}, false, nil
	}
	if err != nil {
		return ReservaRetiro{}, false, fmt.Errorf("store: reading the reservation %s: %w", id, err)
	}

	return r, true, nil
}

// ReservaRetiroPorCobro finds the reservation of a store's collection point.
func (s *Store) ReservaRetiroPorCobro(ctx context.Context, incomingPaymentURL string) (ReservaRetiro, bool, error) {
	r, err := scanReservaRetiro(s.pool.QueryRow(ctx, reservaRetiroSelect+` where x.incoming_payment_url = $1`, incomingPaymentURL))
	if errors.Is(err, pgx.ErrNoRows) {
		return ReservaRetiro{}, false, nil
	}
	if err != nil {
		return ReservaRetiro{}, false, fmt.Errorf("store: finding the reservation of a collection point: %w", err)
	}

	return r, true, nil
}

// MarcarRiesgo records the risk verdict of a reservation.
func (s *Store) MarcarRiesgo(ctx context.Context, reservaID string, ok bool) error {
	if _, err := s.pool.Exec(ctx, `update reservas set riesgo_ok = $2 where id = $1`, reservaID, ok); err != nil {
		return fmt.Errorf("store: recording the risk of %s: %w", reservaID, err)
	}

	return nil
}

// RetirosPagadosDesde counts the withdrawals a person paid since a moment: the
// smurfing rule of the contract.
func (s *Store) RetirosPagadosDesde(ctx context.Context, personaID string, desde time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		select count(*) from reservas
		where persona_id = $1 and estado in ('pagada', 'entregada') and pagada_en >= $2`, personaID, desde).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: counting the withdrawals of %s: %w", personaID, err)
	}

	return n, nil
}

// BloquearPorRiesgo blocks a person and raises the alert that explains it.
func (s *Store) BloquearPorRiesgo(ctx context.Context, alertaID, personaID, tiendaID, regla, detalle string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: blocking %s: %w", personaID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `update personas set estado_registro = 'bloqueada' where id = $1`, personaID); err != nil {
		return fmt.Errorf("store: blocking %s: %w", personaID, err)
	}
	if _, err := tx.Exec(ctx, `insert into alertas (id, persona_id, tienda_id, regla, detalle) values ($1, $2, $3, $4, $5)`,
		alertaID, personaID, tiendaID, regla, detalle); err != nil {
		return fmt.Errorf("store: raising the alert of %s: %w", personaID, err)
	}

	return tx.Commit(ctx)
}

// GuardarCobros records the two collection points of a withdrawal.
func (s *Store) GuardarCobros(ctx context.Context, reservaID, tiendaURL, plataformaURL string) error {
	const query = `
		insert into retiros (reserva_id, incoming_payment_url, cobro_plataforma_url)
		values ($1, $2, nullif($3, ''))
		on conflict (reserva_id) do update
		set incoming_payment_url = $2, cobro_plataforma_url = nullif($3, ''), actualizado_en = now()`
	if _, err := s.pool.Exec(ctx, query, reservaID, tiendaURL, plataformaURL); err != nil {
		return fmt.Errorf("store: recording the collection points of %s: %w", reservaID, err)
	}

	return nil
}

// GuardarCotizacion records the quote in force. A new quote needs a new approval,
// so the reservation forgets the previous one.
func (s *Store) GuardarCotizacion(ctx context.Context, reservaID, quoteURL, quotePlataformaURL string, debito float64, moneda string, vence time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: recording the quote of %s: %w", reservaID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `
		update retiros set quote_url = $2, quote_plataforma_url = nullif($3, ''), debito = $4, moneda = $5, quote_vence_en = $6,
		       actualizado_en = now()
		where reserva_id = $1`, reservaID, quoteURL, quotePlataformaURL, debito, moneda, vence); err != nil {
		return fmt.Errorf("store: recording the quote of %s: %w", reservaID, err)
	}
	if _, err := tx.Exec(ctx, `update reservas set aprobacion_en = null where id = $1 and estado = 'reservada'`, reservaID); err != nil {
		return fmt.Errorf("store: clearing the approval of %s: %w", reservaID, err)
	}

	return tx.Commit(ctx)
}

// AprobacionPago is what the person wrote to approve one quote.
type AprobacionPago struct {
	ID           string
	ReservaID    string
	PersonaID    string
	QuoteURL     string
	Debito       float64
	Moneda       string
	TextoUsuario string
	Canal        string
}

// RegistrarAprobacion records the approval (an insert-only trail) and marks the
// reservation approved.
func (s *Store) RegistrarAprobacion(ctx context.Context, a AprobacionPago) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: recording the approval of %s: %w", a.ReservaID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `
		insert into aprobaciones_pago (id, reserva_id, persona_id, quote_url, debito, moneda, texto_usuario, canal)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`,
		a.ID, a.ReservaID, a.PersonaID, a.QuoteURL, a.Debito, a.Moneda, a.TextoUsuario, a.Canal); err != nil {
		return fmt.Errorf("store: recording the approval of %s: %w", a.ReservaID, err)
	}
	if _, err := tx.Exec(ctx, `update reservas set aprobacion_en = now() where id = $1`, a.ReservaID); err != nil {
		return fmt.Errorf("store: marking the approval of %s: %w", a.ReservaID, err)
	}

	return tx.Commit(ctx)
}

// AprobacionDeQuote says whether the person approved exactly that quote.
func (s *Store) AprobacionDeQuote(ctx context.Context, reservaID, quoteURL string) (bool, error) {
	var existe bool
	err := s.pool.QueryRow(ctx, `select exists (select 1 from aprobaciones_pago where reserva_id = $1 and quote_url = $2)`,
		reservaID, quoteURL).Scan(&existe)
	if err != nil {
		return false, fmt.Errorf("store: reading the approval of %s: %w", reservaID, err)
	}

	return existe, nil
}

// GuardarPagos records the two outgoing payments and the answer of the payment, which
// is what a repeated POST /pagos returns.
func (s *Store) GuardarPagos(ctx context.Context, reservaID, outTienda, outPlataforma string, resultado any) error {
	cuerpo, err := json.Marshal(resultado)
	if err != nil {
		return fmt.Errorf("store: encoding the payment of %s: %w", reservaID, err)
	}
	if _, err := s.pool.Exec(ctx, `
		update retiros set outgoing_payment_id = $2, outgoing_plataforma_id = nullif($3, ''), resultado = $4, actualizado_en = now()
		where reserva_id = $1`, reservaID, outTienda, outPlataforma, cuerpo); err != nil {
		return fmt.Errorf("store: recording the payment of %s: %w", reservaID, err)
	}

	return nil
}

// GuardarResultado replaces the answer of the payment (pendiente becomes completado).
func (s *Store) GuardarResultado(ctx context.Context, reservaID string, resultado any) error {
	cuerpo, err := json.Marshal(resultado)
	if err != nil {
		return fmt.Errorf("store: encoding the payment of %s: %w", reservaID, err)
	}
	if _, err := s.pool.Exec(ctx, `update retiros set resultado = $2, actualizado_en = now() where reserva_id = $1`,
		reservaID, cuerpo); err != nil {
		return fmt.Errorf("store: recording the payment result of %s: %w", reservaID, err)
	}

	return nil
}

// MarcarLiquidez records that the ASE put up the liquidity of the payments.
func (s *Store) MarcarLiquidez(ctx context.Context, reservaID string) error {
	if _, err := s.pool.Exec(ctx, `update retiros set liquidez_en = now() where reserva_id = $1`, reservaID); err != nil {
		return fmt.Errorf("store: recording the liquidity of %s: %w", reservaID, err)
	}

	return nil
}

// MarcarPagada closes the payment of a reservation ONCE: it says whether this call did.
func (s *Store) MarcarPagada(ctx context.Context, reservaID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `update reservas set estado = 'pagada', pagada_en = now() where id = $1 and estado = 'reservada'`, reservaID)
	if err != nil {
		return false, fmt.Errorf("store: marking %s paid: %w", reservaID, err)
	}

	return tag.RowsAffected() == 1, nil
}

// CupoGrant is the interactive grant of a cupo, from the request to its token.
type CupoGrant struct {
	PersonaID     string
	GrantID       string
	WalletAddress string
	Limite        float64
	Moneda        string
	Intervalo     string
	Usado         float64
	AccessToken   string
	ManageURL     string
	TokenVenceEn  *time.Time
	ContinueURI   string
	ContinueToken string
	ClientNonce   string
	ServerNonce   string
	AuthServer    string
	AprobadoEn    *time.Time
}

// GuardarCupoPendiente records a requested grant. A person has one cupo, so asking
// again replaces a grant she never approved.
func (s *Store) GuardarCupoPendiente(ctx context.Context, c CupoGrant) error {
	const query = `
		insert into cupos (persona_id, grant_id, wallet_address, limite, moneda, intervalo, continue_uri, continue_token,
		                   nonce, server_nonce, auth_server)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		on conflict (persona_id) do update
		set grant_id = $2, wallet_address = $3, limite = $4, moneda = $5, intervalo = $6, continue_uri = $7,
		    continue_token = $8, nonce = $9, server_nonce = $10, auth_server = $11, access_token = null,
		    token_manage_url = null, token_vence_en = null, aprobado_en = null, usado = 0
		where cupos.aprobado_en is null`
	tag, err := s.pool.Exec(ctx, query, c.PersonaID, c.GrantID, c.WalletAddress, c.Limite, c.Moneda, c.Intervalo,
		c.ContinueURI, c.ContinueToken, c.ClientNonce, c.ServerNonce, c.AuthServer)
	if err != nil {
		return fmt.Errorf("store: recording the cupo of %s: %w", c.PersonaID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCupoAprobado
	}

	return nil
}

// ErrCupoAprobado is returned when a person asks again for a cupo she already approved.
var ErrCupoAprobado = errors.New("la persona ya aprobó su cupo")

const cupoSelect = `
	select persona_id, grant_id, wallet_address, limite::float8, moneda, intervalo, usado::float8,
	       coalesce(access_token, ''), coalesce(token_manage_url, ''), token_vence_en,
	       coalesce(continue_uri, ''), coalesce(continue_token, ''), coalesce(nonce, ''), coalesce(server_nonce, ''),
	       coalesce(auth_server, ''), aprobado_en
	from cupos`

func scanCupo(row pgx.Row) (CupoGrant, error) {
	var c CupoGrant
	err := row.Scan(&c.PersonaID, &c.GrantID, &c.WalletAddress, &c.Limite, &c.Moneda, &c.Intervalo, &c.Usado,
		&c.AccessToken, &c.ManageURL, &c.TokenVenceEn, &c.ContinueURI, &c.ContinueToken, &c.ClientNonce,
		&c.ServerNonce, &c.AuthServer, &c.AprobadoEn)

	return c, err
}

// CupoPorGrant reads a cupo by its grant id.
func (s *Store) CupoPorGrant(ctx context.Context, grantID string) (CupoGrant, bool, error) {
	c, err := scanCupo(s.pool.QueryRow(ctx, cupoSelect+` where grant_id = $1`, grantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CupoGrant{}, false, nil
	}
	if err != nil {
		return CupoGrant{}, false, fmt.Errorf("store: reading the grant %s: %w", grantID, err)
	}

	return c, true, nil
}

// CupoPorPersona reads the cupo of a person.
func (s *Store) CupoPorPersona(ctx context.Context, personaID string) (CupoGrant, bool, error) {
	c, err := scanCupo(s.pool.QueryRow(ctx, cupoSelect+` where persona_id = $1`, personaID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CupoGrant{}, false, nil
	}
	if err != nil {
		return CupoGrant{}, false, fmt.Errorf("store: reading the cupo of %s: %w", personaID, err)
	}

	return c, true, nil
}

// AprobarCupo stores the token of an approved grant (already encrypted by the caller).
func (s *Store) AprobarCupo(ctx context.Context, grantID, token, manageURL string, vence time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		update cupos set access_token = $2, token_manage_url = $3, token_vence_en = $4, aprobado_en = coalesce(aprobado_en, now())
		where grant_id = $1`, grantID, token, manageURL, vence); err != nil {
		return fmt.Errorf("store: approving the grant %s: %w", grantID, err)
	}

	return nil
}

// ActualizarToken replaces the token after a rotation.
func (s *Store) ActualizarToken(ctx context.Context, personaID, token, manageURL string, vence time.Time) error {
	if _, err := s.pool.Exec(ctx, `
		update cupos set access_token = $2, token_manage_url = $3, token_vence_en = $4 where persona_id = $1`,
		personaID, token, manageURL, vence); err != nil {
		return fmt.Errorf("store: rotating the token of %s: %w", personaID, err)
	}

	return nil
}

// SumarUsado adds a payment to what the cupo has used.
func (s *Store) SumarUsado(ctx context.Context, personaID string, debito float64) error {
	if _, err := s.pool.Exec(ctx, `update cupos set usado = usado + $2 where persona_id = $1`, personaID, debito); err != nil {
		return fmt.Errorf("store: using the cupo of %s: %w", personaID, err)
	}

	return nil
}
