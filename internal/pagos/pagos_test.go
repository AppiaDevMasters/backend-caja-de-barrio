package pagos_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/pagos"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

const token = "token-de-prueba"

const (
	walletCristian = "https://op.cajadebarrio.lat/cristian"
	walletTienda   = "https://op.cajadebarrio.lat/tienda-de-magno"
	walletApp      = "https://op.cajadebarrio.lat/app-cajadebarrio-cop"
)

type memoria struct {
	reserva      store.ReservaRetiro
	persona      store.Persona
	cupo         *store.CupoGrant
	aprobaciones map[string]bool
	movimientos  map[string]int64
	pagados      int
}

func (m *memoria) BilleteraDe(context.Context, string) (store.Billetera, bool, error) {
	return store.Billetera{}, false, nil
}
func (m *memoria) MovimientosDe(_ context.Context, wallet string) (store.Billetera, error) {
	var saldo int64
	for clave, monto := range m.movimientos {
		if strings.HasPrefix(clave, wallet+"|") {
			saldo += monto
		}
	}
	return store.Billetera{WalletAddress: wallet, Saldo: saldo}, nil
}
func (m *memoria) PersonaPorID(context.Context, string) (store.Persona, bool, error) {
	return m.persona, true, nil
}
func (m *memoria) ReservaRetiroPorID(_ context.Context, id string) (store.ReservaRetiro, bool, error) {
	return m.reserva, id == m.reserva.ID, nil
}
func (m *memoria) ReservaRetiroPorCobro(_ context.Context, url string) (store.ReservaRetiro, bool, error) {
	return m.reserva, m.reserva.IncomingPaymentURL != nil && *m.reserva.IncomingPaymentURL == url, nil
}
func (m *memoria) MarcarRiesgo(_ context.Context, _ string, ok bool) error {
	m.reserva.RiesgoOK = ok
	return nil
}
func (m *memoria) RetirosPagadosDesde(context.Context, string, time.Time) (int, error) {
	return m.pagados, nil
}
func (m *memoria) BloquearPorRiesgo(context.Context, string, string, string, string, string) error {
	m.reserva.EstadoCliente = "bloqueada"
	return nil
}
func (m *memoria) GuardarCobros(_ context.Context, _, tienda, plataforma string) error {
	m.reserva.IncomingPaymentURL, m.reserva.CobroPlataformaURL = &tienda, &plataforma
	return nil
}
func (m *memoria) GuardarCotizacion(_ context.Context, _, q, qp string, debito float64, moneda string, vence time.Time) error {
	m.reserva.QuoteURL, m.reserva.QuotePlataformaURL, m.reserva.Debito = &q, &qp, &debito
	m.reserva.Moneda, m.reserva.QuoteVenceEn, m.reserva.AprobacionEn = &moneda, &vence, nil
	return nil
}
func (m *memoria) RegistrarAprobacion(_ context.Context, a store.AprobacionPago) error {
	m.aprobaciones[a.QuoteURL] = true
	ahora := time.Now()
	m.reserva.AprobacionEn = &ahora
	return nil
}
func (m *memoria) AprobacionDeQuote(_ context.Context, _, quote string) (bool, error) {
	return m.aprobaciones[quote], nil
}
func (m *memoria) GuardarPagos(_ context.Context, _, out, outP string, resultado any) error {
	m.reserva.OutgoingPaymentID, m.reserva.OutgoingPlataformaID = &out, &outP
	m.reserva.Resultado, _ = json.Marshal(resultado)
	return nil
}
func (m *memoria) GuardarResultado(_ context.Context, _ string, resultado any) error {
	m.reserva.Resultado, _ = json.Marshal(resultado)
	return nil
}
func (m *memoria) MarcarLiquidez(context.Context, string) error {
	ahora := time.Now()
	m.reserva.LiquidezEn = &ahora
	return nil
}
func (m *memoria) MarcarPagada(context.Context, string) (bool, error) {
	if m.reserva.Estado != "reservada" {
		return false, nil
	}
	m.reserva.Estado = "pagada"
	return true, nil
}
func (m *memoria) GuardarCupoPendiente(_ context.Context, c store.CupoGrant) error {
	m.cupo = &c
	return nil
}
func (m *memoria) CupoPorGrant(_ context.Context, grantID string) (store.CupoGrant, bool, error) {
	if m.cupo == nil || m.cupo.GrantID != grantID {
		return store.CupoGrant{}, false, nil
	}
	return *m.cupo, true, nil
}
func (m *memoria) CupoPorPersona(context.Context, string) (store.CupoGrant, bool, error) {
	if m.cupo == nil {
		return store.CupoGrant{}, false, nil
	}
	return *m.cupo, true, nil
}
func (m *memoria) AprobarCupo(_ context.Context, _, tok, manage string, vence time.Time) error {
	ahora := time.Now()
	m.cupo.AccessToken, m.cupo.ManageURL, m.cupo.TokenVenceEn, m.cupo.AprobadoEn = tok, manage, &vence, &ahora
	return nil
}
func (m *memoria) ActualizarToken(_ context.Context, _, tok, manage string, vence time.Time) error {
	m.cupo.AccessToken, m.cupo.ManageURL, m.cupo.TokenVenceEn = tok, manage, &vence
	return nil
}
func (m *memoria) SumarUsado(_ context.Context, _ string, debito float64) error {
	m.cupo.Usado += debito
	return nil
}
func (m *memoria) RegistrarMovimiento(_ context.Context, _, wallet, tipo string, monto int64, ref string) error {
	m.movimientos[wallet+"|"+tipo+"|"+ref] = monto
	return nil
}

