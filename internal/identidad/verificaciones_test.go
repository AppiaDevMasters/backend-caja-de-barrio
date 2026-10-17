package identidad_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/didit"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/identidad"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/verifik"
)

// memoria implements Datos and Verificaciones over maps.
type memoria struct {
	personas  map[string]store.Persona
	sesiones  map[string]store.SesionVerificacion
	politicas map[string]bool
	alertas   []string
	cargas    map[string]int64
	motivos   []string
}

func nuevaMemoria() *memoria {
	return &memoria{personas: map[string]store.Persona{}, sesiones: map[string]store.SesionVerificacion{},
		politicas: map[string]bool{}, cargas: map[string]int64{}}
}

func (m *memoria) PersonaPorWhatsapp(_ context.Context, whatsapp string) (store.Persona, bool, error) {
	for _, p := range m.personas {
		if p.Whatsapp == whatsapp {
			return p, true, nil
		}
	}
	return store.Persona{}, false, nil
}
func (m *memoria) PersonaPorID(_ context.Context, id string) (store.Persona, bool, error) {
	p, ok := m.personas[id]
	return p, ok, nil
}
func (m *memoria) TenderoPorWhatsapp(context.Context, string, time.Time) (store.Tendero, bool, error) {
	return store.Tendero{}, false, nil
}
func (m *memoria) ElegibilidadDe(context.Context, string, string, time.Time) (store.Elegibilidad, error) {
	return store.Elegibilidad{}, nil
}
func (m *memoria) CupoDe(context.Context, string) (store.Cupo, bool, error) {
	return store.Cupo{}, false, nil
}
func (m *memoria) AceptarPoliticas(_ context.Context, _, whatsapp, _, _ string) error {
	m.politicas[whatsapp] = true
	return nil
}
func (m *memoria) PoliticasAceptadas(_ context.Context, whatsapp, _ string) (bool, error) {
	return m.politicas[whatsapp], nil
}
func (m *memoria) CrearPersona(_ context.Context, p store.Persona) error {
	m.personas[p.ID] = p
	return nil
}
func (m *memoria) CrearSesionVerificacion(_ context.Context, id, personaID, motivo string) error {
	m.sesiones[id] = store.SesionVerificacion{ID: id, PersonaID: personaID, Motivo: motivo}
	return nil
}
func (m *memoria) AsignarSesionDidit(_ context.Context, id, diditID string) error {
	s := m.sesiones[id]
	s.DiditSessionID = diditID
	m.sesiones[id] = s
	return nil
}
func (m *memoria) SesionVerificacionPorID(_ context.Context, id string) (store.SesionVerificacion, bool, error) {
	s, ok := m.sesiones[id]
	return s, ok, nil
}
func (m *memoria) SesionVerificacionPorDiditID(_ context.Context, diditID string) (store.SesionVerificacion, bool, error) {
	for _, s := range m.sesiones {
		if s.DiditSessionID == diditID {
			return s, true, nil
		}
	}
	return store.SesionVerificacion{}, false, nil
}
func (m *memoria) UltimaAltaAprobada(_ context.Context, personaID string) (string, bool, error) {
	for _, s := range m.sesiones {
		if s.PersonaID == personaID && s.Motivo == "alta" && s.Aprobada != nil && *s.Aprobada {
			return s.ID, true, nil
		}
	}
	return "", false, nil
}
func (m *memoria) ResolverSesionVerificacion(_ context.Context, id string, aprobada bool, score *float64) (bool, error) {
	s := m.sesiones[id]
	if s.Aprobada != nil {
		return false, nil
	}
	s.Aprobada, s.Score = &aprobada, score
	m.sesiones[id] = s
	return true, nil
}
func (m *memoria) MarcarVerificacion(_ context.Context, personaID, sesionID string, hasta *time.Time) error {
	p := m.personas[personaID]
	p.VerificadoHasta = hasta
	if hasta != nil {
		p.VerificacionSessionID = &sesionID
	} else {
		p.VerificacionSessionID = nil
	}
	m.personas[personaID] = p
	return nil
}
func (m *memoria) CompletarRegistro(_ context.Context, personaID, cedula, nombre, wallet, estado string) error {
	p := m.personas[personaID]
	if cedula != "" {
		p.Cedula = cedula
	}
	if nombre != "" {
		p.Nombre = nombre
	}
	if wallet != "" {
		p.WalletAddress = wallet
	}
	p.EstadoRegistro = estado
	m.personas[personaID] = p
	return nil
}
func (m *memoria) CrearAlerta(_ context.Context, _, personaID, regla, _ string) error {
	m.alertas = append(m.alertas, personaID+":"+regla)
	return nil
}

