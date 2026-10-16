package identidad_test

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
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/identidad"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/verifik"
)

const token = "token-de-servicio-de-prueba"

// datosFalsos stands in for the data layer and records what the routes asked for.
type datosFalsos struct {
	persona    store.Persona
	hayPersona bool
	tendero    store.Tendero
	hayTendero bool
	elegible   store.Elegibilidad
	cupo       store.Cupo
	hayCupo    bool
	err        error

	aceptacionID      string
	aceptacionNumero  string
	aceptacionVersion string
}

func (f *datosFalsos) PersonaPorWhatsapp(_ context.Context, _ string) (store.Persona, bool, error) {
	return f.persona, f.hayPersona, f.err
}

func (f *datosFalsos) TenderoPorWhatsapp(_ context.Context, _ string, _ time.Time) (store.Tendero, bool, error) {
	return f.tendero, f.hayTendero, f.err
}

func (f *datosFalsos) ElegibilidadDe(_ context.Context, _, _ string, _ time.Time) (store.Elegibilidad, error) {
	return f.elegible, f.err
}

func (f *datosFalsos) CupoDe(_ context.Context, _ string) (store.Cupo, bool, error) {
	return f.cupo, f.hayCupo, f.err
}

func (f *datosFalsos) AceptarPoliticas(_ context.Context, id, whatsapp, version, _ string) error {
	f.aceptacionID = id
	f.aceptacionNumero = whatsapp
	f.aceptacionVersion = version

	return f.err
}

// surface mounts the internal surface the way the backend does, so these tests walk
// the same path a caller walks: /internal, the token check, and the routes behind it.
func surface(t *testing.T, datos identidad.Datos) *httptest.Server {
	t.Helper()

	server, err := api.New(api.Config{ServiceToken: token})
	if err != nil {
		t.Fatalf("api.New() returned an error: %v", err)
	}
	identidad.Register(server, datos, nil)

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

// THE SECURITY PROPERTY OF EVERY ROUTE: it answers behind the token check.
func TestTheIdentityRoutesAnswerBehindTheTokenCheck(t *testing.T) {
	srv := surface(t, &datosFalsos{})

	status, body := call(t, srv, http.MethodGet, "/internal/identidad/personas?whatsapp=573000000001", "", false)

	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: the route answered without the token", status)
	}
	if code := codeOf(t, body); code != string(api.CodeNoAutorizado) {
		t.Errorf("codigo = %s, want %s", code, api.CodeNoAutorizado)
	}
}

// The client shape of 5.1, with the fields the agent uses: her state, whether she
// accepted the policies, whether her verification is current, and what is left of her
// cupo.
func TestQuienEscribeAnswersTheClientShape(t *testing.T) {
	srv := surface(t, &datosFalsos{
		persona:    store.Persona{ID: "per_laura", Nombre: "Laura Gómez", EstadoRegistro: "aprobada", Nivel: 1, WalletAddress: "https://x/laura"},
		hayPersona: true,
		elegible:   store.Elegibilidad{Existe: true, PoliticasAceptadas: true, VerificacionVigente: false},
		cupo:       store.Cupo{Aprobado: true, Limite: 1_000_000, Usado: 205_000, Moneda: "COP"},
		hayCupo:    true,
	})

	status, body := call(t, srv, http.MethodGet, "/internal/identidad/personas?whatsapp=573000000001", "", true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}

	if body["rol"] != "cliente" || body["persona_id"] != "per_laura" || body["nombre"] != "Laura Gómez" {
		t.Errorf("the answer does not say who is writing: %v", body)
	}
	if body["politicas_aceptadas"] != true || body["politicas_version"] != identidad.VersionPoliticas {
		t.Errorf("the answer does not carry the policies: %v", body)
	}
	if body["verificacion_vigente"] != false {
		t.Errorf("verificacion_vigente = %v, want what the data layer said", body["verificacion_vigente"])
	}
	if body["cupo_aprobado"] != true || body["cupo_moneda"] != "COP" {
		t.Errorf("the answer does not carry the cupo: %v", body)
	}
	// The ceiling minus what was used: the contract prints 795000 for a million with
	// 205.000 used.
	if body["cupo_disponible"] != float64(795_000) {
		t.Errorf("cupo_disponible = %v, want 795000", body["cupo_disponible"])
	}
}

