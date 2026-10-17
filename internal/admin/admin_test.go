package admin_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/admin"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

const origenPanel = "http://localhost:3000"

type datosFalsos struct {
	usuarios   map[string]store.UsuarioPanel
	personas   map[string]store.PersonaPanel
	whatsapps  map[string]string
	auditoria  []store.Auditoria
	movimiento map[string]int64
}

func (d *datosFalsos) UsuarioPanelPorUsername(_ context.Context, u string) (store.UsuarioPanel, bool, error) {
	usuario, ok := d.usuarios[u]
	return usuario, ok, nil
}
func (d *datosFalsos) Auditar(_ context.Context, e store.Auditoria) error {
	d.auditoria = append(d.auditoria, e)
	return nil
}
func (d *datosFalsos) HistorialDe(context.Context, string, string) ([]store.EntradaHistorial, error) {
	return nil, nil
}
func (d *datosFalsos) PersonasPanel(_ context.Context, estado, _ string) ([]store.PersonaPanel, error) {
	lista := []store.PersonaPanel{}
	for _, p := range d.personas {
		if estado == "" || p.EstadoRegistro == estado {
			lista = append(lista, p)
		}
	}
	return lista, nil
}
func (d *datosFalsos) PersonaPanelPorID(_ context.Context, id string) (store.PersonaPanel, bool, error) {
	p, ok := d.personas[id]
	return p, ok, nil
}
func (d *datosFalsos) CambiarEstadoPersona(_ context.Context, id string, desde []string, a string) (bool, error) {
	p := d.personas[id]
	for _, estado := range desde {
		if p.EstadoRegistro == estado {
			p.EstadoRegistro = a
			d.personas[id] = p
			return true, nil
		}
	}
	return false, nil
}
func (d *datosFalsos) AsignarWallet(_ context.Context, id, wallet string) error {
	p := d.personas[id]
	p.WalletAddress = wallet
	d.personas[id] = p
	return nil
}
func (d *datosFalsos) MovimientosDe(_ context.Context, wallet string) (store.Billetera, error) {
	var saldo int64
	for clave, monto := range d.movimiento {
		if strings.HasPrefix(clave, wallet+"|") {
			saldo += monto
		}
	}
	return store.Billetera{WalletAddress: wallet, Saldo: saldo}, nil
}
func (d *datosFalsos) RegistrarMovimiento(_ context.Context, _, wallet, tipo string, monto int64, ref string) error {
	d.movimiento[wallet+"|"+tipo+"|"+ref] = monto
	return nil
}

type billeterasFalsas struct{ creadas int }

func (b *billeterasFalsas) CrearBilletera(_ context.Context, id, _ string) (string, error) {
	b.creadas++
	return "https://op.cajadebarrio.lat/" + id, nil
}

type avisosFalsos struct {
	mu       sync.Mutex
	enviados []string
	listo    chan struct{}
}

func (a *avisosFalsos) Avisar(_ context.Context, whatsapp, texto string) error {
	a.mu.Lock()
	a.enviados = append(a.enviados, whatsapp+": "+texto)
	a.mu.Unlock()
	a.listo <- struct{}{}
	return nil
}

type escenario struct {
	srv        *httptest.Server
	datos      *datosFalsos
	billeteras *billeterasFalsas
	avisos     *avisosFalsos
}

