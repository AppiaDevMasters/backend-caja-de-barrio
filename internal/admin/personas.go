package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

type diditBody struct {
	Liveness  string     `json:"liveness"`
	Score     *float64   `json:"score"`
	SessionID *string    `json:"session_id"`
	En        *time.Time `json:"en"`
}

// verifikBody is what the registry said about the document, as far as the record keeps
// it: an approved registration was confirmed, a manual review carries the reason.
type verifikBody struct {
	Valida           *bool   `json:"valida"`
	NombreCoincide   *bool   `json:"nombre_coincide"`
	NombreRegistrado *string `json:"nombre_registrado"`
	Error            *string `json:"error"`
}

type movimientoBody struct {
	Tipo  string    `json:"tipo"`
	Monto int64     `json:"monto"`
	En    time.Time `json:"en"`
}

type billeteraBody struct {
	WalletAddress *string          `json:"wallet_address"`
	SaldoCOP      int64            `json:"saldo_cop"`
	Movimientos   []movimientoBody `json:"movimientos"`
}

type historialBody struct {
	Ts     time.Time `json:"ts"`
	Actor  string    `json:"actor"`
	Accion string    `json:"accion"`
	Motivo *string   `json:"motivo"`
}

type personaBody struct {
	ID             string       `json:"id"`
	Nombre         string       `json:"nombre"`
	Cedula         string       `json:"cedula"`
	Whatsapp       string       `json:"whatsapp"`
	EstadoRegistro string       `json:"estado_registro"`
	CreadaEn       time.Time    `json:"creada_en"`
	Wallet         *string      `json:"wallet"`
	SaldoCOP       int64        `json:"saldo_cop"`
	MotivoRevision *string      `json:"motivo_revision"`
	Didit          *diditBody   `json:"didit"`
	Verifik        *verifikBody `json:"verifik,omitempty"`

	// Only in the detail.
	PoliticasVersion     *string         `json:"politicas_version,omitempty"`
	PoliticasAceptadasEn *time.Time      `json:"politicas_aceptadas_en,omitempty"`
	Billetera            *billeteraBody  `json:"billetera,omitempty"`
	Historial            []historialBody `json:"historial,omitempty"`
}

// enmascarar keeps the last four characters: the panel shows identifiers masked and
// revealing one is its own audited action.
func enmascarar(valor, prefijo string) string {
	limpio := strings.TrimSpace(valor)
	if limpio == "" {
		return ""
	}
	runas := []rune(limpio)
	if len(runas) <= 4 {
		return prefijo + string(runas)
	}

	return prefijo + string(runas[len(runas)-4:])
}

func opcional(valor string) *string {
	if valor == "" {
		return nil
	}

	return &valor
}

func cuerpoPersona(p store.PersonaPanel) personaBody {
	body := personaBody{
		ID:             p.ID,
		Nombre:         p.Nombre,
		Cedula:         enmascarar(p.Cedula, "****"),
		Whatsapp:       enmascarar(p.Whatsapp, "+57 *** *** "),
		EstadoRegistro: p.EstadoRegistro,
		CreadaEn:       p.CreadaEn,
		Wallet:         opcional(p.WalletAddress),
		SaldoCOP:       p.SaldoCOP,
		MotivoRevision: p.MotivoRevision,
	}
	if p.DiditSessionID != nil {
		liveness := "pendiente"
		if p.DiditAprobada != nil {
			liveness = "rechazada"
			if *p.DiditAprobada {
				liveness = "aprobada"
			}
		}
		body.Didit = &diditBody{Liveness: liveness, Score: p.DiditScore, SessionID: p.DiditSessionID, En: p.DiditEn}
	}

	return body
}

func (s *Server) listarPersonas(w http.ResponseWriter, r *http.Request, _ Sesion) {
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))
	if estado == "todas" || estado == "todos" {
		estado = ""
	}
	personas, err := s.datos.PersonasPanel(r.Context(), estado, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		api.Fail(w, err)

		return
	}

	lista := make([]personaBody, 0, len(personas))
	for _, persona := range personas {
		lista = append(lista, cuerpoPersona(persona))
	}
	api.JSON(w, http.StatusOK, map[string]any{"personas": lista})
}

func (s *Server) fichaPersona(w http.ResponseWriter, r *http.Request, _ Sesion) {
	s.responderFicha(w, r, r.PathValue("id"))
}

func (s *Server) responderFicha(w http.ResponseWriter, r *http.Request, id string) {
	persona, ok, err := s.datos.PersonaPanelPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !ok {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una persona con ese identificador."))

		return
	}

	body := cuerpoPersona(persona)
	body.PoliticasVersion, body.PoliticasAceptadasEn = persona.PoliticasVersion, persona.PoliticasEn
	switch {
	case persona.EstadoRegistro == "aprobada" && persona.Cedula != "" && persona.MotivoRevision == nil:
		si, nombre := true, persona.Nombre
		body.Verifik = &verifikBody{Valida: &si, NombreCoincide: &si, NombreRegistrado: &nombre}
	case persona.EstadoRegistro == "revision_manual":
		body.Verifik = &verifikBody{Error: persona.MotivoRevision}
	}

	billetera, err := s.datos.MovimientosDe(r.Context(), persona.WalletAddress)
	if err != nil {
		api.Fail(w, err)

		return
	}
	body.Billetera = &billeteraBody{WalletAddress: opcional(persona.WalletAddress), SaldoCOP: billetera.Saldo,
		Movimientos: []movimientoBody{}}
	for _, m := range billetera.Movimientos {
		body.Billetera.Movimientos = append(body.Billetera.Movimientos, movimientoBody{Tipo: m.Tipo, Monto: m.Monto, En: m.En})
	}

	historial, err := s.datos.HistorialDe(r.Context(), "persona", persona.ID)
	if err != nil {
		api.Fail(w, err)

		return
	}
	body.Historial = []historialBody{}
	for _, h := range historial {
		body.Historial = append(body.Historial, historialBody{Ts: h.Ts, Actor: h.Actor, Accion: h.Accion, Motivo: h.Motivo})
	}

	api.JSON(w, http.StatusOK, body)
}

