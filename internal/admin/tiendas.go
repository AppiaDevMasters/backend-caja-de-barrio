package admin

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/caja"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// whatsappValido is a Colombian mobile number with its country code, digits only, as
// the agent receives it from WhatsApp.
var whatsappValido = regexp.MustCompile(`^57[0-9]{10}$`)

type duenoBody struct {
	Nombre     string  `json:"nombre"`
	Cedula     *string `json:"cedula"`
	Whatsapp   string  `json:"whatsapp"`
	Verificado bool    `json:"verificado"`
}

type tiendaBody struct {
	ID                   string          `json:"id"`
	Nombre               string          `json:"nombre"`
	Direccion            string          `json:"direccion"`
	Lat                  float64         `json:"lat"`
	Lon                  float64         `json:"lon"`
	Nivel                string          `json:"nivel"`
	Estado               string          `json:"estado"`
	Wallet               string          `json:"wallet"`
	Reputacion           float64         `json:"reputacion"`
	TopeRetiroCOP        int64           `json:"tope_retiro_cop"`
	EfectivoPublicadoCOP int64           `json:"efectivo_publicado_cop"`
	EfectivoReservadoCOP int64           `json:"efectivo_reservado_cop"`
	SaldoCOP             int64           `json:"saldo_cop"`
	MotivoRevision       *string         `json:"motivo_revision"`
	CreadaEn             time.Time       `json:"creada_en"`
	Dueno                duenoBody       `json:"dueno"`
	Didit                *diditBody      `json:"didit"`
	Billetera            *billeteraBody  `json:"billetera,omitempty"`
	Historial            []historialBody `json:"historial,omitempty"`
}

func cuerpoTienda(t store.TiendaPanel) tiendaBody {
	body := tiendaBody{
		ID:                   t.ID,
		Nombre:               t.Nombre,
		Direccion:            t.Direccion,
		Lat:                  t.Lat,
		Lon:                  t.Lon,
		Nivel:                t.Nivel,
		Estado:               t.Estado,
		Wallet:               t.WalletAddress,
		Reputacion:           t.Reputacion,
		TopeRetiroCOP:        caja.TopeTienda(t.Nivel),
		EfectivoPublicadoCOP: t.EfectivoPublicado,
		EfectivoReservadoCOP: t.EfectivoReservado,
		SaldoCOP:             t.SaldoCOP,
		MotivoRevision:       t.MotivoRevision,
		CreadaEn:             t.CreadaEn,
		Dueno: duenoBody{
			Nombre:     t.NombreTendero,
			Whatsapp:   enmascarar(t.WhatsappTendero, "+57 *** *** "),
			Verificado: t.CedulaTendero != nil && *t.CedulaTendero != "",
		},
	}
	if t.CedulaTendero != nil && *t.CedulaTendero != "" {
		cedula := enmascarar(*t.CedulaTendero, "****")
		body.Dueno.Cedula = &cedula
	}
	if t.DiditSessionID != nil {
		liveness := "pendiente"
		if t.DiditAprobada != nil {
			liveness = "rechazada"
			if *t.DiditAprobada {
				liveness = "aprobada"
			}
		}
		body.Didit = &diditBody{Liveness: liveness, Score: t.DiditScore, SessionID: t.DiditSessionID, En: t.DiditEn}
	}

	return body
}

func (s *Server) listarTiendas(w http.ResponseWriter, r *http.Request, _ Sesion) {
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))
	if estado == "todas" || estado == "todos" {
		estado = ""
	}
	tiendas, err := s.datos.TiendasPanel(r.Context(), estado, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		api.Fail(w, err)

		return
	}

	lista := make([]tiendaBody, 0, len(tiendas))
	for _, tienda := range tiendas {
		lista = append(lista, cuerpoTienda(tienda))
	}
	api.JSON(w, http.StatusOK, map[string]any{"tiendas": lista})
}

func (s *Server) fichaTienda(w http.ResponseWriter, r *http.Request, _ Sesion) {
	s.responderTienda(w, r, http.StatusOK, r.PathValue("id"))
}

func (s *Server) responderTienda(w http.ResponseWriter, r *http.Request, status int, id string) {
	tienda, ok, err := s.datos.TiendaPanelPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !ok {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una tienda con ese identificador."))

		return
	}

	body := cuerpoTienda(tienda)
	billetera, err := s.datos.MovimientosDe(r.Context(), tienda.WalletAddress)
	if err != nil {
		api.Fail(w, err)

		return
	}
	body.Billetera = &billeteraBody{WalletAddress: opcional(tienda.WalletAddress), SaldoCOP: billetera.Saldo,
		Movimientos: []movimientoBody{}}
	for _, m := range billetera.Movimientos {
		body.Billetera.Movimientos = append(body.Billetera.Movimientos, movimientoBody{Tipo: m.Tipo, Monto: m.Monto, En: m.En})
	}
	historial, err := s.datos.HistorialDe(r.Context(), "tienda", tienda.ID)
	if err != nil {
		api.Fail(w, err)

		return
	}
	body.Historial = []historialBody{}
	for _, h := range historial {
		body.Historial = append(body.Historial, historialBody{Ts: h.Ts, Actor: h.Actor, Accion: h.Accion, Motivo: h.Motivo})
	}

	api.JSON(w, status, body)
}

type registrarTenderoBody struct {
	NombreTienda    string  `json:"nombre_tienda"`
	Direccion       string  `json:"direccion"`
	Lat             float64 `json:"lat"`
	Lon             float64 `json:"lon"`
	Nivel           string  `json:"nivel"`
	WhatsappTendero string  `json:"whatsapp_tendero"`
	NombreTendero   string  `json:"nombre_tendero"`
	Motivo          string  `json:"motivo"`
}