func (m *memoria) RegistrarMovimiento(_ context.Context, _, wallet, tipo string, monto int64, referencia string) error {
	m.cargas[wallet+"|"+tipo+"|"+referencia] = monto
	return nil
}

func (m *memoria) AnotarMotivoRevision(_ context.Context, personaID, motivo string) error {
	m.motivos = append(m.motivos, personaID+":"+motivo)
	return nil
}

// diditFalso records the sessions it creates and answers the decision it is told.
type diditFalso struct {
	workflows map[string]string // vendor_data -> workflow
	callbacks map[string]string
	decision  didit.Decision
	consultas int
}

func (d *diditFalso) CreateSessionWith(_ context.Context, workflowID, vendorData, callbackURL string) (didit.Session, error) {
	d.workflows[vendorData], d.callbacks[vendorData] = workflowID, callbackURL
	return didit.Session{ID: "didit-" + vendorData, URL: "https://verify.didit.me/" + vendorData}, nil
}
func (d *diditFalso) Decision(context.Context, string) (didit.Decision, error) {
	d.consultas++
	return d.decision, nil
}

type registroFalso struct{ validos map[string]string }

func (r registroFalso) VerifyDocument(_ context.Context, _, numero string) (verifik.Document, error) {
	nombre, ok := r.validos[numero]
	return verifik.Document{Valid: ok, FullName: nombre}, nil
}

type billeterasFalsas struct{ creadas int }

func (b *billeterasFalsas) CrearBilletera(_ context.Context, personaID, _ string) (string, error) {
	b.creadas++
	return "https://op.cajadebarrio.lat/nueva-" + personaID, nil
}

type carterasDeAdorno struct{}

func (carterasDeAdorno) ResolveWalletAddress(context.Context, string) (opclient.WalletAddress, error) {
	return opclient.WalletAddress{}, nil
}

type escenario struct {
	srv        *httptest.Server
	datos      *memoria
	didit      *diditFalso
	billeteras *billeterasFalsas
}

func nuevoEscenario(t *testing.T) escenario {
	t.Helper()
	e := escenario{datos: nuevaMemoria(), didit: &diditFalso{workflows: map[string]string{}, callbacks: map[string]string{}},
		billeteras: &billeterasFalsas{}}
	e.didit.decision = didit.Decision{Status: "In Progress", Pending: true}

	server, err := api.New(api.Config{ServiceToken: token})
	if err != nil {
		t.Fatalf("api.New() returned an error: %v", err)
	}
	identidad.Register(server, identidad.Dependencias{
		Datos:          e.datos,
		Cedulas:        registroFalso{validos: map[string]string{"1036123456": "JUAN PEREZ GOMEZ"}},
		Carteras:       carterasDeAdorno{},
		Didit:          e.didit,
		Verificaciones: e.datos,
		ConfigDidit: identidad.ConfigDidit{WorkflowRegistro: "wf-registro", WorkflowBiometrico: "wf-biometrico",
			CallbackURL: "https://agente.cajadebarrio.lat/didit/retorno", SaldoInicial: 1000000},
		Billeteras: e.billeteras,
		Saldos:     e.datos,
	})
	mux := http.NewServeMux()
	server.Register(mux)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)

	return e
}

