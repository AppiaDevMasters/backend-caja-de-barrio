package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contractStatuses is the table of section 3 of the contract, copied by hand so
// this test fails when the code drifts from the document instead of agreeing with
// itself.
var contractStatuses = map[Code]int{
	CodeNoAutorizado:        http.StatusUnauthorized,
	CodeNoEncontrado:        http.StatusNotFound,
	CodeDatosInvalidos:      http.StatusBadRequest,
	CodeSinPoliticas:        http.StatusConflict,
	CodeSinVerificacion:     http.StatusConflict,
	CodePersonaNoHabilitada: http.StatusConflict,
	CodeYaRegistrada:        http.StatusConflict,
	CodeTiendaNoDisponible:  http.StatusConflict,
	CodeTopeExcedido:        http.StatusConflict,
	CodeDatosNoCoinciden:    http.StatusConflict,
	CodeEstadoInvalido:      http.StatusConflict,
	CodeCodigoIncorrecto:    http.StatusConflict,
	CodeSinFaceMatch:        http.StatusConflict,
	CodeSinRiesgo:           http.StatusConflict,
	CodeSinAprobacion:       http.StatusConflict,
	CodeSinCupo:             http.StatusConflict,
	CodeCupoExcedido:        http.StatusConflict,
	CodeCotizacionVencida:   http.StatusConflict,
	CodeHashInvalido:        http.StatusBadRequest,
	CodeFirmaInvalida:       http.StatusUnauthorized,
	CodeProveedor:           http.StatusBadGateway,
}

func TestEveryDocumentedCodeMapsToTheContractStatus(t *testing.T) {
	for code, want := range contractStatuses {
		t.Run(string(code), func(t *testing.T) {
			if got := code.Status(); got != want {
				t.Errorf("%s.Status() = %d, want %d", code, got, want)
			}
		})
	}
}

// contractOrder is the order the contract lists the codes in, which Codes()
// promises to keep.
var contractOrder = []Code{
	CodeNoAutorizado,
	CodeNoEncontrado,
	CodeDatosInvalidos,
	CodeSinPoliticas,
	CodeSinVerificacion,
	CodePersonaNoHabilitada,
	CodeYaRegistrada,
	CodeTiendaNoDisponible,
	CodeTopeExcedido,
	CodeDatosNoCoinciden,
	CodeEstadoInvalido,
	CodeCodigoIncorrecto,
	CodeSinFaceMatch,
	CodeSinRiesgo,
	CodeSinAprobacion,
	CodeSinCupo,
	CodeCupoExcedido,
	CodeCotizacionVencida,
	CodeHashInvalido,
	CodeFirmaInvalida,
	CodeProveedor,
}

func TestTheCodeSetIsExactlyTheContracts(t *testing.T) {
	codes := Codes()
	if len(codes) != len(contractStatuses) {
		t.Fatalf("Codes() returns %d codes, the contract fixes %d", len(codes), len(contractStatuses))
	}

	seen := make(map[Code]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			t.Errorf("Codes() repeats %s", code)
		}
		seen[code] = true

		if _, documented := contractStatuses[code]; !documented {
			t.Errorf("Codes() returns %s, which the contract does not document", code)
		}
	}
	for code := range contractStatuses {
		if !seen[code] {
			t.Errorf("Codes() omits %s", code)
		}
	}
}

func TestTheCodeOrderIsTheContracts(t *testing.T) {
	// The order is part of what Codes() promises, and a set comparison would not
	// notice it changing.
	got := Codes()
	if len(got) != len(contractOrder) {
		t.Fatalf("Codes() returns %d codes, want %d", len(got), len(contractOrder))
	}
	for index, want := range contractOrder {
		if got[index] != want {
			t.Errorf("Codes()[%d] = %s, want %s", index, got[index], want)
		}
	}
}

