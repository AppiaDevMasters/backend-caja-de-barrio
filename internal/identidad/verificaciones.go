package identidad

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/didit"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// VigenciaVerificacion is how long a Didit verification lets a person operate: the
// contract's thirty minutes.
const VigenciaVerificacion = 30 * time.Minute

// Verificaciones is the data layer of the Didit sessions (5.5 to 5.7).
type Verificaciones interface {
	PersonaPorID(ctx context.Context, id string) (store.Persona, bool, error)
	CrearSesionVerificacion(ctx context.Context, id, personaID, motivo string) error
	AsignarSesionDidit(ctx context.Context, id, diditSessionID string) error
	SesionVerificacionPorID(ctx context.Context, id string) (store.SesionVerificacion, bool, error)
	ResolverSesionVerificacion(ctx context.Context, id string, aprobada bool, score *float64) (bool, error)
	MarcarVerificacion(ctx context.Context, personaID, sesionID string, hasta *time.Time) error
	CompletarRegistro(ctx context.Context, personaID, cedula, nombre, wallet, estado string) error
	CrearAlerta(ctx context.Context, id, personaID, regla, detalle string) error
}

// Didit is the provider client these routes need, as an interface so the routes can be
// exercised without calling it.
type Didit interface {
	CreateSessionWith(ctx context.Context, workflowID, vendorData, callbackURL string) (didit.Session, error)
	Decision(ctx context.Context, sessionID string) (didit.Decision, error)
}

// Billeteras creates the wallet a newly registered person pays from, in this
// backend's own ASE. It may be nil: the registration still completes without it.
type Billeteras interface {
	CrearBilletera(ctx context.Context, personaID, nombre string) (string, error)
}

// ConfigDidit is what the routes need to know about the provider's setup.
type ConfigDidit struct {
	// WorkflowRegistro runs document + liveness (motivo "alta").
	WorkflowRegistro string
	// WorkflowBiometrico runs liveness + face match (motivo "retiro"). Empty falls
	// back to WorkflowRegistro.
	WorkflowBiometrico string
	// CallbackURL is where the browser goes back to; the session id is appended.
	CallbackURL string
}

type crearVerificacionBody struct {
	PersonaID   string `json:"persona_id"`
	Whatsapp    string `json:"whatsapp"`
	Motivo      string `json:"motivo"`
	TiendaID    string `json:"tienda_id"`
	SolicitudID string `json:"solicitud_id"`
}

// crearVerificacion answers 5.5: it opens a Didit session.
//
// The three shapes of the contract: a new person by her WhatsApp (motivo "alta", no
// typed document: Didit reads it and Verifik checks it), an existing person
// (motivo "alta" or "retiro"), and a store's keeper ("alta_tendero"), which needs the
// panel's store registration and is answered as not available yet.
func crearVerificacion(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Didit == nil || deps.Verificaciones == nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "La verificación con Didit no está configurada."))

			return
		}

		var body crearVerificacionBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

			return
		}
		motivo := strings.TrimSpace(body.Motivo)
		if body.TiendaID != "" || body.SolicitudID != "" || motivo == "alta_tendero" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "La verificación de tenderos todavía no está disponible."))

			return
		}
		if motivo != "alta" && motivo != "retiro" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El motivo debe ser alta o retiro."))

			return
		}

		persona, err := personaAVerificar(r.Context(), deps, body, motivo)
		if err != nil {
			api.Fail(w, err)

			return
		}

		id, err := nuevoID("did_")
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo crear la verificación."))

			return
		}
		if err := deps.Verificaciones.CrearSesionVerificacion(r.Context(), id, persona.ID, motivo); err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo crear la verificación."))

			return
		}

		workflow := deps.ConfigDidit.WorkflowRegistro
		if motivo == "retiro" && deps.ConfigDidit.WorkflowBiometrico != "" {
			workflow = deps.ConfigDidit.WorkflowBiometrico
		}
		sesion, err := deps.Didit.CreateSessionWith(r.Context(), workflow, id, callbackDe(deps.ConfigDidit.CallbackURL, id))
		if err != nil {
			slog.Error("didit: creating a session", "session_id", id, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo crear la sesión en Didit."))

			return
		}
		if err := deps.Verificaciones.AsignarSesionDidit(r.Context(), id, sesion.ID); err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo guardar la sesión de Didit."))

			return
		}

		api.JSON(w, http.StatusCreated, map[string]any{"session_id": id, "url_verificacion": sesion.URL})
	}
}

