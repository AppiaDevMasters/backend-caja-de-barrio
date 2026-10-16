package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// Code is one of the error codes the contract fixes in its section 3.
type Code string

// The codes of section 3 of the contract, named as the document names them.
const (
	CodeNoAutorizado        Code = "NO_AUTORIZADO"
	CodeNoEncontrado        Code = "NO_ENCONTRADO"
	CodeDatosInvalidos      Code = "DATOS_INVALIDOS"
	CodeSinPoliticas        Code = "SIN_POLITICAS"
	CodeSinVerificacion     Code = "SIN_VERIFICACION"
	CodePersonaNoHabilitada Code = "PERSONA_NO_HABILITADA"
	CodeYaRegistrada        Code = "YA_REGISTRADA"
	CodeTiendaNoDisponible  Code = "TIENDA_NO_DISPONIBLE"
	CodeTopeExcedido        Code = "TOPE_EXCEDIDO"
	CodeDatosNoCoinciden    Code = "DATOS_NO_COINCIDEN"
	CodeEstadoInvalido      Code = "ESTADO_INVALIDO"
	CodeCodigoIncorrecto    Code = "CODIGO_INCORRECTO"
	CodeSinFaceMatch        Code = "SIN_FACE_MATCH"
	CodeSinRiesgo           Code = "SIN_RIESGO"
	CodeSinAprobacion       Code = "SIN_APROBACION"
	CodeSinCupo             Code = "SIN_CUPO"
	CodeCupoExcedido        Code = "CUPO_EXCEDIDO"
	CodeCotizacionVencida   Code = "COTIZACION_VENCIDA"
	CodeHashInvalido        Code = "HASH_INVALIDO"
	CodeFirmaInvalida       Code = "FIRMA_INVALIDA"
	CodeProveedor           Code = "PROVEEDOR"
)

// The two codes the contract does not fix, both outside Codes() so that function
// stays the contract's vocabulary.
const (
	// CodeInterno is the 500. The contract says every 4xx and 5xx carries the
	// envelope, and an unclassified failure is still a 5xx, so it needs a code:
	// without one the reply would be a bare status the agent cannot turn into a
	// sentence.
	CodeInterno Code = "INTERNO"

	// CodeMetodoNoPermitido is the 405 the router raises when a route exists but
	// does not take that method. The contract fixes no code for it, and it is a
	// caller bug that a person's assistant still has to explain.
	CodeMetodoNoPermitido Code = "METODO_NO_PERMITIDO"
)

// unclassifiedMessage is what a person reads when this package could not classify
// a failure. It says nothing about what went wrong on the inside.
const unclassifiedMessage = "Ocurrió un error inesperado. Intenta de nuevo en unos minutos."

// contractCodes is the table of section 3 in the order the document lists it.
var contractCodes = []Code{
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

// Codes returns the codes the contract fixes, in the order it lists them. The
// slice is a copy, so a caller cannot reorder the table for everyone else.
func Codes() []Code {
	codes := make([]Code, len(contractCodes))
	copy(codes, contractCodes)

	return codes
}

// known reports whether this package can answer with the code. A code the table
// does not carry is a caller bug, and letting it through would send the agent an
// identifier nobody can look up.
func (c Code) known() bool {
	for _, contract := range contractCodes {
		if c == contract {
			return true
		}
	}

	return c == CodeInterno || c == CodeMetodoNoPermitido
}

// Status maps a code to the HTTP status the contract fixes.
//
// The last case is a code this package does not know, which means a caller
// answered with something that is not one of the codes: it answers 500 rather
// than guessing a status a person would be told.
func (c Code) Status() int {
	switch c {
	case CodeNoAutorizado, CodeFirmaInvalida:
		return http.StatusUnauthorized
	case CodeNoEncontrado:
		return http.StatusNotFound
	case CodeDatosInvalidos, CodeHashInvalido:
		return http.StatusBadRequest
	case CodeSinPoliticas, CodeSinVerificacion, CodePersonaNoHabilitada,
		CodeYaRegistrada, CodeTiendaNoDisponible, CodeTopeExcedido,
		CodeDatosNoCoinciden, CodeEstadoInvalido, CodeCodigoIncorrecto,
		CodeSinFaceMatch, CodeSinRiesgo, CodeSinAprobacion, CodeSinCupo,
		CodeCupoExcedido, CodeCotizacionVencida:
		return http.StatusConflict
	case CodeMetodoNoPermitido:
		return http.StatusMethodNotAllowed
	case CodeProveedor:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// Error is a failure with a code the agent turns into a sentence for the person.
//
// A business decision that is not a failure is not an Error: the contract says
// those answer 200 with the result, and turning one into an Error would tell a
// person that something broke when nothing did. A handler that wants to answer a
// blocking decision uses JSON with 200, never Fail.
type Error struct {
	// Code is one of the contract codes.
	Code Code

	// Message goes to the person in clear Spanish, because the agent relays it.
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Errorf builds an Error with the code the caller chose.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// body is the envelope the contract fixes for every failure.
type body struct {
	Error payload `json:"error"`
}

type payload struct {
	Codigo  string `json:"codigo"`
	Mensaje string `json:"mensaje"`
}

// Fail writes the contract envelope with the status of the code.
func Fail(w http.ResponseWriter, err error) {
	var classified *Error
	// A (*Error)(nil) satisfies errors.As, and dereferencing it would crash the
	// process that answers the whole surface.
	if errors.As(err, &classified) && classified != nil {
		if classified.Code.known() {
			writeEnvelope(w, classified.Code, classified.Message)

			return
		}

		slog.Error("the internal surface was answered with a code the contract does not fix",
			"code", string(classified.Code), "error", err)
		writeEnvelope(w, CodeInterno, unclassifiedMessage)

		return
	}

	// An unclassified failure is a bug, and its text can carry a host, a
	// connection string or somebody's data. The agent reads this body to a person,
	// so the detail goes to the log and the body carries a sentence that is true
	// and says nothing more.
	slog.Error("the internal surface failed without a classified error", "error", err)
	writeEnvelope(w, CodeInterno, unclassifiedMessage)
}

func writeEnvelope(w http.ResponseWriter, code Code, message string) {
	JSON(w, code.Status(), body{Error: payload{Codigo: string(code), Mensaje: message}})
}
