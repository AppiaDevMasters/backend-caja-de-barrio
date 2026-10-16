// Package identidad owns the routes of identity: who is writing, the acceptance of
// the policies and the registration of a person (contract 5.1, 5.2 and 5.4).
//
// The routes register themselves on the internal surface and never touch a router
// directly: a route registered on the caller's own mux would answer before the token
// check and stay unauthenticated. The patterns are RELATIVE to the surface, which
// strips /internal itself.
package identidad

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/verifik"
)

// VersionPoliticas is the version of the policies in force. The contract fixes it as
// a demo value, and it is the one a person has to accept to operate.
const VersionPoliticas = "v1-2026-10"

// zonaColombia is the day boundary of the demo. Colombia has no daylight saving time,
// so a fixed offset is exact and the container needs no timezone database.
var zonaColombia = time.FixedZone("America/Bogota", -5*60*60)

// Datos is the data layer this package needs, as an interface so the routes can be
// exercised without a database.
type Datos interface {
	PersonaPorWhatsapp(ctx context.Context, whatsapp string) (store.Persona, bool, error)
	TenderoPorWhatsapp(ctx context.Context, whatsapp string, fecha time.Time) (store.Tendero, bool, error)
	ElegibilidadDe(ctx context.Context, personaID, versionPoliticas string, ahora time.Time) (store.Elegibilidad, error)
	CupoDe(ctx context.Context, personaID string) (store.Cupo, bool, error)
	AceptarPoliticas(ctx context.Context, id, whatsapp, version, textoUsuario string) error
}

// Cedulas is the civil registry client this package needs (5.3), as an interface so
// the routes can be exercised without calling the provider.
//
// It may be nil: without the provider's token the route is still mounted and answers
// that it is not configured, because a caller that gets an unknown path cannot tell a
// missing configuration from a wrong URL.
type Cedulas interface {
	VerifyDocument(ctx context.Context, documentType, documentNumber string) (verifik.Document, error)
}

// Register adds the identity routes to the surface.
func Register(server *api.Server, datos Datos, cedulas Cedulas) {
	server.Handle("GET /identidad/personas", quienEscribe(datos))
	server.Handle("POST /identidad/politicas/aceptaciones", aceptarPoliticas(datos))
	server.Handle("POST /identidad/cedulas/verificar", verificarCedula(cedulas))
}

// quienEscribeBody is the answer of 5.1 for the person the number belongs to.
type quienEscribeBody struct {
	Rol                 string  `json:"rol"`
	PersonaID           string  `json:"persona_id"`
	Nombre              string  `json:"nombre"`
	EstadoRegistro      string  `json:"estado_registro"`
	Nivel               int     `json:"nivel"`
	WalletAddress       string  `json:"wallet_address"`
	PoliticasAceptadas  bool    `json:"politicas_aceptadas"`
	PoliticasVersion    string  `json:"politicas_version"`
	VerificacionVigente bool    `json:"verificacion_vigente"`
	CupoAprobado        bool    `json:"cupo_aprobado"`
	CupoDisponible      float64 `json:"cupo_disponible"`
	CupoMoneda          string  `json:"cupo_moneda"`
}

// tenderoBody is the other shape of 5.1: the same kind of number, but the store's.
type tenderoBody struct {
	Rol              string `json:"rol"`
	TiendaID         string `json:"tienda_id"`
	NombreTienda     string `json:"nombre_tienda"`
	Nombre           string `json:"nombre"`
	EfectivoLibreCOP int64  `json:"efectivo_libre_cop"`
}

// noRegistradaBody is what the agent gets for a number nobody knows: it is an answer
// and not a failure, because telling a person that she is not registered yet is the
// first step of registering her.
type noRegistradaBody struct {
	Rol                string  `json:"rol"`
	PersonaID          *string `json:"persona_id"`
	PoliticasAceptadas bool    `json:"politicas_aceptadas"`
	PoliticasVersion   string  `json:"politicas_version"`
}