// personaAVerificar finds (or, for a new number, creates) the person a session is for.
func personaAVerificar(ctx context.Context, deps Dependencias, body crearVerificacionBody, motivo string) (store.Persona, error) {
	whatsapp := strings.TrimSpace(body.Whatsapp)

	var (
		persona store.Persona
		existe  bool
		err     error
	)
	switch {
	case strings.TrimSpace(body.PersonaID) != "":
		persona, existe, err = deps.Verificaciones.PersonaPorID(ctx, strings.TrimSpace(body.PersonaID))
	case whatsapp != "":
		persona, existe, err = deps.Datos.PersonaPorWhatsapp(ctx, whatsapp)
	default:
		return store.Persona{}, api.Errorf(api.CodeDatosInvalidos, "Falta persona_id o whatsapp.")
	}
	if err != nil {
		return store.Persona{}, api.Errorf(api.CodeInterno, "No se pudo leer a la persona.")
	}

	if !existe {
		if motivo != "alta" || whatsapp == "" {
			return store.Persona{}, api.Errorf(api.CodeNoEncontrado, "No existe esa persona.")
		}
		persona = store.Persona{Whatsapp: whatsapp, Nivel: 1, EstadoRegistro: "verificando"}
	}

	aceptadas, err := deps.Datos.PoliticasAceptadas(ctx, persona.Whatsapp, VersionPoliticas)
	if err != nil {
		return store.Persona{}, api.Errorf(api.CodeInterno, "No se pudo leer a la persona.")
	}
	if !aceptadas {
		return store.Persona{}, api.Errorf(api.CodeSinPoliticas, "Sin aceptar las políticas no se puede verificar la identidad.")
	}
	if persona.EstadoRegistro == "bloqueada" || persona.EstadoRegistro == "rechazada" {
		return store.Persona{}, api.Errorf(api.CodePersonaNoHabilitada, "La persona no puede operar.")
	}
	if motivo == "retiro" && persona.EstadoRegistro != "aprobada" {
		return store.Persona{}, api.Errorf(api.CodePersonaNoHabilitada, "La persona todavía no terminó su registro.")
	}

	if !existe {
		id, err := nuevoID("per_")
		if err != nil {
			return store.Persona{}, api.Errorf(api.CodeInterno, "No se pudo registrar a la persona.")
		}
		persona.ID = id
		if err := deps.Datos.CrearPersona(ctx, persona); err != nil {
			if errors.Is(err, store.ErrYaRegistrada) {
				return store.Persona{}, api.Errorf(api.CodeYaRegistrada, "Ese WhatsApp ya está registrado.")
			}
			return store.Persona{}, api.Errorf(api.CodeInterno, "No se pudo registrar a la persona.")
		}
	}

	return persona, nil
}

func callbackDe(base, sessionID string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}

	return base + separator + "session_id=" + url.QueryEscape(sessionID)
}

// estadoVerificacion answers 5.6. It works without a webhook: when the session is
// still open here, it asks the provider for the decision and, if there is one,
// applies it exactly as the webhook would.
func estadoVerificacion(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Didit == nil || deps.Verificaciones == nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "La verificación con Didit no está configurada."))

			return
		}

		sesion, existe, err := deps.Verificaciones.SesionVerificacionPorID(r.Context(), r.PathValue("session_id"))
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer la verificación."))

			return
		}
		if !existe {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe esa verificación."))

			return
		}

		respuesta, err := resolver(r.Context(), deps, sesion)
		if err != nil {
			api.Fail(w, err)

			return
		}
		api.JSON(w, http.StatusOK, respuesta)
	}
}

type webhookDiditBody struct {
	Cuerpo string `json:"cuerpo"`
	Firma  string `json:"firma"`
}

// webhookDidit answers 5.7. The webhook is a wake-up call, not the verdict: the body is
// never trusted, the decision is read back from the provider with the API key. The
// Didit application is shared with another project, so a session that is not ours is
// answered 200 and ignored (a 4xx would make the provider retry).
func webhookDidit(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Didit == nil || deps.Verificaciones == nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "La verificación con Didit no está configurada."))

			return
		}

		var body webhookDiditBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

			return
		}
		evento, err := didit.ParseWebhook([]byte(body.Cuerpo))
		if err != nil {
			api.JSON(w, http.StatusOK, map[string]any{"procesado": false, "ignorado": "cuerpo_invalido"})

			return
		}

		sesion, existe, err := deps.Verificaciones.SesionVerificacionPorID(r.Context(), evento.VendorData)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer la verificación."))

			return
		}
		if !existe || (sesion.DiditSessionID != "" && evento.SessionID != "" && sesion.DiditSessionID != evento.SessionID) {
			api.JSON(w, http.StatusOK, map[string]any{"procesado": false, "ignorado": "sesion_ajena"})

			return
		}
		if !evento.Final() {
			api.JSON(w, http.StatusOK, map[string]any{"procesado": false, "ignorado": evento.Status})

			return
		}

		respuesta, err := resolver(r.Context(), deps, sesion)
		if err != nil {
			api.Fail(w, err)

			return
		}
		pendiente, _ := respuesta["pendiente"].(bool)
		respuesta["procesado"] = !pendiente
		api.JSON(w, http.StatusOK, respuesta)
	}
}

