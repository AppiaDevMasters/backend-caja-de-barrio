package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// resumen answers GET /admin/resumen with the shape the panel's home page reads.
func (s *Server) resumen(w http.ResponseWriter, r *http.Request, _ Sesion) {
	datos, err := s.datos.ResumenPanel(r.Context())
	if err != nil {
		api.Fail(w, err)

		return
	}

	horas := make([]string, 0, len(datos.PorHora))
	for h := range datos.PorHora {
		horas = append(horas, h)
	}
	sort.Strings(horas)
	porHora := make([]map[string]any, 0, len(horas))
	for _, h := range horas {
		porHora = append(porHora, map[string]any{"h": h, "n": datos.PorHora[h]})
	}

	api.JSON(w, http.StatusOK, map[string]any{
		"kpis": []map[string]any{
			{"key": "retiros", "label": "Retiros completados", "value": datos.RetirosEntregadosHoy, "icon": "check", "to": "/retiros?estado=entregada", "hint": "Hoy"},
			{"key": "monto", "label": "Monto entregado", "value": datos.MontoEntregadoHoy, "format": "cop", "icon": "wallet", "to": "/retiros?estado=entregada", "hint": "Hoy"},
			{"key": "pendientes", "label": "Registros por revisar", "value": datos.RegistrosPorRevisar, "icon": "users", "to": "/personas?estado=revision_manual", "hint": "Cola de revisión manual"},
			{"key": "alertas", "label": "Alertas abiertas", "value": datos.AlertasAbiertas, "icon": "alert", "tone": "danger", "to": "/alertas?estado=pendientes", "hint": "Abiertas", "role": "alertas"},
			{"key": "tiendas", "label": "Tiendas activas", "value": datos.TiendasActivas, "icon": "store", "to": "/tiendas?estado=activa", "hint": "de " + itoa(datos.TiendasTotal) + " registradas"},
			{"key": "efectivo", "label": "Efectivo disponible publicado", "value": datos.EfectivoLibre, "format": "cop", "icon": "banknote", "to": "/tiendas?estado=activa", "hint": "Publicado menos reservado"},
		},
		"porHora": porHora,
	})
}

func retiroResumen(r store.RetiroPanel) map[string]any {
	return map[string]any{
		"id": r.ID, "estado": r.Estado, "tienda_id": r.TiendaID, "nombre_tienda": r.NombreTienda,
		"persona_id": r.PersonaID, "nombre_cliente": r.NombreCliente, "monto_cop": r.MontoCOP,
		"comision_tendero_cop": r.ComisionTenderoCOP, "comision_plataforma_cop": r.ComisionPlataformaCOP,
		"total_cop": r.MontoCOP + r.ComisionTenderoCOP + r.ComisionPlataformaCOP,
		"riesgo_ok": r.RiesgoOK, "face_match_ok": r.FaceMatchOK, "creada_en": r.CreadaEn, "vence_en": r.VenceEn,
		"pagada_en": r.PagadaEn, "entregado_en": r.EntregadoEn, "cancelada_motivo": r.MotivoCancelacion,
	}
}

func (s *Server) listarRetiros(w http.ResponseWriter, r *http.Request, _ Sesion) {
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))
	if estado == "todas" || estado == "todos" {
		estado = ""
	}
	retiros, err := s.datos.RetirosPanel(r.Context(), estado, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		api.Fail(w, err)

		return
	}
	lista := make([]map[string]any, 0, len(retiros))
	for _, retiro := range retiros {
		lista = append(lista, retiroResumen(retiro))
	}
	api.JSON(w, http.StatusOK, map[string]any{"retiros": lista})
}

// paso is one step of the withdrawal's timeline (panel-de-control-v2.md §3.1).
func paso(key, label string, hecho bool, ts *time.Time, ref *string, detalle string) map[string]any {
	estado := "pendiente"
	if hecho {
		estado = "ok"
	}
	var marca any
	if ts != nil {
		marca = ts
	}
	var referencia any
	if ref != nil && *ref != "" {
		referencia = *ref
	}

	return map[string]any{"key": key, "label": label, "estado": estado, "ts": marca, "ref": referencia, "detalle": detalle}
}