func aprobada(documento string) didit.Decision {
	si, score := true, 0.97
	return didit.Decision{Status: "Approved", Approved: &si, Score: &score,
		Document: didit.Document{Number: documento, FullName: "Juan Perez"}}
}

func TestRegistroConDiditPrimeroYVerifikDespues(t *testing.T) {
	e := nuevoEscenario(t)
	e.datos.politicas["573007654321"] = true

	status, body := call(t, e.srv, http.MethodPost, "/internal/identidad/verificaciones",
		`{"whatsapp":"573007654321","motivo":"alta"}`, true)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body %v", status, body)
	}
	sid := body["session_id"].(string)
	if e.didit.workflows[sid] != "wf-registro" {
		t.Fatalf("workflow = %q, want the registration one", e.didit.workflows[sid])
	}
	if e.didit.callbacks[sid] != "https://agente.cajadebarrio.lat/didit/retorno?session_id="+sid {
		t.Fatalf("callback = %q", e.didit.callbacks[sid])
	}

	_, body = call(t, e.srv, http.MethodGet, "/internal/identidad/verificaciones/"+sid, "", true)
	if body["pendiente"] != true {
		t.Fatalf("a session without a decision must be pending: %v", body)
	}

	e.didit.decision = aprobada("1036123456")
	_, body = call(t, e.srv, http.MethodGet, "/internal/identidad/verificaciones/"+sid, "", true)
	if body["aprobada"] != true || body["estado_registro"] != "aprobada" || body["whatsapp"] != "573007654321" {
		t.Fatalf("the approved registration is not reported: %v", body)
	}
	persona, _, _ := e.datos.PersonaPorWhatsapp(context.Background(), "573007654321")
	if persona.Cedula != "1036123456" || persona.Nombre != "JUAN PEREZ GOMEZ" || persona.VerificadoHasta == nil {
		t.Fatalf("the registration did not keep the document, the registry name and the window: %+v", persona)
	}
	if !strings.HasPrefix(persona.WalletAddress, "https://op.cajadebarrio.lat/") {
		t.Fatalf("the person got no wallet: %q", persona.WalletAddress)
	}

	// Asking again (the webhook and the browser can both arrive) applies nothing twice.
	call(t, e.srv, http.MethodGet, "/internal/identidad/verificaciones/"+sid, "", true)
	if len(e.datos.cargas) != 1 || e.datos.cargas[persona.WalletAddress+"|carga_inicial|"+sid] != 1000000 {
		t.Fatalf("the new wallet must be loaded once: %v", e.datos.cargas)
	}
	if e.billeteras.creadas != 1 {
		t.Fatalf("wallets created = %d, want 1", e.billeteras.creadas)
	}
}

func TestUnDocumentoQueElRegistroNoConoceVaARevisionManual(t *testing.T) {
	e := nuevoEscenario(t)
	e.datos.politicas["573001112233"] = true
	_, body := call(t, e.srv, http.MethodPost, "/internal/identidad/verificaciones",
		`{"whatsapp":"573001112233","motivo":"alta"}`, true)
	e.didit.decision = aprobada("99999999")
	_, body = call(t, e.srv, http.MethodGet, "/internal/identidad/verificaciones/"+body["session_id"].(string), "", true)
	if body["estado_registro"] != "revision_manual" || e.billeteras.creadas != 0 {
		t.Fatalf("an unknown document must not be approved: %v", body)
	}
}

func TestSinPoliticasNoHayVerificacion(t *testing.T) {
	e := nuevoEscenario(t)
	status, body := call(t, e.srv, http.MethodPost, "/internal/identidad/verificaciones",
		`{"whatsapp":"573007654321","motivo":"alta"}`, true)
	if status != http.StatusConflict || codeOf(t, body) != "SIN_POLITICAS" {
		t.Fatalf("status = %d, body %v", status, body)
	}
}