func nuevoEscenario(t *testing.T) escenario {
	t.Helper()
	hash, err := admin.HashClave("clave-de-prueba")
	if err != nil {
		t.Fatal(err)
	}
	e := escenario{
		datos: &datosFalsos{
			usuarios: map[string]store.UsuarioPanel{
				"cumplimiento": {Username: "cumplimiento", Nombre: "Oficial", Rol: "cumplimiento", ClaveHash: hash},
				"soporte":      {Username: "soporte", Nombre: "Soporte", Rol: "soporte", ClaveHash: hash},
			},
			personas: map[string]store.PersonaPanel{
				"per_juan": {ID: "per_juan", Nombre: "JUAN PEREZ", Cedula: "1036123456", Whatsapp: "573009990002",
					EstadoRegistro: "revision_manual", CreadaEn: time.Now()},
				"per_laura": {ID: "per_laura", Nombre: "LAURA", Whatsapp: "573000000001", EstadoRegistro: "aprobada",
					WalletAddress: "https://op.cajadebarrio.lat/laura", CreadaEn: time.Now()},
			},
			movimiento: map[string]int64{},
		},
		billeteras: &billeterasFalsas{},
		avisos:     &avisosFalsos{listo: make(chan struct{}, 4)},
	}
	server, err := admin.New(admin.Config{Origenes: []string{origenPanel}, Secreto: []byte("secreto"),
		SaldoInicial: 1000000, CookieInsegura: true}, e.datos, e.billeteras, e.avisos)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server.Register(mux)
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)

	return e
}

func (e escenario) pedir(t *testing.T, metodo, ruta, cuerpo string, cookie *http.Cookie) (*http.Response, map[string]any) {
	t.Helper()
	var body io.Reader
	if cuerpo != "" {
		body = strings.NewReader(cuerpo)
	}
	req, _ := http.NewRequest(metodo, e.srv.URL+ruta, body)
	req.Header.Set("Origin", origenPanel)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var decodificado map[string]any
	_ = json.NewDecoder(res.Body).Decode(&decodificado)

	return res, decodificado
}

func (e escenario) entrar(t *testing.T, usuario string) *http.Cookie {
	t.Helper()
	res, body := e.pedir(t, http.MethodPost, "/admin/auth/login",
		`{"username":"`+usuario+`","password":"clave-de-prueba"}`, nil)
	if res.StatusCode != http.StatusOK || body["rol"] == nil {
		t.Fatalf("login = %d %v", res.StatusCode, body)
	}
	for _, c := range res.Cookies() {
		if c.Name == admin.CookieSesion {
			if !c.HttpOnly || c.SameSite != http.SameSiteNoneMode {
				t.Fatalf("the session cookie must be HttpOnly and SameSite=None: %+v", c)
			}
			return c
		}
	}
	t.Fatal("the login did not set the session cookie")
	return nil
}

func TestCORSSoloParaElPanel(t *testing.T) {
	e := nuevoEscenario(t)
	for origen, quiere := range map[string]int{origenPanel: http.StatusNoContent, "https://otro.example": http.StatusForbidden} {
		req, _ := http.NewRequest(http.MethodOptions, e.srv.URL+"/admin/auth/login", nil)
		req.Header.Set("Origin", origen)
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != quiere {
			t.Errorf("preflight from %s = %d, want %d", origen, res.StatusCode, quiere)
		}
		permitido := res.Header.Get("Access-Control-Allow-Origin")
		if (quiere == http.StatusNoContent) != (permitido == origen) || permitido == "*" {
			t.Errorf("Allow-Origin for %s = %q", origen, permitido)
		}
	}
}

func TestLoginSesionYRoles(t *testing.T) {
	e := nuevoEscenario(t)

	res, _ := e.pedir(t, http.MethodPost, "/admin/auth/login", `{"username":"cumplimiento","password":"otra"}`, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong password = %d", res.StatusCode)
	}
	if res, _ := e.pedir(t, http.MethodGet, "/admin/personas", "", nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session = %d", res.StatusCode)
	}
	falsa := &http.Cookie{Name: admin.CookieSesion, Value: "eyJ1Ijoic3VwZXIiLCJyIjoic3VwZXJhZG1pbiIsInYiOjk5OTk5OTk5OTl9.x"}
	if res, _ := e.pedir(t, http.MethodGet, "/admin/auth/yo", "", falsa); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged cookie = %d", res.StatusCode)
	}

	soporte := e.entrar(t, "soporte")
	if _, body := e.pedir(t, http.MethodGet, "/admin/auth/yo", "", soporte); body["rol"] != "soporte" {
		t.Fatalf("yo = %v", body)
	}
	res, body := e.pedir(t, http.MethodPost, "/admin/personas/per_juan/aprobar", `{"motivo":"Documento revisado a mano"}`, soporte)
	if res.StatusCode != http.StatusForbidden || body["error"].(map[string]any)["codigo"] != "SIN_PERMISO" {
		t.Fatalf("soporte approving = %d %v", res.StatusCode, body)
	}
}

