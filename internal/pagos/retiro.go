package pagos

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

func leerReserva(w http.ResponseWriter, r *http.Request, deps Dependencias, id string) (store.ReservaRetiro, bool) {
	reserva, existe, err := deps.Datos.ReservaRetiroPorID(r.Context(), strings.TrimSpace(id))
	if err != nil {
		api.Fail(w, err)

		return store.ReservaRetiro{}, false
	}
	if !existe {
		api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe esa reserva."))

		return store.ReservaRetiro{}, false
	}

	return reserva, true
}

// crearCobros answers 8.4: the two incoming payments of the withdrawal.
func crearCobros(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReservaID    string `json:"reserva_id"`
			WalletTienda string `json:"wallet_tienda"`
			MontoCOP     int64  `json:"monto_cop"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

			return
		}
		reserva, ok := leerReserva(w, r, deps, body.ReservaID)
		if !ok {
			return
		}
		if reserva.Estado != "reservada" {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La reserva está %s.", reserva.Estado))

			return
		}
		if (body.WalletTienda != "" && !mismaWallet(body.WalletTienda, reserva.WalletTienda)) ||
			(body.MontoCOP != 0 && body.MontoCOP != reserva.MontoCOP) {
			api.Fail(w, api.Errorf(api.CodeDatosNoCoinciden, "La billetera o el monto no coinciden con la reserva."))

			return
		}

		tiendaRecibe := reserva.MontoCOP + reserva.ComisionTenderoCOP
		respuesta := func(tienda, plataforma string) map[string]any {
			return map[string]any{
				"incoming_payment_url": tienda, "cobro_plataforma_url": plataforma,
				"tienda_recibe_cop": tiendaRecibe, "caja_de_barrio_recibe_cop": reserva.ComisionPlataformaCOP,
			}
		}
		if reserva.IncomingPaymentURL != nil && reserva.CobroPlataformaURL != nil {
			api.JSON(w, http.StatusCreated, respuesta(*reserva.IncomingPaymentURL, *reserva.CobroPlataformaURL))

			return
		}

		cobros, err := deps.OpenPayments.CreateIncomingPayments(r.Context(), opclient.CreateIncomingPaymentsParams{
			StoreWalletAddress:    normalizarWallet(reserva.WalletTienda),
			StoreAmountCOP:        tiendaRecibe,
			PlatformWalletAddress: deps.Config.WalletPlataforma,
			PlatformAmountCOP:     reserva.ComisionPlataformaCOP,
		})
		if err != nil {
			slog.Error("pagos: creating the collection points", "reserva_id", reserva.ID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudieron crear los cobros en Open Payments."))

			return
		}
		if err := deps.Datos.GuardarCobros(r.Context(), reserva.ID, cobros.Store.URL, cobros.Platform.URL); err != nil {
			api.Fail(w, err)

			return
		}

		api.JSON(w, http.StatusCreated, respuesta(cobros.Store.URL, cobros.Platform.URL))
	}
}

// cotizar answers 8.5: two quotes from the client's wallet, one to each collection point.
func cotizar(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReservaID          string `json:"reserva_id"`
			WalletCliente      string `json:"wallet_cliente"`
			IncomingPaymentURL string `json:"incoming_payment_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

			return
		}

		var (
			reserva store.ReservaRetiro
			existe  bool
			err     error
		)
		if strings.TrimSpace(body.ReservaID) != "" {
			reserva, existe, err = deps.Datos.ReservaRetiroPorID(r.Context(), strings.TrimSpace(body.ReservaID))
		} else {
			reserva, existe, err = deps.Datos.ReservaRetiroPorCobro(r.Context(), strings.TrimSpace(body.IncomingPaymentURL))
		}
		if err != nil {
			api.Fail(w, err)

			return
		}
		if !existe || reserva.IncomingPaymentURL == nil || reserva.CobroPlataformaURL == nil {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "Esa reserva no tiene cobros todavía: primero hay que crear el cobro."))

			return
		}
		if reserva.Estado != "reservada" {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La reserva está %s.", reserva.Estado))

			return
		}
		if body.WalletCliente != "" && !mismaWallet(body.WalletCliente, reserva.WalletCliente) {
			api.Fail(w, api.Errorf(api.CodeDatosNoCoinciden, "La billetera no es la de la persona de la reserva."))

			return
		}

		quotes, err := deps.OpenPayments.CreateQuotes(r.Context(), opclient.CreateQuotesParams{
			ClientWalletAddress: normalizarWallet(reserva.WalletCliente),
			StoreReceiver:       *reserva.IncomingPaymentURL,
			StoreAmountCOP:      reserva.MontoCOP + reserva.ComisionTenderoCOP,
			PlatformReceiver:    *reserva.CobroPlataformaURL,
			PlatformAmountCOP:   reserva.ComisionPlataformaCOP,
		})
		if err != nil {
			slog.Error("pagos: creating the quotes", "reserva_id", reserva.ID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo cotizar el pago en Open Payments."))

			return
		}
		debito, err := enteros(quotes.Debit)
		if err != nil {
			api.Fail(w, err)

			return
		}
		vence, err := time.Parse(time.RFC3339, quotes.ExpiresAt)
		if err != nil {
			vence = time.Now().Add(5 * time.Minute)
		}
		if err := deps.Datos.GuardarCotizacion(r.Context(), reserva.ID, quotes.Store.URL, quotes.Platform.URL, debito,
			quotes.Debit.AssetCode, vence); err != nil {
			api.Fail(w, err)

			return
		}

		api.JSON(w, http.StatusCreated, map[string]any{
			"quote_url": quotes.Store.URL, "quote_plataforma_url": quotes.Platform.URL,
			"debito": debito, "moneda": quotes.Debit.AssetCode, "vence_en": vence.UTC().Format(time.RFC3339),
			"desglose_cop": map[string]int64{
				"retiro": reserva.MontoCOP, "comision_tendero": reserva.ComisionTenderoCOP,
				"comision_caja_de_barrio": reserva.ComisionPlataformaCOP, "total": reserva.TotalCOP(),
			},
		})
	}
}