func TestTheCodesOutsideTheContractStayOutsideIt(t *testing.T) {
	// Two codes exist that the contract does not fix: the 500 the envelope needs,
	// and the 405 the router raises for a method a route does not take. Neither is
	// part of Codes(), which stays the contract's vocabulary.
	additions := map[Code]int{
		CodeInterno:           http.StatusInternalServerError,
		CodeMetodoNoPermitido: http.StatusMethodNotAllowed,
	}

	for code, want := range additions {
		if got := code.Status(); got != want {
			t.Errorf("%s.Status() = %d, want %d", code, got, want)
		}
		if _, documented := contractStatuses[code]; documented {
			t.Errorf("%s is in the contract table, so it is not an addition", code)
		}
	}

	for _, code := range Codes() {
		if _, addition := additions[code]; addition {
			t.Errorf("Codes() offers %s, which is not one of the contract codes", code)
		}
	}
}

func TestErrorfBuildsTheEnvelopeError(t *testing.T) {
	err := Errorf(CodeSinVerificacion, "Primero hay que verificar la identidad en Didit.")

	if err.Code != CodeSinVerificacion {
		t.Errorf("Code = %q, want %q", err.Code, CodeSinVerificacion)
	}
	if want := "Primero hay que verificar la identidad en Didit."; err.Message != want {
		t.Errorf("Message = %q, want %q", err.Message, want)
	}
	if err.Error() == "" {
		t.Error("Error() is empty, so the log line would say nothing")
	}

	// It is an error a caller can return, and %w keeps it recognisable.
	wrapped := fmt.Errorf("la ruta fallo: %w", err)
	var target *Error
	if !errors.As(wrapped, &target) {
		t.Error("a wrapped envelope error is not recognised, so Fail could not classify it")
	}
}

func TestFailWritesAClassifiedError(t *testing.T) {
	recorder := httptest.NewRecorder()

	Fail(recorder, Errorf(CodeSinVerificacion, "Primero hay que verificar la identidad en Didit."))

	if recorder.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	body := decodeEnvelope(t, recorder)
	if body.Error.Codigo != string(CodeSinVerificacion) {
		t.Errorf("codigo = %q, want %q", body.Error.Codigo, CodeSinVerificacion)
	}
	if want := "Primero hay que verificar la identidad en Didit."; body.Error.Mensaje != want {
		t.Errorf("mensaje = %q, want %q", body.Error.Mensaje, want)
	}
}

func TestFailDoesNotLeakAnUnclassifiedError(t *testing.T) {
	// An unclassified error is a bug, and its text can carry a host, a
	// connection string or a person's data. The agent relays whatever is in this
	// body to a person, so the detail stays in the log, not in the reply.
	internal := errors.New("dial tcp 10.10.0.4:5432: connection refused, dsn postgres://rafiki:secreto@postgres")

	recorder := httptest.NewRecorder()
	Fail(recorder, internal)

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	body := decodeEnvelope(t, recorder)
	if body.Error.Codigo != string(CodeInterno) {
		t.Errorf("codigo = %q, want %q", body.Error.Codigo, CodeInterno)
	}
	for _, leak := range []string{"10.10.0.4", "secreto", "connection refused", "postgres://"} {
		if strings.Contains(recorder.Body.String(), leak) {
			t.Errorf("the reply leaks %q: %s", leak, recorder.Body.String())
		}
	}
	if body.Error.Mensaje == "" {
		t.Error("mensaje is empty, so the agent has nothing to say to the person")
	}
}

func TestJSONWritesTheBodyWithItsStatus(t *testing.T) {
	// The Go field names are not the JSON keys, so this proves the tags are
	// honoured and not merely that a map key arrived as it was written.
	type persona struct {
		PersonaID      string `json:"persona_id"`
		CupoDisponible int    `json:"cupo_disponible"`
	}

	recorder := httptest.NewRecorder()

	JSON(recorder, http.StatusCreated, persona{PersonaID: "per_laura", CupoDisponible: 795000})

	if recorder.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if body["persona_id"] != "per_laura" {
		t.Errorf("body = %v, want persona_id per_laura", body)
	}
	if _, found := body["cupo_disponible"]; !found {
		t.Errorf("body = %v, want the snake_case key cupo_disponible", body)
	}
	// The Go name must not travel: the agent parses these keys.
	if _, leaked := body["PersonaID"]; leaked {
		t.Errorf("the Go field name reached the body: %v", body)
	}
}