func (m *memoria) ConfirmarEntrega(_ context.Context, _, _, parte string, recibido bool, _, _ string) (store.Entrega, error) {
	if m.reserva.Estado != "pagada" {
		return store.Entrega{}, store.ErrReservaEnOtroEstado{Estado: m.reserva.Estado}
	}
	ahora := time.Now()
	if parte == "cliente" && !recibido {
		m.reserva.Estado = "en_disputa"
		return store.Entrega{Estado: "en_disputa"}, nil
	}
	if parte == "tendero" {
		m.reserva.EntregaTenderoEn = &ahora
	} else {
		m.reserva.RecibidoClienteEn = &ahora
	}
	if m.reserva.EntregaTenderoEn != nil && m.reserva.RecibidoClienteEn != nil {
		m.reserva.Estado = "entregada"
	}
	return store.Entrega{Estado: m.reserva.Estado, FaltaTendero: m.reserva.EntregaTenderoEn == nil,
		FaltaCliente: m.reserva.RecibidoClienteEn == nil}, nil
}
func (m *memoria) CancelarReserva(context.Context, string, string) error {
	if m.reserva.Estado != "reservada" || m.reserva.OutgoingPaymentID != nil {
		return store.ErrReservaEnOtroEstado{Estado: m.reserva.Estado}
	}
	m.reserva.Estado = "cancelada"
	return nil
}
func (m *memoria) ReservasActivasDeTienda(context.Context, string) ([]store.ReservaActiva, error) {
	return []store.ReservaActiva{{ID: m.reserva.ID, Estado: m.reserva.Estado, MontoCOP: m.reserva.MontoCOP, Cliente: "Cristian"}}, nil
}

// openPaymentsFalso answers like Rafiki did in the live withdrawal (cmd/retiro).
type openPaymentsFalso struct {
	pagos   int
	tokenes []string
}