// aprobar answers 8.6: the person wrote APROBAR for the quote in force.
func aprobar(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReservaID    string `json:"reserva_id"`
			PersonaID    string `json:"persona_id"`
			TextoUsuario string `json:"texto_usuario"`
			Canal        string `json:"canal"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.TextoUsuario) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Faltan la reserva, la persona o el texto que escribió."))

			return
		}
		reserva, ok := leerReserva(w, r, deps, body.ReservaID)
		if !ok {
			return
		}
		if reserva.PersonaID != strings.TrimSpace(body.PersonaID) {
			api.Fail(w, api.Errorf(api.CodeDatosNoCoinciden, "La reserva no es de esta persona."))

			return
		}
		if reserva.Estado != "reservada" || reserva.QuoteURL == nil || reserva.Debito == nil {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No hay un pago cotizado pendiente de aprobación para esta reserva."))

			return
		}

		id, err := nuevoID("apr_")
		if err != nil {
			api.Fail(w, err)

			return
		}
		canal := strings.TrimSpace(body.Canal)
		if canal == "" {
			canal = "whatsapp"
		}
		moneda := ""
		if reserva.Moneda != nil {
			moneda = *reserva.Moneda
		}
		if err := deps.Datos.RegistrarAprobacion(r.Context(), store.AprobacionPago{
			ID: id, ReservaID: reserva.ID, PersonaID: reserva.PersonaID, QuoteURL: *reserva.QuoteURL,
			Debito: *reserva.Debito, Moneda: moneda, TextoUsuario: body.TextoUsuario, Canal: canal,
		}); err != nil {
			api.Fail(w, err)

			return
		}

		api.JSON(w, http.StatusCreated, map[string]any{"ok": true, "aprobacion_id": id, "debito": *reserva.Debito, "moneda": moneda})
	}
}

// esDelASE says whether a wallet lives at this backend's own Rafiki: then its money is
// the virtual wallet, and the ASE has to put up the liquidity of its payments.
func esDelASE(deps Dependencias, wallet string) bool {
	u, err := url.Parse(normalizarWallet(wallet))

	return err == nil && deps.Config.HostASE != "" && strings.EqualFold(u.Host, deps.Config.HostASE)
}

// pagar answers 8.7: the two outgoing payments with the token of the cupo, the
// liquidity backed by the virtual wallet and the wait for the store's collection point.
func pagar(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReservaID string `json:"reserva_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido."))

			return
		}
		reserva, ok := leerReserva(w, r, deps, body.ReservaID)
		if !ok {
			return
		}

		// Idempotency: the same reservation never creates payments twice.
		if len(reserva.Resultado) > 0 {
			var anterior map[string]any
			if json.Unmarshal(reserva.Resultado, &anterior) == nil {
				if anterior["estado"] == "pendiente" {
					if final, err := cerrarSiLlego(r.Context(), deps, reserva); err == nil && final != nil {
						anterior = final
					}
				}
				api.JSON(w, http.StatusOK, anterior)

				return
			}
		}

		ahora := time.Now()
		cupo, tieneCupo, err := deps.Datos.CupoPorPersona(r.Context(), reserva.PersonaID)
		if err != nil {
			api.Fail(w, err)

			return
		}
		switch {
		case reserva.Estado != "reservada":
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "No se puede pagar una reserva en estado %s.", reserva.Estado))
			return
		case ahora.After(reserva.VenceEn):
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La reserva venció; hay que reservar de nuevo."))
			return
		case !reserva.FaceMatchOK:
			api.Fail(w, api.Errorf(api.CodeSinFaceMatch, "No se paga sin verificación facial aprobada."))
			return
		case !reserva.RiesgoOK:
			api.Fail(w, api.Errorf(api.CodeSinRiesgo, "No se paga sin visto bueno de riesgo."))
			return
		case reserva.QuoteURL == nil || reserva.QuotePlataformaURL == nil || reserva.Debito == nil:
			api.Fail(w, api.Errorf(api.CodeSinAprobacion, "Todavía no hay una cotización aprobada."))
			return
		case reserva.QuoteVenceEn != nil && ahora.After(*reserva.QuoteVenceEn):
			api.Fail(w, api.Errorf(api.CodeCotizacionVencida, "La cotización venció; hay que cotizar de nuevo."))
			return
		case !tieneCupo || cupo.AprobadoEn == nil || cupo.AccessToken == "":
			api.Fail(w, api.Errorf(api.CodeSinCupo, "La persona no ha aprobado su cupo en la billetera."))
			return
		case cupo.Usado+*reserva.Debito > cupo.Limite+1e-9:
			api.Fail(w, api.Errorf(api.CodeCupoExcedido, "El pago supera el cupo disponible."))
			return
		}
		aprobada, err := deps.Datos.AprobacionDeQuote(r.Context(), reserva.ID, *reserva.QuoteURL)
		if err != nil {
			api.Fail(w, err)

			return
		}
		if !aprobada {
			api.Fail(w, api.Errorf(api.CodeSinAprobacion, "La persona todavía no ha escrito APROBAR para esta cotización."))

			return
		}

		// A wallet of this ASE pays from the virtual wallet: the balance must cover it
		// BEFORE any payment exists.
		delASE := esDelASE(deps, reserva.WalletCliente)
		if delASE {
			billetera, err := deps.Datos.MovimientosDe(r.Context(), reserva.WalletCliente)
			if err != nil {
				api.Fail(w, err)

				return
			}
			if billetera.Saldo < reserva.TotalCOP() {
				api.Fail(w, api.Errorf(api.CodeSaldoInsuficiente, "Saldo insuficiente: la billetera tiene %d COP y el retiro cuesta %d.",
					billetera.Saldo, reserva.TotalCOP()))

				return
			}
		}

		token, err := tokenVigente(r.Context(), deps, cupo)
		if err != nil {
			slog.Error("pagos: preparing the cupo token", "persona_id", cupo.PersonaID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo usar el cupo aprobado; hay que aprobarlo de nuevo."))

			return
		}

		pagos, err := deps.OpenPayments.CreateOutgoingPayments(r.Context(), opclient.CreateOutgoingPaymentsParams{
			ClientWalletAddress: normalizarWallet(reserva.WalletCliente),
			StoreQuoteURL:       *reserva.QuoteURL,
			PlatformQuoteURL:    *reserva.QuotePlataformaURL,
			Token:               token,
		})
		if pagos.Token.Value != "" {
			guardarToken(r.Context(), deps, cupo.PersonaID, pagos.Token)
		}
		if err != nil || pagos.Store.URL == "" {
			slog.Error("pagos: creating the outgoing payments", "reserva_id", reserva.ID, "error", err)
			fallido := map[string]any{"estado": "fallido", "error": "No se pudo enviar el pago por Open Payments."}
			if pagos.Store.URL != "" {
				_ = deps.Datos.GuardarPagos(r.Context(), reserva.ID, pagos.Store.URL, pagos.Platform.URL, fallido)
			}
			api.JSON(w, http.StatusOK, fallido)

			return
		}

		resultado := map[string]any{
			"estado": "pendiente", "outgoing_payment_id": pagos.Store.URL, "outgoing_plataforma_id": pagos.Platform.URL,
			"cupo_disponible": cupo.Limite - cupo.Usado - *reserva.Debito,
		}
		if err := deps.Datos.GuardarPagos(r.Context(), reserva.ID, pagos.Store.URL, pagos.Platform.URL, resultado); err != nil {
			api.Fail(w, err)

			return
		}

		if delASE {
			if err := fondear(r.Context(), deps, reserva, pagos); err != nil {
				slog.Error("pagos: putting up the liquidity", "reserva_id", reserva.ID, "error", err)
			}
		}

		reserva.OutgoingPaymentID = &pagos.Store.URL
		final, err := esperarYCerrar(r.Context(), deps, reserva)
		if err != nil {
			slog.Error("pagos: waiting for the store's collection point", "reserva_id", reserva.ID, "error", err)
		}
		if final != nil {
			resultado = final
		}
		api.JSON(w, http.StatusOK, resultado)
	}
}

