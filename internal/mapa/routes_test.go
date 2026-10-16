package mapa_test

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
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/mapa"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

const token = "token-de-servicio-de-prueba"

// tiendasFalsas stands in for the data layer and records what the route asked for.
type tiendasFalsas struct {
	cercanas []store.TiendaCercana
	tienda   store.Tienda
	libre    int64
	err      error

	params         store.TiendasCercanasParams
	publicadoPara  string
	publicadoCon   int64
	publicadoFecha time.Time
}

func (f *tiendasFalsas) TiendasCercanas(_ context.Context, params store.TiendasCercanasParams) ([]store.TiendaCercana, error) {
	f.params = params

	return f.cercanas, f.err
}

func (f *tiendasFalsas) Tienda(_ context.Context, _ string, _ time.Time) (store.Tienda, error) {
	return f.tienda, f.err
}

func (f *tiendasFalsas) PublicarEfectivo(_ context.Context, id string, montoCOP int64, fecha time.Time) (int64, error) {
	f.publicadoPara = id
	f.publicadoCon = montoCOP
	f.publicadoFecha = fecha

	return f.libre, f.err
}

// surface mounts the internal surface exactly the way the backend does, so these
// tests walk the path a caller walks: /internal, the token check, and the map's
// routes behind it.
func surface(t *testing.T, tiendas mapa.Tiendas) *httptest.Server {
	t.Helper()

	server, err := api.New(api.Config{ServiceToken: token})
	if err != nil {
		t.Fatalf("api.New() returned an error: %v", err)
	}
	mapa.Register(server, tiendas)

	mux := http.NewServeMux()
	server.Register(mux)

	httptestServer := httptest.NewServer(mux)
	t.Cleanup(httptestServer.Close)

	return httptestServer
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, withToken bool) (int, map[string]any) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}

	request, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if withToken {
		request.Header.Set(api.ServiceTokenHeader, token)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("calling the surface: %v", err)
	}
	defer response.Body.Close()

	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}

	return response.StatusCode, decoded
}

// codeOf reads the envelope of an error answer.
func codeOf(t *testing.T, body map[string]any) string {
	t.Helper()

	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("the answer is not the contract envelope: %v", body)
	}
	code, ok := envelope["codigo"].(string)
	if !ok {
		t.Fatalf("the envelope has no codigo: %v", envelope)
	}

	return code
}

// THE SECURITY PROPERTY OF EVERY ROUTE: it answers behind the token check. A route
// registered on the caller's own mux would answer before the middleware and this
// test would fail with a 200 instead of a 401.
func TestTheMapRoutesAnswerBehindTheTokenCheck(t *testing.T) {
	srv := surface(t, &tiendasFalsas{})

	status, body := call(t, srv, http.MethodGet, "/internal/mapa/tiendas/cercanas?lat=6.2766&lon=-75.5583&monto_cop=200000", "", false)

	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: the route answered without the token", status)
	}
	if code := codeOf(t, body); code != string(api.CodeNoAutorizado) {
		t.Errorf("codigo = %s, want %s", code, api.CodeNoAutorizado)
	}
}

// The answer of 6.1 with the numbers of the contract's own example, plus what the
// route asked the data layer for: the levels the amount reaches, the default radius
// and the cap of three.
func TestCercanasAnswersWithTheContractsNumbers(t *testing.T) {
	fake := &tiendasFalsas{cercanas: []store.TiendaCercana{{
		ID:            "tie_jaime",
		Nombre:        "Tienda Don Jaime",
		Direccion:     "Cra 50 #92-15",
		Metros:        64.5,
		Lat:           6.2761,
		Lon:           -75.558,
		WalletAddress: "$ilp.interledger-test.dev/tienda-cajadebarrio",
		EfectivoLibre: 800_000,
	}}}
	srv := surface(t, fake)

	status, body := call(t, srv, http.MethodGet, "/internal/mapa/tiendas/cercanas?lat=6.2766&lon=-75.5583&monto_cop=200000", "", true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}

	tiendas, ok := body["tiendas"].([]any)
	if !ok || len(tiendas) != 1 {
		t.Fatalf("tiendas = %v, want one store", body["tiendas"])
	}
	tienda, ok := tiendas[0].(map[string]any)
	if !ok {
		t.Fatalf("the store is not an object: %v", tiendas[0])
	}

	// The numbers of the contract's 6.1 example.
	esperado := map[string]float64{
		"comision_tendero_cop":    3000,
		"tasa_tendero":            0.015,
		"comision_plataforma_cop": 2000,
		"total_cop":               205000,
		"metros":                  65,
	}
	for key, want := range esperado {
		got, ok := tienda[key].(float64)
		if !ok {
			t.Errorf("%s = %v, want a number", key, tienda[key])
			continue
		}
		if diff := got - want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if tienda["id"] != "tie_jaime" || tienda["wallet_address"] != "$ilp.interledger-test.dev/tienda-cajadebarrio" {
		t.Errorf("the answer is missing the fields the agent uses: %v", tienda)
	}

	// What the route asked for: the amount decides which levels can serve it, the
	// radius defaults to 1500 and the agent is offered three stores.
	params := fake.params
	if params.MontoCOP != 200_000 || params.RadioM != 1500 || params.Maximo != 3 {
		t.Errorf("the route asked for %+v, want the amount, the default radius and three stores", params)
	}
	if len(params.Niveles) != 3 {
		t.Errorf("Niveles = %v, want the three levels a 200000 withdrawal can use", params.Niveles)
	}
	if params.Fecha.IsZero() {
		t.Error("the route asked with a zero day, and the cash is published per day")
	}
}

// A search that finds nothing is an answer and not a failure: the agent has to be
// able to tell the person that no store can serve that amount.
func TestCercanasWithoutResultsIsAnAnswer(t *testing.T) {
	srv := surface(t, &tiendasFalsas{cercanas: nil})

	status, body := call(t, srv, http.MethodGet, "/internal/mapa/tiendas/cercanas?lat=6.2766&lon=-75.5583&monto_cop=9000000", "", true)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: finding nothing is not a failure", status)
	}
	tiendas, ok := body["tiendas"].([]any)
	if !ok {
		t.Fatalf("tiendas = %v, want an empty list and not null", body["tiendas"])
	}
	if len(tiendas) != 0 {
		t.Errorf("tiendas = %v, want an empty list", tiendas)
	}
}