func (o *openPaymentsFalso) ResolveWalletAddress(_ context.Context, w string) (opclient.WalletAddress, error) {
	return opclient.WalletAddress{URL: w, AssetCode: "COP", AssetScale: 2, AuthServer: "https://auth.cajadebarrio.lat"}, nil
}
func (o *openPaymentsFalso) RequestOutgoingPaymentGrant(_ context.Context, p opclient.OutgoingPaymentGrantParams) (opclient.OutgoingPaymentGrant, error) {
	return opclient.OutgoingPaymentGrant{ID: p.GrantID, RedirectURL: "https://auth.cajadebarrio.lat/interact/1/abc",
		ContinueURI: "https://auth.cajadebarrio.lat/continue/1", ContinueToken: "ct", ClientNonce: "cn", ServerNonce: "sn"}, nil
}
func (o *openPaymentsFalso) ContinueOutgoingPaymentGrant(context.Context, opclient.ContinueOutgoingPaymentGrantParams) (opclient.AccessToken, error) {
	return opclient.AccessToken{Value: "tok-1", ManageURL: "https://auth.cajadebarrio.lat/token/1", ExpiresIn: 600}, nil
}
func (o *openPaymentsFalso) RotateAccessToken(context.Context, string, string) (opclient.AccessToken, error) {
	return opclient.AccessToken{Value: "tok-rotado", ManageURL: "https://auth.cajadebarrio.lat/token/2", ExpiresIn: 600}, nil
}
func (o *openPaymentsFalso) CreateIncomingPayments(_ context.Context, p opclient.CreateIncomingPaymentsParams) (opclient.IncomingPayments, error) {
	return opclient.IncomingPayments{
		Store:    opclient.IncomingPayment{URL: p.StoreWalletAddress + "/incoming-payments/1"},
		Platform: opclient.IncomingPayment{URL: p.PlatformWalletAddress + "/incoming-payments/2"},
	}, nil
}
func (o *openPaymentsFalso) CreateQuotes(_ context.Context, p opclient.CreateQuotesParams) (opclient.Quotes, error) {
	total := (p.StoreAmountCOP + p.PlatformAmountCOP) * 100
	return opclient.Quotes{
		Store: opclient.Quote{URL: p.ClientWalletAddress + "/quotes/1"}, Platform: opclient.Quote{URL: p.ClientWalletAddress + "/quotes/2"},
		Debit:     opclient.Amount{Value: itoa(total), AssetCode: "COP", AssetScale: 2},
		ExpiresAt: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339),
	}, nil
}
func (o *openPaymentsFalso) CreateOutgoingPayments(_ context.Context, p opclient.CreateOutgoingPaymentsParams) (opclient.OutgoingPayments, error) {
	o.pagos++
	o.tokenes = append(o.tokenes, p.Token.Value)
	return opclient.OutgoingPayments{
		Store: opclient.OutgoingPayment{URL: walletCristian + "/outgoing-payments/out-1"}, Platform: opclient.OutgoingPayment{URL: walletCristian + "/outgoing-payments/out-2"},
		Token: opclient.AccessToken{Value: "tok-final", ManageURL: "https://auth.cajadebarrio.lat/token/3", ExpiresIn: 600},
	}, nil
}

type liquidezFalsa struct{ depositos, retiros []string }

func (l *liquidezFalsa) WithdrawIncomingPaymentLiquidity(_ context.Context, id string) error {
	l.retiros = append(l.retiros, id)
	return nil
}

func (l *liquidezFalsa) DepositOutgoingPaymentLiquidity(_ context.Context, id string) error {
	l.depositos = append(l.depositos, id)
	return nil
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

type escenario struct {
	srv      *httptest.Server
	datos    *memoria
	op       *openPaymentsFalso
	liquidez *liquidezFalsa
	recibido int64
}

func nuevoEscenario(t *testing.T) *escenario {
	t.Helper()
	e := &escenario{
		datos: &memoria{
			persona: store.Persona{ID: "per_cristian", Whatsapp: "573506477716", WalletAddress: walletCristian, EstadoRegistro: "aprobada"},
			reserva: store.ReservaRetiro{ID: "res_1", Estado: "reservada", MontoCOP: 200_000, ComisionTenderoCOP: 3_600,
				ComisionPlataformaCOP: 2_000, FaceMatchOK: true, VenceEn: time.Now().Add(15 * time.Minute),
				TiendaID: "tie_magno", WalletTienda: walletTienda, PersonaID: "per_cristian", WalletCliente: walletCristian,
				NivelCliente: 1, EstadoCliente: "aprobada"},
			aprobaciones: map[string]bool{},
			movimientos:  map[string]int64{walletCristian + "|carga_inicial|did_alta": 1_000_000},
		},
		op:       &openPaymentsFalso{},
		liquidez: &liquidezFalsa{},
	}
	server, err := api.New(api.Config{ServiceToken: token})
	if err != nil {
		t.Fatal(err)
	}
	pagos.Register(server, pagos.Dependencias{
		Datos: e.datos, OpenPayments: e.op, Liquidez: e.liquidez,
		Cobros: func(context.Context, string) (int64, int, error) { return e.recibido, 2, nil },
		Config: pagos.Config{FinishURL: "https://agente.cajadebarrio.lat/op/finish", WalletPlataforma: walletApp,
			AuthServerHash: "https://auth.cajadebarrio.lat/tenant", HostASE: "op.cajadebarrio.lat",
			Secreto: []byte("secreto"), EsperaCobro: 10 * time.Millisecond, EsperaGrant: time.Millisecond},
	})
	mux := http.NewServeMux()
	server.Register(mux)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)

	return e
}

func (e *escenario) pedir(t *testing.T, metodo, ruta, cuerpo string) (int, map[string]any) {
	t.Helper()
	var body io.Reader
	if cuerpo != "" {
		body = strings.NewReader(cuerpo)
	}
	req, _ := http.NewRequest(metodo, e.srv.URL+"/internal"+ruta, body)
	req.Header.Set(api.ServiceTokenHeader, token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var decodificado map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decodificado)

	return res.StatusCode, decodificado
}

