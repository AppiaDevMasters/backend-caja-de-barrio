package pagos

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// cotizarTestnet answers 8.5 in testnet mode: ONE quote from the client's wallet to the
// store's incoming payment, by receiver and without an amount, so the store gets the
// exact pesos it asked for and the debit comes out in the client's own asset (USD → COP).
func cotizarTestnet(w http.ResponseWriter, r *http.Request, deps Dependencias, reserva store.ReservaRetiro) {
	quote, err := deps.OpenPayments.CreateQuoteByReceiver(r.Context(), reserva.WalletCliente, *reserva.IncomingPaymentURL)
	if err != nil {
		slog.Error("pagos: creating the testnet quote", "reserva_id", reserva.ID, "error", err)
		api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo cotizar el pago en el testnet."))

		return
	}
	debito, err := enteros(quote.DebitAmount)
	if err != nil {
		api.Fail(w, err)

		return
	}
	recibe, err := enteros(quote.ReceiveAmount)
	if err != nil {
		api.Fail(w, err)

		return
	}
	vence, err := time.Parse(time.RFC3339, quote.ExpiresAt)
	if err != nil {
		vence = time.Now().Add(5 * time.Minute)
	}
	// A new quote needs a new APROBAR: GuardarCotizacion forgets the previous approval.
	if err := deps.Datos.GuardarCotizacion(r.Context(), reserva.ID, quote.URL, "", debito, quote.DebitAmount.AssetCode, vence); err != nil {
		api.Fail(w, err)

		return
	}

	api.JSON(w, http.StatusCreated, map[string]any{
		"quote_url": quote.URL, "quote_plataforma_url": nil,
		"debito": debito, "moneda": quote.DebitAmount.AssetCode,
		"recibe": recibe, "moneda_recibe": quote.ReceiveAmount.AssetCode,
		"debit_amount": montoBody(quote.DebitAmount), "receive_amount": montoBody(quote.ReceiveAmount),
		"vence_en": vence.UTC().Format(time.RFC3339),
		"desglose_cop": map[string]int64{
			"retiro": reserva.MontoCOP, "comision_tendero": reserva.ComisionTenderoCOP,
			"comision_caja_de_barrio": reserva.ComisionPlataformaCOP, "total": reserva.TotalCOP(),
		},
		"modo": "testnet",
	})
}

// pagarTestnet answers 8.7 in testnet mode: ONE outgoing payment from the client's real
// testnet wallet. There is no virtual wallet to check and no liquidity to put up: the
// money is in the client's wallet, and if it is not, the payment says so.
func pagarTestnet(w http.ResponseWriter, r *http.Request, deps Dependencias, reserva store.ReservaRetiro,
	cupo store.CupoGrant, token opclient.AccessToken) {
	pago, rotado, err := deps.OpenPayments.CreateOutgoingPayment(r.Context(), reserva.WalletCliente, *reserva.QuoteURL, token)
	if rotado.Value != "" {
		guardarToken(r.Context(), deps, cupo.PersonaID, rotado)
	}
	if pago.URL == "" {
		slog.Error("pagos: creating the testnet payment", "reserva_id", reserva.ID, "error", err)
		mensaje := "No se pudo enviar el pago por Open Payments."
		if sinFondos(err) {
			mensaje = "La billetera del cliente no tiene fondos suficientes para este pago."
		}
		api.JSON(w, http.StatusOK, map[string]any{"estado": "fallido", "error": mensaje})

		return
	}
	if err != nil {
		// The payment exists and only the rotation failed: the money moved, so the
		// withdrawal goes on and the next payment rotates from the stored token.
		slog.Warn("pagos: the testnet payment was made but its token did not rotate", "reserva_id", reserva.ID, "error", err)
	}

	resultado := map[string]any{
		"estado": "pendiente", "outgoing_payment_id": pago.URL, "outgoing_plataforma_id": nil,
		"cupo_disponible": cupo.Limite - cupo.Usado - *reserva.Debito,
		"debit_amount":    montoBody(pago.DebitAmount), "receive_amount": montoBody(pago.ReceiveAmount),
		"modo": "testnet",
	}
	if err := deps.Datos.GuardarPagos(r.Context(), reserva.ID, pago.URL, "", resultado); err != nil {
		api.Fail(w, err)

		return
	}

	reserva.OutgoingPaymentID = &pago.URL
	final, err := esperarYCerrar(r.Context(), deps, reserva)
	if err != nil {
		slog.Error("pagos: waiting for the testnet collection point", "reserva_id", reserva.ID, "error", err)
	}
	if final != nil {
		final["debit_amount"], final["receive_amount"], final["modo"] = resultado["debit_amount"], resultado["receive_amount"], "testnet"
		resultado = final
	}
	api.JSON(w, http.StatusOK, resultado)
}
