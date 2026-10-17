package pagos

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
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

// saldoTestnet answers GET /pagos/saldo in testnet mode. The money is in a wallet of
// another ASE, and Open Payments gives a client app no way to read a balance: only the
// wallet's own provider sees it. So the answer says where the money is, says why its
// balance is not shown, and lists what Caja de Barrio does know: the withdrawals paid
// (in the client's asset) or collected (in pesos) through that wallet.
func saldoTestnet(w http.ResponseWriter, r *http.Request, deps Dependencias, whatsapp string) {
	prefijo := ""
	if u, err := url.Parse(deps.Config.WalletClienteTestnet); err == nil {
		prefijo = u.Scheme + "://" + u.Host + "/"
	}
	tipo, pagos, err := deps.Datos.PagosExternosDe(r.Context(), whatsapp, prefijo)
	if err != nil {
		api.Fail(w, err)

		return
	}
	if tipo == "" {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "Ese número todavía no tiene billetera: primero debe registrarse."))

		return
	}

	wallet := deps.Config.WalletClienteTestnet
	if tipo == "tienda" {
		wallet = deps.Config.WalletTiendaTestnet
	}
	moneda := ""
	if documento, err := deps.OpenPayments.ResolveWalletAddress(r.Context(), wallet); err == nil {
		moneda = documento.AssetCode
	} else {
		slog.Warn("pagos: resolving the testnet wallet for its balance answer", "error", err)
	}
	direccion := "$" + strings.TrimPrefix(wallet, "https://")

	aviso := "Pagas desde tu billetera en " + moneda + " (" + direccion + "). Por privacidad, Open Payments no deja " +
		"que una app vea el saldo de tu billetera: lo ves en tu propia billetera. Antes de cada retiro te mostramos " +
		"cuánto se debita en " + moneda + " y solo se paga si escribes APROBAR."
	movimientos := []map[string]any{}
	for _, p := range pagos {
		if tipo == "tienda" {
			movimientos = append(movimientos, map[string]any{"tipo": "cobro_retiro", "monto": p.RecibidoCOP, "moneda": "COP",
				"cliente": p.Cliente, "reserva_id": p.ReservaID, "en": p.En})

			continue
		}
		movimientos = append(movimientos, map[string]any{"tipo": "pago_retiro", "monto": -p.Debito, "moneda": p.Moneda,
			"recibido_cop": p.RecibidoCOP, "tienda": p.Tienda, "reserva_id": p.ReservaID, "en": p.En})
	}
	if tipo == "tienda" {
		aviso = "Tus cobros llegan a tu billetera en " + moneda + " (" + direccion + "). Por privacidad, Open Payments " +
			"no deja que una app vea el saldo de tu billetera: lo ves en tu propia billetera. Aquí están los retiros " +
			"que cobraste por Caja de Barrio."
	}

	api.JSON(w, http.StatusOK, map[string]any{
		"modo": "testnet", "wallet_address": direccion, "moneda": moneda,
		"saldo": nil, "saldo_visible": false, "aviso": aviso, "movimientos": movimientos,
	})
}