func codigo(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	c, _ := envelope["codigo"].(string)
	return c
}

func TestElRetiroSePagaDePuntaAPuntaConOpenPayments(t *testing.T) {
	e := nuevoEscenario(t)

	// 7.3: risk.
	if _, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/riesgo", `{"persona_id":"per_cristian","monto_cop":200000,"dispositivo":"573506477716"}`); body["decision"] != "permitir" {
		t.Fatalf("risk = %v", body)
	}

	// 8.2 and 8.3: the cupo, approved once in the wallet.
	status, body := e.pedir(t, http.MethodPost, "/pagos/cupos", `{"persona_id":"per_cristian","wallet_cliente":"`+walletCristian+`"}`)
	if status != http.StatusCreated || body["redirect_url"] == "" || body["limite"] != float64(1_000_000) {
		t.Fatalf("cupo = %d %v", status, body)
	}
	grant := body["grant_id"].(string)
	hash := opclient.InteractiveHash("cn", "sn", "ref-1", "https://auth.cajadebarrio.lat/tenant")
	if status, body := e.pedir(t, http.MethodPost, "/pagos/cupos/"+grant+"/continuar", `{"interact_ref":"ref-1","hash":"otro"}`); status != http.StatusBadRequest || codigo(body) != "HASH_INVALIDO" {
		t.Fatalf("a forged hash = %d %v", status, body)
	}
	if _, body := e.pedir(t, http.MethodPost, "/pagos/cupos/"+grant+"/continuar", `{"interact_ref":"ref-1","hash":"`+hash+`"}`); body["ok"] != true || body["whatsapp"] != "573506477716" {
		t.Fatalf("continue = %v", body)
	}
	if strings.Contains(e.datos.cupo.AccessToken, "tok-1") {
		t.Fatal("the access token must be stored encrypted")
	}

	// 8.4 to 8.6: collection points, quote and APROBAR.
	if _, body := e.pedir(t, http.MethodPost, "/pagos/cobros", `{"reserva_id":"res_1","wallet_tienda":"`+walletTienda+`","monto_cop":200000}`); body["tienda_recibe_cop"] != float64(203_600) || body["caja_de_barrio_recibe_cop"] != float64(2_000) {
		t.Fatalf("cobros = %v", body)
	}
	if status, body := e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`); status != http.StatusConflict || codigo(body) != "SIN_APROBACION" {
		t.Fatalf("paying before a quote = %d %v", status, body)
	}
	if _, body := e.pedir(t, http.MethodPost, "/pagos/cotizaciones", `{"reserva_id":"res_1","wallet_cliente":"`+walletCristian+`"}`); body["debito"] != float64(205_600) {
		t.Fatalf("quote = %v", body)
	}
	if status, body := e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`); status != http.StatusConflict || codigo(body) != "SIN_APROBACION" {
		t.Fatalf("paying without APROBAR = %d %v", status, body)
	}
	if _, body := e.pedir(t, http.MethodPost, "/pagos/aprobaciones", `{"reserva_id":"res_1","persona_id":"per_cristian","texto_usuario":"APROBAR","canal":"adk"}`); body["ok"] != true {
		t.Fatalf("approval = %v", body)
	}

	// 8.7: the money arrives at the store.
	e.recibido = 203_600 * 100
	status, body = e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`)
	if status != http.StatusOK || body["estado"] != "completado" || body["cupo_disponible"] != float64(1_000_000-205_600) {
		t.Fatalf("pay = %d %v", status, body)
	}
	if len(e.liquidez.retiros) != 2 || e.liquidez.retiros[0] != "1" || e.liquidez.retiros[1] != "2" {
		t.Fatalf("the ASE must withdraw what both collection points received: %v", e.liquidez.retiros)
	}
	if len(e.liquidez.depositos) != 2 || e.liquidez.depositos[0] != "out-1" {
		t.Fatalf("the ASE must put up the liquidity of both payments: %v", e.liquidez.depositos)
	}
	if e.datos.movimientos[walletCristian+"|pago_retiro|res_1"] != -205_600 ||
		e.datos.movimientos[walletTienda+"|cobro_retiro|res_1"] != 203_600 ||
		e.datos.movimientos[walletApp+"|comision_plataforma|res_1"] != 2_000 {
		t.Fatalf("the virtual wallets must move: %v", e.datos.movimientos)
	}
	if e.datos.reserva.Estado != "pagada" || e.op.tokenes[0] != "tok-1" {
		t.Fatalf("estado %s, tokens %v", e.datos.reserva.Estado, e.op.tokenes)
	}

	// Idempotency: the same reservation never pays twice.
	if _, body := e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`); body["estado"] != "completado" || e.op.pagos != 1 {
		t.Fatalf("a second POST /pagos = %v, payments %d", body, e.op.pagos)
	}
	if _, body := e.pedir(t, http.MethodGet, "/pagos/cobros/estado?url="+walletTienda+"/incoming-payments/1", ""); body["completado"] != true || body["recibido"] != float64(203_600) {
		t.Fatalf("estado del cobro = %v", body)
	}

	// 7.6 and 7.7: the delivery, confirmed by both parties.
	e.datos.reserva.Codigo = "4821"
	if status, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/cancelar", `{"motivo":"ya no lo necesito"}`); status != http.StatusConflict {
		t.Fatalf("a paid reservation cannot be cancelled: %d %v", status, body)
	}
	if status, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/entrega-tendero", `{"codigo":"1111"}`); status != http.StatusConflict || codigo(body) != "CODIGO_INCORRECTO" {
		t.Fatalf("a wrong code = %d %v", status, body)
	}
	if _, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/entrega-tendero", `{"codigo":"4821"}`); body["estado"] != "pagada" || body["falta_confirmacion_cliente"] != true {
		t.Fatalf("the storekeeper's side = %v", body)
	}
	if _, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/recibido-cliente", `{"recibido":true,"texto_usuario":"sí, recibí"}`); body["estado"] != "entregada" {
		t.Fatalf("both confirmed = %v", body)
	}
}