func TestColaDeRevisionYAprobacionManual(t *testing.T) {
	e := nuevoEscenario(t)
	cookie := e.entrar(t, "cumplimiento")

	_, body := e.pedir(t, http.MethodGet, "/admin/personas?estado=revision_manual", "", cookie)
	lista := body["personas"].([]any)
	if len(lista) != 1 {
		t.Fatalf("the review queue = %v", body)
	}
	juan := lista[0].(map[string]any)
	if juan["cedula"] != "****3456" || juan["whatsapp"] != "+57 *** *** 0002" {
		t.Fatalf("identifiers must leave masked: %v", juan)
	}

	if res, _ := e.pedir(t, http.MethodPost, "/admin/personas/per_juan/aprobar", `{"motivo":"corto"}`, cookie); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a short reason = %d", res.StatusCode)
	}

	res, body := e.pedir(t, http.MethodPost, "/admin/personas/per_juan/aprobar",
		`{"motivo":"Cédula revisada a mano: Verifik caído"}`, cookie)
	if res.StatusCode != http.StatusOK || body["estado_registro"] != "aprobada" {
		t.Fatalf("approve = %d %v", res.StatusCode, body)
	}
	if billetera := body["billetera"].(map[string]any); billetera["saldo_cop"] != float64(1000000) {
		t.Fatalf("the approved person must have her wallet loaded: %v", billetera)
	}
	if e.billeteras.creadas != 1 || len(e.datos.auditoria) != 1 || e.datos.auditoria[0].Actor != "cumplimiento" {
		t.Fatalf("wallets %d, audit %+v", e.billeteras.creadas, e.datos.auditoria)
	}
	select {
	case <-e.avisos.listo:
	case <-time.After(2 * time.Second):
		t.Fatal("the agent was not told")
	}
	if !strings.HasPrefix(e.avisos.enviados[0], "573009990002: ") || !strings.Contains(e.avisos.enviados[0], "$1.000.000") {
		t.Fatalf("notice = %q", e.avisos.enviados[0])
	}

	res, body = e.pedir(t, http.MethodPost, "/admin/personas/per_juan/aprobar", `{"motivo":"Segunda aprobación por error"}`, cookie)
	if res.StatusCode != http.StatusConflict || e.billeteras.creadas != 1 {
		t.Fatalf("approving twice = %d %v", res.StatusCode, body)
	}
}

func TestRechazarSoloLoQueNoEstaDecidido(t *testing.T) {
	e := nuevoEscenario(t)
	cookie := e.entrar(t, "cumplimiento")

	if res, _ := e.pedir(t, http.MethodPost, "/admin/personas/per_laura/rechazar", `{"motivo":"No debería poder rechazarla"}`, cookie); res.StatusCode != http.StatusConflict {
		t.Fatalf("rejecting an approved person = %d", res.StatusCode)
	}
	res, body := e.pedir(t, http.MethodPost, "/admin/personas/per_juan/rechazar", `{"motivo":"El documento no es legible"}`, cookie)
	if res.StatusCode != http.StatusOK || body["estado_registro"] != "rechazada" {
		t.Fatalf("reject = %d %v", res.StatusCode, body)
	}
	if res, _ := e.pedir(t, http.MethodGet, "/admin/personas/no-existe", "", cookie); res.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown person = %d", res.StatusCode)
	}
}