// fichaRetiro answers GET /admin/retiros/{id}: the withdrawal, its nine-step timeline with
// the Open Payments ids, and every event the tables recorded.
func (s *Server) fichaRetiro(w http.ResponseWriter, r *http.Request, _ Sesion) {
	s.responderRetiro(w, r, r.PathValue("id"))
}

func (s *Server) responderRetiro(w http.ResponseWriter, r *http.Request, id string) {
	reserva, existe, err := s.datos.ReservaRetiroPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !existe {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe ese retiro."))

		return
	}
	extra, err := s.datos.RetiroExtraDe(r.Context(), reserva.ID)
	if err != nil {
		api.Fail(w, err)

		return
	}

	creada := reserva.CreadaEn
	pagada := reserva.Estado == "pagada" || reserva.Estado == "entregada" || reserva.Estado == "en_disputa"
	cotizacion := ""
	if reserva.Debito != nil && reserva.Moneda != nil {
		cotizacion = "Débito de " + strings.TrimSuffix(strings.TrimSuffix(jsonNumero(*reserva.Debito), ".0"), ".00") + " " + *reserva.Moneda
	}
	aprobacion := ""
	if extra.AprobacionTexto != nil {
		aprobacion = "La persona escribió «" + *extra.AprobacionTexto + "»."
	}
	timeline := []map[string]any{
		paso("identidad", "Verificación de identidad (Didit)", reserva.FaceMatchOK, nil, extra.SesionDidit, "Prueba de vida y comparación facial aprobadas."),
		paso("reserva", "Reserva de efectivo", true, &creada, &reserva.ID, "Efectivo apartado en "+reserva.NombreTienda+"."),
		paso("riesgo", "Evaluación de riesgo", reserva.RiesgoOK, nil, nil, "Tope por nivel y regla de pitufeo."),
		paso("cobro", "Cobros creados", reserva.IncomingPaymentURL != nil, nil, reserva.IncomingPaymentURL, "Incoming payments de la tienda y de Caja de Barrio."),
		paso("cotizacion", "Cotización", reserva.QuoteURL != nil, nil, reserva.QuoteURL, cotizacion),
		paso("aprobacion", "Aprobación en el chat", reserva.AprobacionEn != nil, reserva.AprobacionEn, extra.AprobacionID, aprobacion),
		paso("pago", "Pago completado", pagada, reserva.PagadaEn, reserva.OutgoingPaymentID, "Outgoing payment por Open Payments."),
		paso("entrega_tendero", "Entrega confirmada por el tendero", reserva.EntregaTenderoEn != nil, reserva.EntregaTenderoEn, nil, "Con el código de 4 dígitos."),
		paso("recibido_cliente", "Recibido confirmado por el cliente", reserva.RecibidoClienteEn != nil, reserva.RecibidoClienteEn, nil, ""),
	}
	if reserva.Estado == "cancelada" || reserva.Estado == "vencida" {
		for _, p := range timeline {
			if p["estado"] == "pendiente" {
				p["estado"] = "omitido"
			}
		}
	}

	eventos := make([]map[string]any, 0, len(extra.Eventos))
	for _, e := range extra.Eventos {
		eventos = append(eventos, map[string]any{"ts": e.Ts, "tipo": e.Tipo, "actor": e.Actor, "detalle": e.Detalle})
	}

	api.JSON(w, http.StatusOK, map[string]any{
		"id": reserva.ID, "estado": reserva.Estado, "tienda_id": reserva.TiendaID, "nombre_tienda": reserva.NombreTienda,
		"persona_id": reserva.PersonaID, "nombre_cliente": reserva.NombreCliente,
		"monto_cop": reserva.MontoCOP, "comision_tendero_cop": reserva.ComisionTenderoCOP,
		"comision_plataforma_cop": reserva.ComisionPlataformaCOP, "total_cop": reserva.TotalCOP(),
		"riesgo_ok": reserva.RiesgoOK, "face_match_ok": reserva.FaceMatchOK,
		"creada_en": reserva.CreadaEn, "vence_en": reserva.VenceEn, "pagada_en": reserva.PagadaEn,
		"entregado_en": extra.EntregadoEn, "cancelada_motivo": extra.MotivoCancelacion,
		"wallet_cliente": reserva.WalletCliente, "wallet_tienda": reserva.WalletTienda,
		"open_payments": map[string]any{
			"incoming_payment_url": reserva.IncomingPaymentURL, "cobro_plataforma_url": reserva.CobroPlataformaURL,
			"quote_url": reserva.QuoteURL, "quote_plataforma_url": reserva.QuotePlataformaURL,
			"outgoing_payment_id": reserva.OutgoingPaymentID, "outgoing_plataforma_id": reserva.OutgoingPlataformaID,
		},
		"remesa": nil, "disputa": nil,
		"timeline": timeline,
		"eventos":  eventos,
	})
}