func TestSinSaldoNoSeCreaNingunPago(t *testing.T) {
	e := nuevoEscenario(t)
	e.datos.movimientos = map[string]int64{walletCristian + "|carga_inicial|x": 1_000}
	e.pedir(t, http.MethodPost, "/caja/reservas/res_1/riesgo", `{"persona_id":"per_cristian","monto_cop":200000}`)
	_, body := e.pedir(t, http.MethodPost, "/pagos/cupos", `{"persona_id":"per_cristian"}`)
	hash := opclient.InteractiveHash("cn", "sn", "r", "https://auth.cajadebarrio.lat/tenant")
	e.pedir(t, http.MethodPost, "/pagos/cupos/"+body["grant_id"].(string)+"/continuar", `{"interact_ref":"r","hash":"`+hash+`"}`)
	e.pedir(t, http.MethodPost, "/pagos/cobros", `{"reserva_id":"res_1"}`)
	e.pedir(t, http.MethodPost, "/pagos/cotizaciones", `{"reserva_id":"res_1"}`)
	e.pedir(t, http.MethodPost, "/pagos/aprobaciones", `{"reserva_id":"res_1","persona_id":"per_cristian","texto_usuario":"APROBAR"}`)

	status, body := e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`)
	if status != http.StatusConflict || codigo(body) != "SALDO_INSUFICIENTE" || e.op.pagos != 0 {
		t.Fatalf("without balance = %d %v, payments %d", status, body, e.op.pagos)
	}
}

func TestCancelarDevuelveLaReservaYUnaEntregaSinPagoNoSeConfirma(t *testing.T) {
	e := nuevoEscenario(t)
	if status, _ := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/entrega-tendero", `{"codigo":"0000"}`); status != http.StatusConflict {
		t.Fatalf("delivering an unpaid reservation = %d", status)
	}
	if _, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/cancelar", `{"motivo":"la persona se arrepintió"}`); body["ok"] != true || e.datos.reserva.Estado != "cancelada" {
		t.Fatalf("cancel = %v", body)
	}
	if _, body := e.pedir(t, http.MethodGet, "/caja/tiendas/tie_magno/reservas", ""); len(body["reservas"].([]any)) != 1 {
		t.Fatalf("store reservations = %v", body)
	}
}

func TestElRiesgoBloqueaElPitufeo(t *testing.T) {
	e := nuevoEscenario(t)
	e.datos.pagados = 3
	_, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/riesgo", `{"persona_id":"per_cristian","monto_cop":200000}`)
	if body["decision"] != "bloquear" || e.datos.reserva.EstadoCliente != "bloqueada" {
		t.Fatalf("smurfing = %v", body)
	}
	if _, body := e.pedir(t, http.MethodGet, "/caja/reservas/res_1", ""); body["pasos"].(map[string]any)["riesgo_ok"] != false {
		t.Fatalf("reservation = %v", body)
	}
}