// registrarTendero creates a store in pendiente_verificacion with its Open Payments
// wallet. The storekeeper activates it by verifying his identity with the agent.
func (s *Server) registrarTendero(w http.ResponseWriter, r *http.Request, sesion Sesion) {
	var body registrarTenderoBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

		return
	}
	body.NombreTienda, body.Direccion = strings.TrimSpace(body.NombreTienda), strings.TrimSpace(body.Direccion)
	body.NombreTendero, body.Motivo = strings.TrimSpace(body.NombreTendero), strings.TrimSpace(body.Motivo)
	body.WhatsappTendero = strings.TrimPrefix(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(body.WhatsappTendero)), "+")

	switch {
	case body.NombreTienda == "" || body.Direccion == "" || body.NombreTendero == "":
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Faltan el nombre de la tienda, la dirección o el nombre del tendero."))
		return
	case body.Nivel != "basico" && body.Nivel != "comercio" && body.Nivel != "aliado":
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El nivel debe ser basico, comercio o aliado."))
		return
	case !whatsappValido.MatchString(body.WhatsappTendero):
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El WhatsApp del tendero debe ser un celular colombiano: 57 y diez dígitos."))
		return
	case math.Abs(body.Lat) > 90 || math.Abs(body.Lon) > 180 || (body.Lat == 0 && body.Lon == 0):
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "La ubicación de la tienda no es válida."))
		return
	case utf8.RuneCountInString(body.Motivo) < motivoMinimo:
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El motivo debe tener al menos %d caracteres.", motivoMinimo))
		return
	}

	// The agent recognizes a person before a storekeeper, so a number that is already a
	// client would never reach the store's verification.
	if _, esPersona, err := s.datos.PersonaPorWhatsapp(r.Context(), body.WhatsappTendero); err != nil {
		api.Fail(w, err)

		return
	} else if esPersona {
		api.Fail(w, api.Errorf(api.CodeYaRegistrada, "Ese WhatsApp ya es de un cliente; el tendero necesita otro número."))

		return
	}

	id, err := nuevoID("tie_")
	if err != nil {
		api.Fail(w, err)

		return
	}
	wallet := ""
	if s.billeteras != nil {
		wallet, err = s.billeteras.CrearBilletera(r.Context(), id, body.NombreTienda)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo crear la billetera de la tienda en Open Payments."))

			return
		}
	}

	err = s.datos.CrearTiendaPendiente(r.Context(), store.TiendaNueva{
		ID: id, Nombre: body.NombreTienda, Direccion: body.Direccion, Lat: body.Lat, Lon: body.Lon, Nivel: body.Nivel,
		WalletAddress: wallet, Whatsapp: body.WhatsappTendero, NombreTendero: body.NombreTendero,
	})
	if errors.Is(err, store.ErrTenderoExistente) {
		api.Fail(w, api.Errorf(api.CodeYaRegistrada, "Ese WhatsApp ya es tendero de otra tienda."))

		return
	}
	if err != nil {
		api.Fail(w, err)

		return
	}

	s.auditar(r.Context(), sesion, "registrar_tendero", "tienda", id, body.Motivo,
		map[string]any{"nivel": body.Nivel, "wallet": wallet})
	s.avisar(body.WhatsappTendero, "Hola "+body.NombreTendero+", registramos tu tienda "+body.NombreTienda+
		" en Caja de Barrio. Para activarla, escríbeme por aquí y verificamos tu identidad.")

	s.responderTienda(w, r, http.StatusCreated, id)
}

// cambiarEstadoTienda activates (by hand, for example after a manual review) or
// suspends a store. The panel says "suspendida"; the schema calls it inactiva.
func (s *Server) cambiarEstadoTienda(w http.ResponseWriter, r *http.Request, sesion Sesion) {
	var body struct {
		Estado string `json:"estado"`
		Motivo string `json:"motivo"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(body.Motivo)) < motivoMinimo {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El motivo debe tener al menos %d caracteres.", motivoMinimo))

		return
	}

	var desde []string
	var a, aviso string
	switch body.Estado {
	case "activa":
		desde, a = []string{"pendiente_verificacion", "revision_manual", "inactiva"}, "activa"
		aviso = "¡Tu tienda quedó activa en Caja de Barrio! Ya puedes publicar tu efectivo del día."
	case "suspendida", "inactiva":
		desde, a = []string{"activa", "pendiente_verificacion", "revision_manual"}, "inactiva"
		aviso = "Tu tienda quedó suspendida en Caja de Barrio por ahora. Si tienes dudas, escríbenos por aquí."
	default:
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El estado debe ser activa o suspendida."))

		return
	}

	id := r.PathValue("id")
	tienda, existe, err := s.datos.TiendaPanelPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !existe {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una tienda con ese identificador."))

		return
	}
	movida, err := s.datos.CambiarEstadoTienda(r.Context(), id, desde, a)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !movida {
		api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La tienda está %s; no se puede pasar a %s.", tienda.Estado, a))

		return
	}

	s.auditar(r.Context(), sesion, "estado_tienda", "tienda", id, strings.TrimSpace(body.Motivo),
		map[string]any{"estado_anterior": tienda.Estado, "estado_nuevo": a})
	s.avisar(tienda.WhatsappTendero, aviso)

	s.responderTienda(w, r, http.StatusOK, id)
}