// aprobarPersona approves by hand a registration the automatic path did not approve
// (for example, Verifik did not answer): the same effects as an automatic approval,
// the wallet and its initial load included.
func (s *Server) aprobarPersona(w http.ResponseWriter, r *http.Request, sesion Sesion) {
	id := r.PathValue("id")
	motivo, ok := leerMotivo(w, r)
	if !ok {
		return
	}

	persona, existe, err := s.datos.PersonaPanelPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !existe {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una persona con ese identificador."))

		return
	}
	desde := []string{"revision_manual", "verificando"}
	if persona.EstadoRegistro != desde[0] && persona.EstadoRegistro != desde[1] {
		api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "Solo se aprueba a mano un registro en revisión manual; este está %s.",
			persona.EstadoRegistro))

		return
	}

	// The wallet first: if Rafiki fails, the person stays as she was and the operator
	// can try again.
	wallet := persona.WalletAddress
	if wallet == "" && s.billeteras != nil {
		wallet, err = s.billeteras.CrearBilletera(r.Context(), persona.ID, persona.Nombre)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo crear la billetera en Open Payments. Intenta de nuevo."))

			return
		}
	}

	movida, err := s.datos.CambiarEstadoPersona(r.Context(), persona.ID, desde, "aprobada")
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !movida {
		api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "Otra persona del equipo ya decidió este registro."))

		return
	}
	if wallet != "" && persona.WalletAddress == "" {
		if err := s.datos.AsignarWallet(r.Context(), persona.ID, wallet); err != nil {
			api.Fail(w, err)

			return
		}
	}
	if wallet != "" && s.cfg.SaldoInicial > 0 {
		movimiento, err := nuevoID("mov_")
		if err == nil {
			err = s.datos.RegistrarMovimiento(r.Context(), movimiento, wallet, "carga_inicial", s.cfg.SaldoInicial,
				"aprobacion:"+persona.ID)
		}
		if err != nil {
			api.Fail(w, err)

			return
		}
	}

	s.auditar(r.Context(), sesion, "aprobar_persona", "persona", persona.ID, motivo,
		map[string]any{"estado_anterior": persona.EstadoRegistro, "wallet": wallet})
	aviso := "¡Tu registro en Caja de Barrio quedó aprobado! Ya puedes retirar efectivo en las tiendas del barrio."
	if wallet != "" && s.cfg.SaldoInicial > 0 {
		aviso = "¡Tu registro en Caja de Barrio quedó aprobado! Tu billetera ya está lista con " +
			pesos(s.cfg.SaldoInicial) + ". Escríbeme cuando quieras retirar efectivo."
	}
	s.avisar(persona.Whatsapp, aviso)

	s.responderFicha(w, r, persona.ID)
}

func (s *Server) rechazarPersona(w http.ResponseWriter, r *http.Request, sesion Sesion) {
	id := r.PathValue("id")
	motivo, ok := leerMotivo(w, r)
	if !ok {
		return
	}

	persona, existe, err := s.datos.PersonaPanelPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !existe {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una persona con ese identificador."))

		return
	}

	movida, err := s.datos.CambiarEstadoPersona(r.Context(), persona.ID,
		[]string{"pendiente", "verificando", "revision_manual"}, "rechazada")
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !movida {
		api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "Este registro ya no se puede rechazar; está %s.", persona.EstadoRegistro))

		return
	}

	s.auditar(r.Context(), sesion, "rechazar_persona", "persona", persona.ID, motivo,
		map[string]any{"estado_anterior": persona.EstadoRegistro})
	s.avisar(persona.Whatsapp, "Revisamos tu registro en Caja de Barrio y por ahora no lo pudimos aprobar. "+
		"Si crees que es un error, escríbenos por aquí.")

	s.responderFicha(w, r, persona.ID)
}

// pesos formats an amount the way a person reads it in Colombia: $1.000.000.
func pesos(valor int64) string {
	digitos := []byte(strings.TrimPrefix(itoa(valor), "-"))
	var salida []byte
	for i, d := range digitos {
		if i > 0 && (len(digitos)-i)%3 == 0 {
			salida = append(salida, '.')
		}
		salida = append(salida, d)
	}

	return "$" + string(salida)
}

func itoa(valor int64) string {
	if valor == 0 {
		return "0"
	}
	negativo := valor < 0
	if negativo {
		valor = -valor
	}
	var b []byte
	for valor > 0 {
		b = append([]byte{byte('0' + valor%10)}, b...)
		valor /= 10
	}
	if negativo {
		b = append([]byte{'-'}, b...)
	}

	return string(b)
}