// The other shape of 5.1: the same kind of number, but the storekeeper's.
func TestQuienEscribeAnswersTheStorekeeperShape(t *testing.T) {
	srv := surface(t, &datosFalsos{
		tendero:    store.Tendero{TiendaID: "tie_jaime", NombreTienda: "Tienda Don Jaime", Nombre: "Don Jaime", EfectivoLibre: 600_000},
		hayTendero: true,
	})

	status, body := call(t, srv, http.MethodGet, "/internal/identidad/personas?whatsapp=573000000010", "", true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if body["rol"] != "tendero" || body["tienda_id"] != "tie_jaime" || body["nombre_tienda"] != "Tienda Don Jaime" {
		t.Errorf("the storekeeper's answer = %v, want the store", body)
	}
	if body["efectivo_libre_cop"] != float64(600_000) {
		t.Errorf("efectivo_libre_cop = %v, want the cash the store has free", body["efectivo_libre_cop"])
	}
	if _, hayPersona := body["persona_id"]; hayPersona {
		t.Errorf("the storekeeper's answer carries a persona_id: %v", body)
	}
}

// A number nobody knows is an ANSWER and not a failure: telling a person that she is
// not registered yet is the first step of registering her.
func TestQuienEscribeAnswersANumberNobodyKnows(t *testing.T) {
	srv := surface(t, &datosFalsos{})

	status, body := call(t, srv, http.MethodGet, "/internal/identidad/personas?whatsapp=573000000999", "", true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: not being registered is not a failure", status)
	}
	if body["rol"] != "no_registrada" {
		t.Errorf("rol = %v, want no_registrada", body["rol"])
	}
	if body["persona_id"] != nil {
		t.Errorf("persona_id = %v, want null for a number nobody knows", body["persona_id"])
	}
	if body["politicas_version"] != identidad.VersionPoliticas {
		t.Errorf("politicas_version = %v, want the version in force", body["politicas_version"])
	}
	if body["politicas_aceptadas"] != false {
		t.Errorf("politicas_aceptadas = %v, want false", body["politicas_aceptadas"])
	}
}

func TestQuienEscribeRejectsARequestWithoutANumber(t *testing.T) {
	srv := surface(t, &datosFalsos{})

	status, body := call(t, srv, http.MethodGet, "/internal/identidad/personas", "", true)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", status, body)
	}
	if code := codeOf(t, body); code != string(api.CodeDatosInvalidos) {
		t.Errorf("codigo = %s, want %s", code, api.CodeDatosInvalidos)
	}
}

// The acceptance of 5.2, with an identifier minted by the route and not by a counter.
func TestAceptarPoliticasRecordsTheAcceptanceOfTheVersionInForce(t *testing.T) {
	datos := &datosFalsos{}
	srv := surface(t, datos)

	status, body := call(t, srv, http.MethodPost, "/internal/identidad/politicas/aceptaciones",
		`{"whatsapp":"573000000001","version":"`+identidad.VersionPoliticas+`","texto_usuario":"Acepto"}`, true)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", status, body)
	}
	if body["ok"] != true || body["version"] != identidad.VersionPoliticas {
		t.Errorf("the answer of 5.2 = %v, want ok and the version", body)
	}
	if !strings.HasPrefix(datos.aceptacionID, "pol_") {
		t.Errorf("the acceptance id is %q, want it to carry the contract's prefix", datos.aceptacionID)
	}
	if datos.aceptacionNumero != "573000000001" || datos.aceptacionVersion != identidad.VersionPoliticas {
		t.Errorf("the route recorded %s of %s, want the number and the version it was given", datos.aceptacionVersion, datos.aceptacionNumero)
	}
}

func TestAceptarPoliticasRefusesWhatItCannotUse(t *testing.T) {
	refusals := map[string]string{
		"another version":                   `{"whatsapp":"573000000001","version":"v0-2025-01","texto_usuario":"Acepto"}`,
		"no number":                         `{"version":"` + identidad.VersionPoliticas + `","texto_usuario":"Acepto"}`,
		"no text from the person":           `{"whatsapp":"573000000001","version":"` + identidad.VersionPoliticas + `"}`,
		"a field the contract does not fix": `{"whatsapp":"573000000001","version":"` + identidad.VersionPoliticas + `","texto_usuario":"Acepto","acepto":true}`,
		"a body that is not an object":      `"Acepto"`,
	}

	for name, body := range refusals {
		t.Run(name, func(t *testing.T) {
			status, answer := call(t, surface(t, &datosFalsos{}), http.MethodPost, "/internal/identidad/politicas/aceptaciones", body, true)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %v", status, answer)
			}
			if code := codeOf(t, answer); code != string(api.CodeDatosInvalidos) {
				t.Errorf("codigo = %s, want %s", code, api.CodeDatosInvalidos)
			}
		})
	}
}

// cedulasFalsas stands in for the civil registry and records what it was asked.
type cedulasFalsas struct {
	documento verifik.Document
	err       error
	tipo      string
	numero    string
}

func (f *cedulasFalsas) VerifyDocument(_ context.Context, documentType, documentNumber string) (verifik.Document, error) {
	f.tipo = documentType
	f.numero = documentNumber

	return f.documento, f.err
}

// surfaceConProveedor mounts the surface with a registry double, for the route that
// talks to the provider.
func surfaceConProveedor(t *testing.T, cedulas identidad.Cedulas) *httptest.Server {
	t.Helper()

	server, err := api.New(api.Config{ServiceToken: token})
	if err != nil {
		t.Fatalf("api.New() returned an error: %v", err)
	}
	identidad.Register(server, &datosFalsos{}, cedulas)

	mux := http.NewServeMux()
	server.Register(mux)

	httptestServer := httptest.NewServer(mux)
	t.Cleanup(httptestServer.Close)

	return httptestServer
}