func TestElRetiroUsaElWorkflowBiometricoYUnRechazoDejaAlerta(t *testing.T) {
	e := nuevoEscenario(t)
	e.datos.politicas["573000000001"] = true
	e.datos.personas["per_laura"] = store.Persona{ID: "per_laura", Whatsapp: "573000000001", EstadoRegistro: "aprobada"}

	// Without a registration session there is no stored face: the full workflow runs.
	_, body := call(t, e.srv, http.MethodPost, "/internal/identidad/verificaciones",
		`{"persona_id":"per_laura","motivo":"retiro"}`, true)
	if sinAlta := body["session_id"].(string); e.didit.workflows[sinAlta] != "wf-registro" {
		t.Fatalf("without a registration, workflow = %q", e.didit.workflows[sinAlta])
	}

	si := true
	e.datos.sesiones["did_alta_laura"] = store.SesionVerificacion{ID: "did_alta_laura", PersonaID: "per_laura",
		Motivo: "alta", Aprobada: &si}
	_, body = call(t, e.srv, http.MethodPost, "/internal/identidad/verificaciones",
		`{"persona_id":"per_laura","motivo":"retiro"}`, true)
	sid := body["session_id"].(string)
	// Didit matches the face stored under the registration's vendor_data.
	if e.didit.workflows["did_alta_laura"] != "wf-biometrico" {
		t.Fatalf("the withdrawal must reuse the registration's vendor_data with the biometric workflow: %v", e.didit.workflows)
	}
	if !strings.HasSuffix(e.didit.callbacks["did_alta_laura"], "session_id="+sid) && e.didit.callbacks["did_alta_laura"] != "" {
		t.Fatalf("the browser comes back with our own session id: %q", e.didit.callbacks["did_alta_laura"])
	}

	no := false
	e.didit.decision = didit.Decision{Status: "Declined", Approved: &no}
	_, body = call(t, e.srv, http.MethodGet, "/internal/identidad/verificaciones/"+sid, "", true)
	if body["aprobada"] != false || len(e.datos.alertas) != 1 || e.datos.personas["per_laura"].VerificadoHasta != nil {
		t.Fatalf("a declined face must leave no verification and one alert: %v %v", body, e.datos.alertas)
	}
}

func TestElWebhookIgnoraSesionesDeOtroProyectoYLeeLaDecisionDeDidit(t *testing.T) {
	e := nuevoEscenario(t)
	ajeno, _ := json.Marshal(map[string]string{"cuerpo": `{"session_id":"x","vendor_data":"de-compense","status":"Approved"}`})
	status, body := call(t, e.srv, http.MethodPost, "/internal/identidad/webhooks/didit", string(ajeno), true)
	if status != http.StatusOK || body["ignorado"] != "sesion_ajena" || e.didit.consultas != 0 {
		t.Fatalf("another project's session must be ignored with a 200: %d %v", status, body)
	}

	e.datos.politicas["573007654321"] = true
	_, creada := call(t, e.srv, http.MethodPost, "/internal/identidad/verificaciones",
		`{"whatsapp":"573007654321","motivo":"alta"}`, true)
	sid := creada["session_id"].(string)
	e.didit.decision = aprobada("1036123456")
	propio, _ := json.Marshal(map[string]string{"cuerpo": `{"session_id":"didit-` + sid + `","vendor_data":"` + sid +
		`","status":"Approved"}`})
	_, body = call(t, e.srv, http.MethodPost, "/internal/identidad/webhooks/didit", string(propio), true)
	if body["procesado"] != true || body["aprobada"] != true || e.didit.consultas != 1 {
		t.Fatalf("our session must be resolved by asking Didit: %v", body)
	}
}

// altasFalsas keeps the stores of the storekeeper's verification.
type altasFalsas struct {
	tiendas map[string]store.TiendaRegistro
	cedulas map[string]string
	alertas []string
	datos   *memoria
}