// tokenVigente opens the stored token and rotates it when it has expired.
func tokenVigente(ctx context.Context, deps Dependencias, cupo store.CupoGrant) (opclient.AccessToken, error) {
	valor, err := descifrar(deps.Config.Secreto, cupo.AccessToken)
	if err != nil {
		return opclient.AccessToken{}, err
	}
	token := opclient.AccessToken{Value: valor, ManageURL: cupo.ManageURL}
	if cupo.TokenVenceEn != nil {
		token.ExpiresIn = int(time.Until(*cupo.TokenVenceEn).Seconds())
	}
	if cupo.TokenVenceEn == nil || time.Now().Add(30*time.Second).Before(*cupo.TokenVenceEn) {
		return token, nil
	}

	rotado, err := deps.OpenPayments.RotateAccessToken(ctx, cupo.ManageURL, valor)
	if err != nil {
		return opclient.AccessToken{}, err
	}
	guardarToken(ctx, deps, cupo.PersonaID, rotado)

	return rotado, nil
}

func guardarToken(ctx context.Context, deps Dependencias, personaID string, token opclient.AccessToken) {
	sellado, err := cifrar(deps.Config.Secreto, token.Value)
	if err == nil {
		err = deps.Datos.ActualizarToken(ctx, personaID, sellado, token.ManageURL,
			time.Now().Add(time.Duration(token.ExpiresIn)*time.Second))
	}
	if err != nil {
		slog.Error("pagos: storing the rotated token", "persona_id", personaID, "error", err)
	}
}