// A document the registry knows comes back with the name it holds.
func TestVerificarCedulaAnswersWithTheNameOfTheRegistry(t *testing.T) {
	cedulas := &cedulasFalsas{documento: verifik.Document{Valid: true, FullName: "LAURA GOMEZ"}}

	status, body := call(t, surfaceConProveedor(t, cedulas), http.MethodPost, "/internal/identidad/cedulas/verificar",
		`{"tipo":"CC","numero":"1036123456"}`, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if body["valida"] != true || body["nombre_completo"] != "LAURA GOMEZ" {
		t.Errorf("the answer of 5.3 = %v, want the document to be valid with its name", body)
	}
	if cedulas.tipo != verifik.DocumentTypeCC || cedulas.numero != "1036123456" {
		t.Errorf("the provider was asked %s/%s, want what the caller sent", cedulas.tipo, cedulas.numero)
	}
}

// A document the registry does NOT know is an answer and not a failure: the agent has
// to be able to ask the person to check the number.
func TestVerificarCedulaAnswersThatADocumentIsNotValid(t *testing.T) {
	cedulas := &cedulasFalsas{documento: verifik.Document{Valid: false}}

	status, body := call(t, surfaceConProveedor(t, cedulas), http.MethodPost, "/internal/identidad/cedulas/verificar",
		`{"tipo":"CC","numero":"1036123456"}`, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: a document that is not valid is not a failure", status)
	}
	if body["valida"] != false {
		t.Errorf("valida = %v, want false", body["valida"])
	}
}

// The local format rule never reaches the provider, and it is the caller's mistake.
func TestVerificarCedulaRefusesWhatTheProviderCannotAccept(t *testing.T) {
	cedulas := &cedulasFalsas{err: verifik.ErrInvalidDocument}

	status, body := call(t, surfaceConProveedor(t, cedulas), http.MethodPost, "/internal/identidad/cedulas/verificar",
		`{"tipo":"TI","numero":"12"}`, true)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", status, body)
	}
	if code := codeOf(t, body); code != string(api.CodeDatosInvalidos) {
		t.Errorf("codigo = %s, want %s", code, api.CodeDatosInvalidos)
	}
}

// A provider failure is a 502, and WHAT THE PROVIDER SAID IS NOT REPEATED: it is an
// upstream detail the person's assistant cannot explain and does not need.
func TestVerificarCedulaDoesNotRepeatWhatTheProviderSaid(t *testing.T) {
	cedulas := &cedulasFalsas{err: &verifik.Error{StatusCode: 500, Message: "detalle interno del proveedor"}}

	status, body := call(t, surfaceConProveedor(t, cedulas), http.MethodPost, "/internal/identidad/cedulas/verificar",
		`{"tipo":"CC","numero":"1036123456"}`, true)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the provider fails: %v", status, body)
	}
	if code := codeOf(t, body); code != string(api.CodeProveedor) {
		t.Errorf("codigo = %s, want %s", code, api.CodeProveedor)
	}
	if strings.Contains(body["error"].(map[string]any)["mensaje"].(string), "detalle interno del proveedor") {
		t.Errorf("the answer repeats what the provider said: %v", body)
	}
}

// Without the provider configured the route is still mounted and says so, instead of
// leaving a caller with a path that does not exist.
func TestVerificarCedulaWithoutAConfiguredProvider(t *testing.T) {
	status, body := call(t, surfaceConProveedor(t, nil), http.MethodPost, "/internal/identidad/cedulas/verificar",
		`{"tipo":"CC","numero":"1036123456"}`, true)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the provider is not configured: %v", status, body)
	}
	if code := codeOf(t, body); code != string(api.CodeInterno) {
		t.Errorf("codigo = %s, want %s", code, api.CodeInterno)
	}
}

// The route is behind the token check, like every other one.
func TestVerificarCedulaAnswersBehindTheTokenCheck(t *testing.T) {
	status, body := call(t, surfaceConProveedor(t, &cedulasFalsas{}), http.MethodPost, "/internal/identidad/cedulas/verificar",
		`{"tipo":"CC","numero":"1036123456"}`, false)

	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: the route answered without the token", status)
	}
	if code := codeOf(t, body); code != string(api.CodeNoAutorizado) {
		t.Errorf("codigo = %s, want %s", code, api.CodeNoAutorizado)
	}
}

func TestVerificarCedulaRejectsABodyItCannotUse(t *testing.T) {
	refusals := map[string]string{
		"no body":                           ``,
		"a body that is not an object":      `"1036123456"`,
		"a field the contract does not fix": `{"tipo":"CC","numero":"1036123456","extra":1}`,
	}

	for name, body := range refusals {
		t.Run(name, func(t *testing.T) {
			status, answer := call(t, surfaceConProveedor(t, &cedulasFalsas{}), http.MethodPost, "/internal/identidad/cedulas/verificar", body, true)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %v", status, answer)
			}
			if code := codeOf(t, answer); code != string(api.CodeDatosInvalidos) {
				t.Errorf("codigo = %s, want %s", code, api.CodeDatosInvalidos)
			}
		})
	}
}
