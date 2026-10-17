package routes_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	routes "github.com/AppiaDevMasters/backend-caja-de-barrio/internal/caja/routes"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

const serviceToken = "token-de-servicio-de-prueba"

var fourDigits = regexp.MustCompile(`^[0-9]{4}$`)

type datosFalsos struct {
	eligibilidad store.Elegibilidad
	reserva      store.Reserva
	reservar     store.ReservarParams
	err          error
	released     chan struct{}
	releaseCalls atomic.Int32
	reservarCall atomic.Bool
}

func (f *datosFalsos) ElegibilidadDe(context.Context, string, string, time.Time) (store.Elegibilidad, error) {
	return f.eligibilidad, f.err
}

func (f *datosFalsos) LiberarVencidas(context.Context, time.Time) (int, error) {
	f.releaseCalls.Add(1)
	if f.released != nil {
		select {
		case f.released <- struct{}{}:
		default:
		}
	}
	return 1, nil
}

func (f *datosFalsos) Reservar(_ context.Context, params store.ReservarParams) (store.Reserva, error) {
	f.reservarCall.Store(true)
	f.reservar = params
	if f.err != nil {
		return store.Reserva{}, f.err
	}
	reserva := f.reserva
	reserva.ID = params.ID
	reserva.TiendaID = params.TiendaID
	reserva.PersonaID = params.PersonaID
	reserva.MontoCOP = params.MontoCOP
	reserva.Codigo = params.Codigo
	reserva.Estado = "reservada"
	reserva.CreadaEn = params.Ahora
	reserva.VenceEn = params.Ahora.Add(params.Vigencia)
	return reserva, nil
}

func surface(t *testing.T, datos routes.Datos) *httptest.Server {
	t.Helper()
	server, err := api.New(api.Config{ServiceToken: serviceToken})
	if err != nil {
		t.Fatalf("api.New() returned an error: %v", err)
	}
	routes.Register(server, datos)
	mux := http.NewServeMux()
	server.Register(mux)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func call(t *testing.T, server *httptest.Server, body string, withToken bool) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/internal/caja/reservas", strings.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if withToken {
		request.Header.Set(api.ServiceTokenHeader, serviceToken)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("calling route: %v", err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return response.StatusCode, decoded
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body is not an error envelope: %v", body)
	}
	code, ok := envelope["codigo"].(string)
	if !ok {
		t.Fatalf("error envelope has no codigo: %v", envelope)
	}
	return code
}

func TestExpiryReleaserCallsTheStoreAndStopsWithContext(t *testing.T) {
	fake := &datosFalsos{released: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	routes.StartExpiryReleaser(ctx, fake, time.Millisecond, logger)
	select {
	case <-fake.released:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("expiry releaser did not call the store")
	}
	callsAtCancel := fake.releaseCalls.Load()
	time.Sleep(20 * time.Millisecond)
	if calls := fake.releaseCalls.Load(); calls != callsAtCancel {
		t.Fatalf("release calls after cancellation = %d, want %d", calls, callsAtCancel)
	}
}

func TestReservaRequiresServiceToken(t *testing.T) {
	server := surface(t, &datosFalsos{})
	status, body := call(t, server, `{"tienda_id":"tie_jaime","persona_id":"per_laura","monto_cop":10000}`, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if got := errorCode(t, body); got != string(api.CodeNoAutorizado) {
		t.Fatalf("codigo = %s, want %s", got, api.CodeNoAutorizado)
	}
}

func TestReservaRejectsPersonWhoIsNotApproved(t *testing.T) {
	fake := &datosFalsos{eligibilidad: store.Elegibilidad{
		Existe:              true,
		EstadoRegistro:      "pendiente",
		PoliticasAceptadas:  true,
		VerificacionVigente: true,
	}}
	server := surface(t, fake)
	status, body := call(t, server, `{"tienda_id":"tie_jaime","persona_id":"per_laura","monto_cop":10000}`, true)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if got := errorCode(t, body); got != string(api.CodePersonaNoAprobada) {
		t.Fatalf("codigo = %s, want %s", got, api.CodePersonaNoAprobada)
	}
	if fake.reservarCall.Load() {
		t.Fatal("Reservar was called for a non-approved person")
	}
}

func TestReservaGeneratesCodeAndUsesContractLifetime(t *testing.T) {
	fake := &datosFalsos{eligibilidad: store.Elegibilidad{
		Existe:              true,
		EstadoRegistro:      "aprobada",
		PoliticasAceptadas:  true,
		VerificacionVigente: true,
	}, reserva: store.Reserva{ComisionTenderoCOP: 1000, ComisionPlataformaCOP: 100}}
	server := surface(t, fake)
	status, body := call(t, server, `{"tienda_id":"tie_jaime","persona_id":"per_laura","monto_cop":10000}`, true)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", status, body)
	}
	code, ok := body["codigo_4_digitos"].(string)
	if !ok || !fourDigits.MatchString(code) {
		t.Fatalf("codigo_4_digitos = %v, want exactly four digits", body["codigo_4_digitos"])
	}
	if fake.reservar.Codigo != code {
		t.Fatalf("store received code %q, response returned %q", fake.reservar.Codigo, code)
	}
	if fake.reservar.Vigencia != 15*time.Minute {
		t.Fatalf("vigencia = %s, want 15m", fake.reservar.Vigencia)
	}
	if body["comision_tendero_cop"] != float64(1000) || body["comision_plataforma_cop"] != float64(100) {
		t.Fatalf("commission response = %v", body)
	}
	if body["total_cop"] != float64(11100) {
		t.Fatalf("total_cop = %v, want 11100", body["total_cop"])
	}
}

func TestReservaRejectsUnacceptedPolicies(t *testing.T) {
	fake := &datosFalsos{eligibilidad: store.Elegibilidad{
		Existe:         true,
		EstadoRegistro: "aprobada",
	}}
	server := surface(t, fake)
	status, body := call(t, server, `{"tienda_id":"tie_jaime","persona_id":"per_laura","monto_cop":10000}`, true)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if got := errorCode(t, body); got != string(api.CodeSinPoliticas) {
		t.Fatalf("codigo = %s, want %s", got, api.CodeSinPoliticas)
	}
	if fake.reservarCall.Load() {
		t.Fatal("Reservar was called before policies were accepted")
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "codigo_4_digitos") {
		t.Fatal("error response leaked a reservation code field")
	}
}

func TestReservaRejectsExpiredVerification(t *testing.T) {
	fake := &datosFalsos{eligibilidad: store.Elegibilidad{
		Existe:             true,
		EstadoRegistro:     "aprobada",
		PoliticasAceptadas: true,
	}}
	server := surface(t, fake)
	status, body := call(t, server, `{"tienda_id":"tie_jaime","persona_id":"per_laura","monto_cop":10000}`, true)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if got := errorCode(t, body); got != string(api.CodeSinVerificacion) {
		t.Fatalf("codigo = %s, want %s", got, api.CodeSinVerificacion)
	}
	if fake.reservarCall.Load() {
		t.Fatal("Reservar was called without current verification")
	}
}