// fondear debits the virtual wallet and puts up the liquidity of both payments. The
// debit is recorded first and once (its reference is the reservation), so a retry
// never charges twice.
func fondear(ctx context.Context, deps Dependencias, reserva store.ReservaRetiro, pagos opclient.OutgoingPayments) error {
	if reserva.LiquidezEn != nil || deps.Liquidez == nil {
		return nil
	}
	id, err := nuevoID("mov_")
	if err != nil {
		return err
	}
	if err := deps.Datos.RegistrarMovimiento(ctx, id, normalizarWallet(reserva.WalletCliente), "pago_retiro",
		-reserva.TotalCOP(), reserva.ID); err != nil {
		return err
	}
	for _, pago := range []string{pagos.Store.URL, pagos.Platform.URL} {
		pagoID := pago[strings.LastIndex(pago, "/")+1:]
		if err := deps.Liquidez.DepositOutgoingPaymentLiquidity(ctx, pagoID); err != nil {
			return err
		}
	}

	return deps.Datos.MarcarLiquidez(ctx, reserva.ID)
}

// esperarYCerrar polls the store's collection point and closes the payment when the
// money is there.
func esperarYCerrar(ctx context.Context, deps Dependencias, reserva store.ReservaRetiro) (map[string]any, error) {
	limite := time.Now().Add(deps.Config.EsperaCobro)
	for {
		final, err := cerrarSiLlego(ctx, deps, reserva)
		if final != nil || time.Now().After(limite) {
			return final, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// cerrarSiLlego reads the store's collection point and, once it holds the whole amount,
// closes the payment: the reservation becomes pagada, the store and Caja de Barrio
// are credited, and the cupo is used. Every effect happens once.
func cerrarSiLlego(ctx context.Context, deps Dependencias, reserva store.ReservaRetiro) (map[string]any, error) {
	if reserva.IncomingPaymentURL == nil || deps.Cobros == nil {
		return nil, nil
	}
	recibido, escala, err := deps.Cobros(ctx, *reserva.IncomingPaymentURL)
	if err != nil {
		return nil, err
	}
	esperado := reserva.MontoCOP + reserva.ComisionTenderoCOP
	if float64(recibido)/math.Pow10(escala) < float64(esperado) {
		return nil, nil
	}

	cerrada, err := deps.Datos.MarcarPagada(ctx, reserva.ID)
	if err != nil {
		return nil, err
	}
	cupo, _, _ := deps.Datos.CupoPorPersona(ctx, reserva.PersonaID)
	debito := 0.0
	if reserva.Debito != nil {
		debito = *reserva.Debito
	}
	if cerrada || reserva.Estado == "pagada" || reserva.Estado == "entregada" {
		retirarLiquidezCobros(ctx, deps, reserva)
	}
	if cerrada {
		for _, abono := range []struct {
			wallet, tipo string
			monto        int64
		}{
			{normalizarWallet(reserva.WalletTienda), "cobro_retiro", esperado},
			{deps.Config.WalletPlataforma, "comision_plataforma", reserva.ComisionPlataformaCOP},
		} {
			id, err := nuevoID("mov_")
			if err == nil {
				err = deps.Datos.RegistrarMovimiento(ctx, id, abono.wallet, abono.tipo, abono.monto, reserva.ID)
			}
			if err != nil {
				slog.Error("pagos: crediting a collection", "reserva_id", reserva.ID, "error", err)
			}
		}
		if err := deps.Datos.SumarUsado(ctx, reserva.PersonaID, debito); err != nil {
			slog.Error("pagos: using the cupo", "reserva_id", reserva.ID, "error", err)
		}
		cupo.Usado += debito
	}

	outPlataforma := ""
	if reserva.OutgoingPlataformaID != nil {
		outPlataforma = *reserva.OutgoingPlataformaID
	}
	outTienda := ""
	if reserva.OutgoingPaymentID != nil {
		outTienda = *reserva.OutgoingPaymentID
	}
	final := map[string]any{
		"estado": "completado", "outgoing_payment_id": outTienda, "outgoing_plataforma_id": outPlataforma,
		"cupo_disponible": cupo.Limite - cupo.Usado,
	}
	if err := deps.Datos.GuardarResultado(ctx, reserva.ID, final); err != nil {
		return final, err
	}

	return final, nil
}

// estadoCobro answers 8.8: whether the store's collection point is complete.
func estadoCobro(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cobro := strings.TrimSpace(r.URL.Query().Get("url"))
		if cobro == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta la URL del cobro."))

			return
		}
		reserva, existe, err := deps.Datos.ReservaRetiroPorCobro(r.Context(), cobro)
		if err != nil {
			api.Fail(w, err)

			return
		}
		if !existe {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe ese cobro."))

			return
		}

		recibido, escala, err := deps.Cobros(r.Context(), cobro)
		if err != nil {
			slog.Error("pagos: reading a collection point", "reserva_id", reserva.ID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo leer el cobro."))

			return
		}
		pesos := int64(math.Round(float64(recibido) / math.Pow10(escala)))
		completado := pesos >= reserva.MontoCOP+reserva.ComisionTenderoCOP
		if completado && reserva.Estado != "reservada" && reserva.OutgoingPaymentID != nil {
			retirarLiquidezCobros(r.Context(), deps, reserva)
		}
		if completado && reserva.Estado == "reservada" && reserva.OutgoingPaymentID != nil {
			if _, err := cerrarSiLlego(r.Context(), deps, reserva); err != nil {
				slog.Error("pagos: closing a payment from its status", "reserva_id", reserva.ID, "error", err)
			}
		}

		api.JSON(w, http.StatusOK, map[string]any{"completado": completado, "recibido": pesos})
	}
}

// retirarLiquidezCobros takes out of Rafiki what the two collection points received:
// Rafiki moves money and does not keep balances, and the virtual wallets already
// credit the store and Caja de Barrio. The withdrawal is idempotent per payment, so
// calling it again (the payment and its status can both close it) is harmless.
func retirarLiquidezCobros(ctx context.Context, deps Dependencias, reserva store.ReservaRetiro) {
	if deps.Liquidez == nil {
		return
	}
	for _, cobro := range []*string{reserva.IncomingPaymentURL, reserva.CobroPlataformaURL} {
		if cobro == nil || *cobro == "" {
			continue
		}
		id := (*cobro)[strings.LastIndex(*cobro, "/")+1:]
		if err := deps.Liquidez.WithdrawIncomingPaymentLiquidity(ctx, id); err != nil {
			slog.Warn("pagos: withdrawing the liquidity of a collection point", "reserva_id", reserva.ID,
				"cobro", id, "error", err)
		}
	}
}