// cancelarRetiro answers POST /admin/retiros/{id}/cancelar: only a reservation that has
// not paid anything, and its cash goes back to the store.
func (s *Server) cancelarRetiro(w http.ResponseWriter, r *http.Request, sesion Sesion) {
	motivo, ok := leerMotivo(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	reserva, existe, err := s.datos.ReservaRetiroPorID(r.Context(), id)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !existe {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe ese retiro."))

		return
	}
	if err := s.datos.CancelarReserva(r.Context(), id, motivo); err != nil {
		var otro store.ErrReservaEnOtroEstado
		if errors.As(err, &otro) {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No se puede cancelar: la reserva está %s.", otro.Estado))

			return
		}
		api.Fail(w, err)

		return
	}

	s.auditar(r.Context(), sesion, "cancelar_reserva", "reserva", id, motivo, map[string]any{"estado_anterior": reserva.Estado})
	s.avisar(reserva.WhatsappCliente, "Tu retiro en "+reserva.NombreTienda+" fue cancelado por el equipo de Caja de Barrio. "+
		"No se te cobró nada. Si quieres, puedes pedir otro retiro por aquí.")
	s.responderRetiro(w, r, id)
}

// saldos answers GET /admin/saldos (every virtual wallet) and GET /admin/saldos?wallet=
// (one wallet with its movements, the same shape as /internal/pagos/saldo).
func (s *Server) saldos(w http.ResponseWriter, r *http.Request, _ Sesion) {
	if wallet := strings.TrimSpace(r.URL.Query().Get("wallet")); wallet != "" {
		billetera, err := s.datos.MovimientosDe(r.Context(), wallet)
		if err != nil {
			api.Fail(w, err)

			return
		}
		movimientos := make([]movimientoBody, 0, len(billetera.Movimientos))
		for _, m := range billetera.Movimientos {
			movimientos = append(movimientos, movimientoBody{Tipo: m.Tipo, Monto: m.Monto, En: m.En})
		}
		api.JSON(w, http.StatusOK, map[string]any{"wallet_address": wallet, "saldo": billetera.Saldo, "moneda": "COP", "movimientos": movimientos})

		return
	}

	billeteras, err := s.datos.SaldosPanel(r.Context())
	if err != nil {
		api.Fail(w, err)

		return
	}
	lista := make([]map[string]any, 0, len(billeteras))
	for _, b := range billeteras {
		lista = append(lista, map[string]any{
			"wallet_address": b.WalletAddress, "saldo_cop": b.SaldoCOP, "ultimo_movimiento_en": b.UltimoMovimientoEn,
			"duenio": map[string]any{"tipo": b.Tipo, "id": b.DuenioID, "nombre": b.Nombre},
		})
	}
	api.JSON(w, http.StatusOK, map[string]any{"billeteras": lista})
}

// listarAlertas answers GET /admin/alertas?estado=&regla=. The panel's filters
// "pendientes" and "cerradas" map to the stored abierta and resuelta.
func (s *Server) listarAlertas(w http.ResponseWriter, r *http.Request, _ Sesion) {
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))
	switch estado {
	case "pendientes", "abierta":
		estado = "abierta"
	case "cerradas", "resuelta":
		estado = "resuelta"
	default:
		estado = ""
	}
	regla := strings.TrimSpace(r.URL.Query().Get("regla"))
	if regla == "todas" {
		regla = ""
	}

	alertas, err := s.datos.AlertasPanel(r.Context(), estado, regla)
	if err != nil {
		api.Fail(w, err)

		return
	}
	lista := make([]map[string]any, 0, len(alertas))
	for _, a := range alertas {
		lista = append(lista, map[string]any{
			"id": a.ID, "regla": a.Regla, "detalle": a.Detalle, "estado": a.Estado, "persona_id": a.PersonaID,
			"nombre_persona": a.NombrePersona, "tienda_id": a.TiendaID, "nombre_tienda": a.NombreTienda,
			"creada_en": a.CreadaEn, "resolucion": a.Resolucion, "resuelta_en": a.ResueltaEn, "uiaf": false,
		})
	}
	api.JSON(w, http.StatusOK, map[string]any{"alertas": lista})
}