func (a *altasFalsas) TiendaRegistroPorID(_ context.Context, id string) (store.TiendaRegistro, bool, error) {
	t, ok := a.tiendas[id]
	return t, ok, nil
}
func (a *altasFalsas) CrearSesionVerificacionTienda(_ context.Context, id, tiendaID string) error {
	a.datos.sesiones[id] = store.SesionVerificacion{ID: id, TiendaID: tiendaID, Motivo: "alta_tendero"}
	return nil
}
func (a *altasFalsas) ResolverAltaTendero(_ context.Context, tiendaID, estado, cedula, _ string) error {
	t := a.tiendas[tiendaID]
	if t.Estado == "pendiente_verificacion" {
		t.Estado = estado
		a.tiendas[tiendaID], a.cedulas[tiendaID] = t, cedula
	}
	return nil
}
func (a *altasFalsas) CrearAlertaTienda(_ context.Context, _, tiendaID, regla, _ string) error {
	a.alertas = append(a.alertas, tiendaID+":"+regla)
	return nil
}

func TestElTenderoDelPanelSeVerificaYSuTiendaQuedaActiva(t *testing.T) {
	e := nuevoEscenario(t)
	altas := &altasFalsas{datos: e.datos, cedulas: map[string]string{}, tiendas: map[string]store.TiendaRegistro{
		"tie_prueba": {ID: "tie_prueba", Nombre: "Tienda La Prueba", Estado: "pendiente_verificacion",
			WhatsappTendero: "573001112233", NombreTendero: "Pedro"},
	}}
	server, err := api.New(api.Config{ServiceToken: token})
	if err != nil {
		t.Fatal(err)
	}
	identidad.Register(server, identidad.Dependencias{
		Datos: e.datos, Cedulas: registroFalso{validos: map[string]string{"1036123456": "PEDRO TENDERO"}},
		Carteras: carterasDeAdorno{}, Didit: e.didit, Verificaciones: e.datos, AltasTendero: altas,
		ConfigDidit: identidad.ConfigDidit{WorkflowRegistro: "wf-registro", WorkflowBiometrico: "wf-biometrico"},
	})
	mux := http.NewServeMux()
	server.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	status, body := call(t, srv, http.MethodPost, "/internal/identidad/verificaciones", `{"tienda_id":"tie_prueba","motivo":"alta_tendero"}`, true)
	if status != http.StatusConflict || codeOf(t, body) != "SIN_POLITICAS" {
		t.Fatalf("without policies = %d %v", status, body)
	}
	e.datos.politicas["573001112233"] = true
	status, body = call(t, srv, http.MethodPost, "/internal/identidad/verificaciones", `{"tienda_id":"tie_prueba","motivo":"alta_tendero"}`, true)
	if status != http.StatusCreated {
		t.Fatalf("status = %d %v", status, body)
	}
	sid := body["session_id"].(string)
	if e.didit.workflows[sid] != "wf-registro" {
		t.Fatalf("the storekeeper uses the registration workflow, got %q", e.didit.workflows[sid])
	}

	e.didit.decision = aprobada("1036123456")
	_, body = call(t, srv, http.MethodGet, "/internal/identidad/verificaciones/"+sid, "", true)
	if body["estado_tienda"] != "activa" || body["whatsapp"] != "573001112233" || body["tienda_id"] != "tie_prueba" {
		t.Fatalf("the verified storekeeper must activate his store: %v", body)
	}
	if altas.cedulas["tie_prueba"] != "1036123456" {
		t.Fatalf("the store keeps the storekeeper's document: %v", altas.cedulas)
	}
	status, _ = call(t, srv, http.MethodPost, "/internal/identidad/verificaciones", `{"tienda_id":"tie_prueba","motivo":"alta_tendero"}`, true)
	if status != http.StatusConflict {
		t.Fatalf("an active store needs no verification: %d", status)
	}
}
