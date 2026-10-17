package pagos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
)

// The risk rules of the contract (the same values as the agent's reference).
const (
	ventanaPitufeo      = 60 * time.Minute
	maxRetirosEnVentana = 3
	topePersonaNivelUno = 500_000
	topePersonaNivelDos = 2_000_000
)

func topePersona(nivel int) int64 {
	if nivel >= 2 {
		return topePersonaNivelDos
	}

	return topePersonaNivelUno
}

// consultarReserva answers 7.2: the reservation and which steps of the withdrawal are done.
func consultarReserva(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reserva, ok := leerReserva(w, r, deps, r.PathValue("id"))
		if !ok {
			return
		}

		api.JSON(w, http.StatusOK, map[string]any{
			"reserva_id": reserva.ID, "estado": reserva.Estado, "tienda_id": reserva.TiendaID,
			"nombre_tienda": reserva.NombreTienda, "persona_id": reserva.PersonaID, "nombre_cliente": reserva.NombreCliente,
			"whatsapp_cliente": reserva.WhatsappCliente, "whatsapp_tendero": reserva.WhatsappTendero,
			"wallet_tienda": reserva.WalletTienda, "wallet_cliente": reserva.WalletCliente,
			"monto_cop": reserva.MontoCOP, "comision_tendero_cop": reserva.ComisionTenderoCOP,
			"comision_plataforma_cop": reserva.ComisionPlataformaCOP, "total_cop": reserva.TotalCOP(),
			"vence_en":             reserva.VenceEn.UTC().Format(time.RFC3339),
			"incoming_payment_url": reserva.IncomingPaymentURL,
			"pasos": map[string]bool{
				"identidad_verificada":        reserva.FaceMatchOK,
				"riesgo_ok":                   reserva.RiesgoOK,
				"cobro_creado":                reserva.IncomingPaymentURL != nil,
				"cotizado":                    reserva.QuoteURL != nil,
				"aprobado_en_chat":            reserva.AprobacionEn != nil,
				"pagada":                      reserva.Estado == "pagada" || reserva.Estado == "entregada",
				"entrega_confirmada_tendero":  reserva.EntregaTenderoEn != nil,
				"recibido_confirmado_cliente": reserva.RecibidoClienteEn != nil,
				"entregada":                   reserva.Estado == "entregada",
			},
		})
	}
}

// evaluarRiesgo answers 7.3: the ceiling of the person's level and the smurfing rule.
func evaluarRiesgo(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PersonaID   string `json:"persona_id"`
			MontoCOP    int64  `json:"monto_cop"`
			Dispositivo string `json:"dispositivo"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

			return
		}
		reserva, ok := leerReserva(w, r, deps, r.PathValue("id"))
		if !ok {
			return
		}
		bloquear := func(motivo string) {
			_ = deps.Datos.MarcarRiesgo(r.Context(), reserva.ID, false)
			api.JSON(w, http.StatusOK, map[string]any{"decision": "bloquear", "motivo": motivo})
		}

		switch {
		case reserva.Estado != "reservada":
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La reserva está %s.", reserva.Estado))
			return
		case strings.TrimSpace(body.PersonaID) != reserva.PersonaID || (body.MontoCOP != 0 && body.MontoCOP != reserva.MontoCOP):
			bloquear("Los datos no coinciden con la reserva.")
			return
		case reserva.EstadoCliente == "bloqueada":
			bloquear("La cuenta está bloqueada.")
			return
		case reserva.MontoCOP > topePersona(reserva.NivelCliente):
			bloquear("El monto supera el tope de su nivel.")
			return
		}

		recientes, err := deps.Datos.RetirosPagadosDesde(r.Context(), reserva.PersonaID, time.Now().Add(-ventanaPitufeo))
		if err != nil {
			api.Fail(w, err)

			return
		}
		if recientes >= maxRetirosEnVentana {
			id, err := nuevoID("ale_")
			if err == nil {
				err = deps.Datos.BloquearPorRiesgo(r.Context(), id, reserva.PersonaID, reserva.TiendaID, "pitufeo",
					fmt.Sprintf("%d retiros en 60 min", recientes))
			}
			if err != nil {
				api.Fail(w, err)

				return
			}
			bloquear("Demasiados retiros en poco tiempo; la cuenta queda en revisión.")

			return
		}

		if err := deps.Datos.MarcarRiesgo(r.Context(), reserva.ID, true); err != nil {
			api.Fail(w, err)

			return
		}
		api.JSON(w, http.StatusOK, map[string]any{"decision": "permitir", "motivo": "ok"})
	}
}