// quienEscribe answers 5.1: who is writing. The same number can be a person's or a
// storekeeper's, and a number nobody knows is answered as such instead of as an error.
func quienEscribe(datos Datos) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		whatsapp := strings.TrimSpace(r.URL.Query().Get("whatsapp"))
		if whatsapp == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta el parametro whatsapp."))

			return
		}

		persona, existe, err := datos.PersonaPorWhatsapp(r.Context(), whatsapp)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer quién escribe."))

			return
		}
		if !existe {
			// A storekeeper is not a person: the store answers for him.
			tendero, esTendero, err := datos.TenderoPorWhatsapp(r.Context(), whatsapp, time.Now().In(zonaColombia))
			if err != nil {
				api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer quién escribe."))

				return
			}
			if esTendero {
				api.JSON(w, http.StatusOK, tenderoBody{
					Rol:              "tendero",
					TiendaID:         tendero.TiendaID,
					NombreTienda:     tendero.NombreTienda,
					Nombre:           tendero.Nombre,
					EfectivoLibreCOP: tendero.EfectivoLibre,
				})

				return
			}

			api.JSON(w, http.StatusOK, noRegistradaBody{
				Rol:              "no_registrada",
				PersonaID:        nil,
				PoliticasVersion: VersionPoliticas,
			})

			return
		}

		elegibilidad, err := datos.ElegibilidadDe(r.Context(), persona.ID, VersionPoliticas, time.Now())
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer quién escribe."))

			return
		}

		body := quienEscribeBody{
			Rol:                 "cliente",
			PersonaID:           persona.ID,
			Nombre:              persona.Nombre,
			EstadoRegistro:      persona.EstadoRegistro,
			Nivel:               persona.Nivel,
			WalletAddress:       persona.WalletAddress,
			PoliticasAceptadas:  elegibilidad.PoliticasAceptadas,
			PoliticasVersion:    VersionPoliticas,
			VerificacionVigente: elegibilidad.VerificacionVigente,
		}

		cupo, tieneCupo, err := datos.CupoDe(r.Context(), persona.ID)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer quién escribe."))

			return
		}
		if tieneCupo {
			body.CupoAprobado = cupo.Aprobado
			body.CupoDisponible = cupo.Limite - cupo.Usado
			body.CupoMoneda = cupo.Moneda
		}

		api.JSON(w, http.StatusOK, body)
	}
}

// aceptarPoliticasBody is the request of 5.2.
type aceptarPoliticasBody struct {
	Whatsapp     string `json:"whatsapp"`
	Version      string `json:"version"`
	TextoUsuario string `json:"texto_usuario"`
}

// aceptarPoliticas answers 5.2: it records that a number accepted a version of the
// policies. Only the version in force counts: accepting an old one would leave a
// person believing she can operate when she cannot.
func aceptarPoliticas(datos Datos) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var body aceptarPoliticasBody
		if err := decoder.Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido: se espera whatsapp, version y texto_usuario."))

			return
		}
		if strings.TrimSpace(body.Whatsapp) == "" || strings.TrimSpace(body.TextoUsuario) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Faltan el whatsapp o el texto que escribió la persona."))

			return
		}
		if body.Version != VersionPoliticas {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, fmt.Sprintf("La versión vigente de las políticas es %s.", VersionPoliticas)))

			return
		}

		id, err := nuevoID("pol_")
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo registrar la aceptación."))

			return
		}

		if err := datos.AceptarPoliticas(r.Context(), id, strings.TrimSpace(body.Whatsapp), body.Version, body.TextoUsuario); err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo registrar la aceptación."))

			return
		}

		api.JSON(w, http.StatusCreated, map[string]any{"ok": true, "version": body.Version})
	}
}

// verificarCedulaBody is the request of 5.3.
type verificarCedulaBody struct {
	Tipo   string `json:"tipo"`
	Numero string `json:"numero"`
}

// verificarCedula answers 5.3: it asks the civil registry whether a document exists and
// gets the full name it holds.
//
// A document the registry does not know is an ANSWER and not a failure: it goes back as
// a 200 with `valida` false, so the agent asks the person to check the number instead of
// telling her that something broke. The failures are the local format rule (400), the
// provider failing (502) and the provider not being configured (500).
func verificarCedula(cedulas Cedulas) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cedulas == nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "La validación de cédulas no está configurada."))

			return
		}

		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var body verificarCedulaBody
		if err := decoder.Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido: se espera {\"tipo\": \"CC\", \"numero\": \"...\"}."))

			return
		}

		documento, err := cedulas.VerifyDocument(r.Context(), body.Tipo, body.Numero)
		switch {
		case errors.Is(err, verifik.ErrInvalidDocument):
			// The provider never saw this one: it is our own format rule.
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El tipo o el número de la cédula no tienen el formato esperado."))

			return
		case err != nil:
			// What the provider said is not repeated verbatim: it is an upstream detail
			// that the person's assistant cannot explain and does not need.
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo validar la cédula con el proveedor."))

			return
		}

		api.JSON(w, http.StatusOK, map[string]any{
			"valida":          documento.Valid,
			"nombre_completo": documento.FullName,
		})
	}
}

// nuevoID mints one identifier with the readable prefix the contract suggests. It
// comes from crypto randomness and not from a counter: two acceptances at the same
// moment would collide with a counter, and the identifier is a primary key.
func nuevoID(prefijo string) (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("identidad: reading random bytes for an id: %w", err)
	}

	return prefijo + hex.EncodeToString(bytes), nil
}