func TestCercanasRejectsParamsItCannotUse(t *testing.T) {
	srv := surface(t, &tiendasFalsas{})

	queries := map[string]string{
		"without latitude":        "?lon=-75.5583&monto_cop=200000",
		"without amount":          "?lat=6.2766&lon=-75.5583",
		"a zero amount":           "?lat=6.2766&lon=-75.5583&monto_cop=0",
		"an amount in words":      "?lat=6.2766&lon=-75.5583&monto_cop=doscientos",
		"a latitude out of range": "?lat=99&lon=-75.5583&monto_cop=200000",
		"a negative radius":       "?lat=6.2766&lon=-75.5583&monto_cop=200000&radio_m=-1",
	}

	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			status, body := call(t, srv, http.MethodGet, "/internal/mapa/tiendas/cercanas"+query, "", true)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %v", status, body)
			}
			if code := codeOf(t, body); code != string(api.CodeDatosInvalidos) {
				t.Errorf("codigo = %s, want %s", code, api.CodeDatosInvalidos)
			}
		})
	}
}

func TestDetalleAnswersAndRefusesAnUnknownStore(t *testing.T) {
	srv := surface(t, &tiendasFalsas{tienda: store.Tienda{
		ID:            "tie_jaime",
		Nombre:        "Tienda Don Jaime",
		Direccion:     "Cra 50 #92-15",
		Lat:           6.2761,
		Lon:           -75.558,
		Nivel:         "comercio",
		WalletAddress: "$ilp.interledger-test.dev/tienda-cajadebarrio",
		EfectivoLibre: 800_000,
	}})

	status, body := call(t, srv, http.MethodGet, "/internal/mapa/tiendas/tie_jaime", "", true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if body["id"] != "tie_jaime" || body["nivel"] != "comercio" || body["efectivo_libre_cop"] != float64(800000) {
		t.Errorf("the answer is missing the fields of 6.2: %v", body)
	}

	unknown := surface(t, &tiendasFalsas{err: store.ErrTiendaNoEncontrada})
	status, body = call(t, unknown, http.MethodGet, "/internal/mapa/tiendas/tie_fantasma", "", true)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %v", status, body)
	}
	if code := codeOf(t, body); code != string(api.CodeNoEncontrado) {
		t.Errorf("codigo = %s, want %s", code, api.CodeNoEncontrado)
	}
}

func TestPublicarEfectivoAnswersAndRefuses(t *testing.T) {
	fake := &tiendasFalsas{libre: 900_000}
	srv := surface(t, fake)

	status, body := call(t, srv, http.MethodPut, "/internal/mapa/tiendas/tie_jaime/efectivo", `{"monto_cop":900000}`, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if body["ok"] != true || body["libre_cop"] != float64(900000) {
		t.Errorf("the answer of 6.3 = %v, want ok and the free cash", body)
	}
	if fake.publicadoPara != "tie_jaime" || fake.publicadoCon != 900_000 || fake.publicadoFecha.IsZero() {
		t.Errorf("the route published %d for %s on %v, want the store, the amount and the day", fake.publicadoCon, fake.publicadoPara, fake.publicadoFecha)
	}

	refusals := []struct {
		name string
		fake *tiendasFalsas
		body string
		want int
		code string
	}{
		{"less cash than the store already reserved", &tiendasFalsas{err: store.ErrEfectivoMenorQueReservado}, `{"monto_cop":400000}`, http.StatusBadRequest, string(api.CodeDatosInvalidos)},
		{"a store that does not exist", &tiendasFalsas{err: store.ErrTiendaNoEncontrada}, `{"monto_cop":900000}`, http.StatusNotFound, string(api.CodeNoEncontrado)},
		{"negative cash", &tiendasFalsas{}, `{"monto_cop":-1}`, http.StatusBadRequest, string(api.CodeDatosInvalidos)},
		// A field the contract does not fix is a caller bug, and reading it and
		// ignoring it would hide the typo until the cash is wrong.
		{"a field the contract does not fix", &tiendasFalsas{}, `{"monto":900000}`, http.StatusBadRequest, string(api.CodeDatosInvalidos)},
		{"a body that is not an object", &tiendasFalsas{}, `900000`, http.StatusBadRequest, string(api.CodeDatosInvalidos)},
	}

	for _, refusal := range refusals {
		t.Run(refusal.name, func(t *testing.T) {
			status, body := call(t, surface(t, refusal.fake), http.MethodPut, "/internal/mapa/tiendas/tie_jaime/efectivo", refusal.body, true)
			if status != refusal.want {
				t.Fatalf("status = %d, want %d: %v", status, refusal.want, body)
			}
			if code := codeOf(t, body); code != refusal.code {
				t.Errorf("codigo = %s, want %s", code, refusal.code)
			}
		})
	}
}