// resolver returns the state of a session and, when the provider has decided and the
// verdict has not been applied yet, applies it once.
func resolver(ctx context.Context, deps Dependencias, sesion store.SesionVerificacion) (map[string]any, error) {
	if sesion.Aprobada == nil && sesion.DiditSessionID != "" {
		decision, err := deps.Didit.Decision(ctx, sesion.DiditSessionID)
		if err != nil {
			slog.Error("didit: reading a decision", "session_id", sesion.ID, "error", err)
			return nil, api.Errorf(api.CodeProveedor, "No se pudo consultar el resultado en Didit.")
		}
		if !decision.Pending && decision.Approved != nil {
			aplico, err := deps.Verificaciones.ResolverSesionVerificacion(ctx, sesion.ID, *decision.Approved, decision.Score)
			if err != nil {
				return nil, api.Errorf(api.CodeInterno, "No se pudo guardar el resultado.")
			}
			if aplico {
				if err := aplicarVeredicto(ctx, deps, sesion, decision); err != nil {
					return nil, err
				}
			}
			sesion.Aprobada, sesion.Score = decision.Approved, decision.Score
		}
	}

	persona, _, err := deps.Verificaciones.PersonaPorID(ctx, sesion.PersonaID)
	if err != nil {
		return nil, api.Errorf(api.CodeInterno, "No se pudo leer a la persona.")
	}

	return map[string]any{
		"aprobada":        sesion.Aprobada,
		"score":           sesion.Score,
		"pendiente":       sesion.Aprobada == nil,
		"motivo":          sesion.Motivo,
		"whatsapp":        persona.Whatsapp,
		"persona_id":      sesion.PersonaID,
		"tienda_id":       nil,
		"estado_registro": persona.EstadoRegistro,
	}, nil
}

// aplicarVeredicto runs the effects of a verdict, once. Approved: thirty minutes of
// verification and, for a registration, Verifik on the document Didit read, the name
// the registry holds and the person's wallet. Declined: no verification and an alert.
func aplicarVeredicto(ctx context.Context, deps Dependencias, sesion store.SesionVerificacion, decision didit.Decision) error {
	falla := api.Errorf(api.CodeInterno, "No se pudo aplicar el resultado de la verificación.")

	if !*decision.Approved {
		if err := deps.Verificaciones.MarcarVerificacion(ctx, sesion.PersonaID, sesion.ID, nil); err != nil {
			return falla
		}
		alerta, err := nuevoID("ale_")
		if err == nil {
			_ = deps.Verificaciones.CrearAlerta(ctx, alerta, sesion.PersonaID, "verificacion_facial_fallida", "sesión "+sesion.ID)
		}

		return nil
	}

	hasta := time.Now().Add(VigenciaVerificacion)
	if err := deps.Verificaciones.MarcarVerificacion(ctx, sesion.PersonaID, sesion.ID, &hasta); err != nil {
		return falla
	}
	if sesion.Motivo != "alta" {
		return nil
	}

	// Registration: nothing is approved on the provider's word alone. The registry has
	// to know the document; otherwise the person goes to manual review.
	estado, cedula, nombre := "revision_manual", decision.Document.Number, decision.Document.FullName
	if cedula != "" && deps.Cedulas != nil {
		documento, err := deps.Cedulas.VerifyDocument(ctx, "CC", cedula)
		if err != nil {
			slog.Error("verifik: checking the document Didit read", "session_id", sesion.ID, "error", err)
		} else if documento.Valid {
			estado = "aprobada"
			if documento.FullName != "" {
				nombre = documento.FullName
			}
		}
	}

	wallet := ""
	if estado == "aprobada" && deps.Billeteras != nil {
		creada, err := deps.Billeteras.CrearBilletera(ctx, sesion.PersonaID, nombre)
		if err != nil {
			slog.Error("rafiki: creating the wallet of a new person", "persona_id", sesion.PersonaID, "error", err)
		} else {
			wallet = creada
		}
	}

	if err := deps.Verificaciones.CompletarRegistro(ctx, sesion.PersonaID, cedula, nombre, wallet, estado); err != nil {
		return falla
	}

	return nil
}