// resolverAlerta answers POST /admin/alertas/{id}/resolver {accion, motivo}.
func (s *Server) resolverAlerta(w http.ResponseWriter, r *http.Request, sesion Sesion) {
	var body struct {
		Accion string `json:"accion"`
		Motivo string `json:"motivo"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil || strings.TrimSpace(body.Accion) == "" {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Faltan la acción y el motivo."))

		return
	}
	motivo := strings.TrimSpace(body.Motivo)
	if utf8.RuneCountInString(motivo) < motivoMinimo {
		api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El motivo debe tener al menos %d caracteres.", motivoMinimo))

		return
	}

	id := r.PathValue("id")
	resuelta, err := s.datos.ResolverAlerta(r.Context(), id, strings.TrimSpace(body.Accion))
	if err != nil {
		api.Fail(w, err)

		return
	}
	if !resuelta {
		api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La alerta no existe o ya estaba resuelta."))

		return
	}
	s.auditar(r.Context(), sesion, "resolver_alerta", "alerta", id, motivo, map[string]any{"accion": body.Accion})
	api.JSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "estado": "resuelta", "resolucion": body.Accion})
}

// auditoria answers GET /admin/auditoria?actor=&accion=&entidad=&q=.
func (s *Server) auditoria(w http.ResponseWriter, r *http.Request, _ Sesion) {
	limpiar := func(nombre string) string {
		v := strings.TrimSpace(r.URL.Query().Get(nombre))
		if v == "todos" || v == "todas" {
			return ""
		}

		return v
	}
	entradas, err := s.datos.AuditoriaPanel(r.Context(), limpiar("actor"), limpiar("accion"), limpiar("entidad"), limpiar("q"))
	if err != nil {
		api.Fail(w, err)

		return
	}
	lista := make([]map[string]any, 0, len(entradas))
	for _, e := range entradas {
		var detalle map[string]any
		_ = json.Unmarshal(e.Detalle, &detalle)
		lista = append(lista, map[string]any{
			"id": e.ID, "ts": e.Ts, "actor": e.Actor, "accion": e.Accion, "entidad_tipo": e.Entidad,
			"entidad_id": e.EntidadID, "entidad_nombre": e.EntidadID, "motivo": e.Motivo,
			"antes": detalle["estado_anterior"], "despues": detalle["estado_nuevo"], "detalle": detalle,
		})
	}
	api.JSON(w, http.StatusOK, map[string]any{"auditoria": lista})
}

func jsonNumero(f float64) string {
	b, _ := json.Marshal(f)

	return string(b)
}
