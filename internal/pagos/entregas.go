package pagos

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

func fallaEntrega(w http.ResponseWriter, err error, que string) {
	var otro store.ErrReservaEnOtroEstado
	if errors.As(err, &otro) {
		api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No se puede %s: la reserva está %s.", que, otro.Estado))

		return
	}
	api.Fail(w, err)
}

// entregaTendero answers 7.6: the storekeeper hands over the cash and confirms it with
// the person's four-digit code. Only a paid reservation whose collection point is
// complete can be delivered (rule 3: the delivery is confirmed by both parties).
func entregaTendero(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Codigo string `json:"codigo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Codigo) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta el código de 4 dígitos."))

			return
		}
		reserva, ok := leerReserva(w, r, deps, r.PathValue("id"))
		if !ok {
			return
		}
		if reserva.Estado != "pagada" {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No se puede entregar: la reserva está %s, no pagada.", reserva.Estado))

			return
		}
		if reserva.IncomingPaymentURL == nil || deps.Cobros == nil {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No se puede entregar: la reserva no tiene cobro."))

			return
		}
		recibido, escala, err := deps.Cobros(r.Context(), *reserva.IncomingPaymentURL)
		if err != nil {
			slog.Error("pagos: reading the collection point before a delivery", "reserva_id", reserva.ID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo confirmar el cobro de la tienda."))

			return
		}
		if float64(recibido)/math.Pow10(escala) < float64(reserva.MontoCOP+reserva.ComisionTenderoCOP) {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No se puede entregar: el cobro de la tienda no figura completado."))

			return
		}
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(body.Codigo)), []byte(reserva.Codigo)) != 1 {
			api.Fail(w, api.Errorf(api.CodeCodigoIncorrecto, "El código no coincide."))

			return
		}

		id, err := nuevoID("con_")
		if err != nil {
			api.Fail(w, err)

			return
		}
		entrega, err := deps.Datos.ConfirmarEntrega(r.Context(), id, reserva.ID, "tendero", true, "", "")
		if err != nil {
			fallaEntrega(w, err, "entregar")

			return
		}

		api.JSON(w, http.StatusOK, map[string]any{"estado": entrega.Estado, "falta_confirmacion_cliente": entrega.FaltaCliente})
	}
}

// recibidoCliente answers 7.7: the person says, in her own words, whether she
// received the cash. A no opens a dispute and an alert.
func recibidoCliente(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Recibido     *bool  `json:"recibido"`
			TextoUsuario string `json:"texto_usuario"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Recibido == nil || strings.TrimSpace(body.TextoUsuario) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Faltan recibido y el texto que escribió la persona."))

			return
		}
		reserva, ok := leerReserva(w, r, deps, r.PathValue("id"))
		if !ok {
			return
		}

		id, err := nuevoID("con_")
		if err != nil {
			api.Fail(w, err)

			return
		}
		alerta, err := nuevoID("ale_")
		if err != nil {
			api.Fail(w, err)

			return
		}
		entrega, err := deps.Datos.ConfirmarEntrega(r.Context(), id, reserva.ID, "cliente", *body.Recibido, body.TextoUsuario, alerta)
		if err != nil {
			fallaEntrega(w, err, "confirmar el recibido")

			return
		}

		respuesta := map[string]any{"estado": entrega.Estado}
		if entrega.Estado != "en_disputa" {
			respuesta["falta_confirmacion_tendero"] = entrega.FaltaTendero
		}
		api.JSON(w, http.StatusOK, respuesta)
	}
}

// cancelar answers 7.5: a reservation that has not paid anything is cancelled and its
// cash goes back to the store.
func cancelar(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Motivo string `json:"motivo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Motivo) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta el motivo de la cancelación."))

			return
		}
		if err := deps.Datos.CancelarReserva(r.Context(), r.PathValue("id"), strings.TrimSpace(body.Motivo)); err != nil {
			var otro store.ErrReservaEnOtroEstado
			if errors.As(err, &otro) && otro.Estado == "inexistente" {
				api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe esa reserva."))

				return
			}
			fallaEntrega(w, err, "cancelar")

			return
		}

		api.JSON(w, http.StatusOK, map[string]any{"ok": true, "motivo": strings.TrimSpace(body.Motivo)})
	}
}

// reservasDeTienda answers 7.4: the open withdrawals a storekeeper is waiting for.
func reservasDeTienda(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reservas, err := deps.Datos.ReservasActivasDeTienda(r.Context(), r.PathValue("tienda_id"))
		if err != nil {
			api.Fail(w, err)

			return
		}
		lista := make([]map[string]any, 0, len(reservas))
		for _, reserva := range reservas {
			lista = append(lista, map[string]any{
				"reserva_id": reserva.ID, "estado": reserva.Estado, "monto_cop": reserva.MontoCOP,
				"comision_tendero_cop": reserva.ComisionTenderoCOP, "cliente": reserva.Cliente,
			})
		}
		api.JSON(w, http.StatusOK, map[string]any{"reservas": lista})
	}
}
