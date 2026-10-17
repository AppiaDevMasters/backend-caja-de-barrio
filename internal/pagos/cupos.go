package pagos

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// wallets answers 8.1: the document of a wallet address.
func wallets(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := normalizarWallet(r.URL.Query().Get("address"))
		if address == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta la wallet address."))

			return
		}
		documento, err := deps.OpenPayments.ResolveWalletAddress(r.Context(), address)
		if err != nil {
			slog.Error("pagos: resolving a wallet", "error", err)
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No se encontró esa billetera."))

			return
		}

		api.JSON(w, http.StatusOK, map[string]any{
			"auth_server": documento.AuthServer, "resource_server": documento.ResourceServer,
			"asset_code": documento.AssetCode, "asset_scale": documento.AssetScale,
		})
	}
}

// pedirCupo answers 8.2: the interactive grant of outgoing-payment the person
// approves once in her wallet.
func pedirCupo(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PersonaID     string `json:"persona_id"`
			WalletCliente string `json:"wallet_cliente"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.PersonaID) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Faltan persona_id y wallet_cliente."))

			return
		}
		if deps.Config.FinishURL == "" {
			api.Fail(w, api.Errorf(api.CodeInterno, "El cupo no está configurado (OP_FINISH_URL)."))

			return
		}

		persona, existe, err := deps.Datos.PersonaPorID(r.Context(), strings.TrimSpace(body.PersonaID))
		if err != nil {
			api.Fail(w, err)

			return
		}
		if !existe {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe esa persona."))

			return
		}
		if persona.EstadoRegistro != "aprobada" {
			api.Fail(w, api.Errorf(api.CodePersonaNoAprobada, "La persona todavía no está aprobada."))

			return
		}
		wallet := normalizarWallet(persona.WalletAddress)
		if deps.Config.Testnet {
			// The demo's testnet wallet replaces the stored one (docs/modo-testnet.md).
			wallet = deps.Config.WalletClienteTestnet
		}
		if body.WalletCliente != "" && !deps.Config.Testnet && !mismaWallet(body.WalletCliente, wallet) {
			api.Fail(w, api.Errorf(api.CodeDatosNoCoinciden, "La billetera no corresponde a la persona."))

			return
		}

		if cupo, tiene, err := deps.Datos.CupoPorPersona(r.Context(), persona.ID); err != nil {
			api.Fail(w, err)

			return
		} else if tiene && cupo.AprobadoEn != nil && mismaWallet(cupo.WalletAddress, wallet) {
			api.JSON(w, http.StatusOK, map[string]any{
				"grant_id": cupo.GrantID, "redirect_url": nil, "ya_aprobado": true,
				"limite": cupo.Limite, "moneda": cupo.Moneda, "intervalo": "P7D",
				"disponible": cupo.Limite - cupo.Usado,
			})

			return
		}

		documento, err := deps.OpenPayments.ResolveWalletAddress(r.Context(), wallet)
		if err != nil {
			slog.Error("pagos: resolving the client's wallet", "persona_id", persona.ID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo leer la billetera de la persona."))

			return
		}
		limite, ok := limitesCupo[documento.AssetCode]
		if deps.Config.Testnet {
			// The ceiling travels in the CLIENT's asset (USD in the demo), not in pesos.
			limite, ok = deps.Config.CupoTestnet, deps.Config.CupoTestnet > 0
		}
		if !ok {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "La moneda %s no tiene cupo definido.", documento.AssetCode))

			return
		}

		grantID, err := nuevoID("gra_")
		if err != nil {
			api.Fail(w, err)

			return
		}
		hoy := time.Now().UTC().Format("2006-01-02")
		grant, err := deps.OpenPayments.RequestOutgoingPaymentGrant(r.Context(), opclient.OutgoingPaymentGrantParams{
			ClientWalletAddress: wallet,
			DebitLimit:          limite,
			Interval:            "R/" + hoy + "T00:00:00Z/P7D",
			FinishURL:           deps.Config.FinishURL,
			GrantID:             grantID,
		})
		if err != nil {
			slog.Error("pagos: requesting the cupo grant", "persona_id", persona.ID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo pedir el cupo en Open Payments."))

			return
		}

		err = deps.Datos.GuardarCupoPendiente(r.Context(), store.CupoGrant{
			PersonaID: persona.ID, GrantID: grantID, WalletAddress: wallet, Limite: float64(limite),
			Moneda: documento.AssetCode, Intervalo: "P7D", ContinueURI: grant.ContinueURI,
			ContinueToken: grant.ContinueToken, ClientNonce: grant.ClientNonce, ServerNonce: grant.ServerNonce,
			AuthServer: authServerDelHash(deps, grant),
		})
		if errors.Is(err, store.ErrCupoAprobado) {
			api.Fail(w, api.Errorf(api.CodeEstadoInvalido, "La persona ya aprobó su cupo."))

			return
		}
		if err != nil {
			api.Fail(w, err)

			return
		}

		api.JSON(w, http.StatusCreated, map[string]any{
			"grant_id": grantID, "redirect_url": grant.RedirectURL, "limite": limite,
			"moneda": documento.AssetCode, "intervalo": "P7D",
		})
	}
}

// continuarCupo answers 8.3: the wallet sent the person back; the hash is verified and
// the grant continued for its token.
func continuarCupo(deps Dependencias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InteractRef string `json:"interact_ref"`
			Hash        string `json:"hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.InteractRef) == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta interact_ref."))

			return
		}

		cupo, existe, err := deps.Datos.CupoPorGrant(r.Context(), r.PathValue("grant_id"))
		if err != nil {
			api.Fail(w, err)

			return
		}
		if !existe {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe ese cupo."))

			return
		}
		persona, _, err := deps.Datos.PersonaPorID(r.Context(), cupo.PersonaID)
		if err != nil {
			api.Fail(w, err)

			return
		}
		respuesta := map[string]any{
			"ok": true, "persona_id": cupo.PersonaID, "whatsapp": persona.Whatsapp,
			"limite": cupo.Limite, "moneda": cupo.Moneda,
		}
		if cupo.AprobadoEn != nil {
			// The browser can come back twice; the grant was already continued.
			api.JSON(w, http.StatusOK, respuesta)

			return
		}

		if !opclient.VerifyInteractiveHash(cupo.ClientNonce, cupo.ServerNonce, body.InteractRef, cupo.AuthServer, body.Hash) {
			slog.Warn("pagos: the cupo callback hash does not verify", "grant_id", cupo.GrantID)
			api.Fail(w, api.Errorf(api.CodeHashInvalido, "El hash del callback no es válido."))

			return
		}

		// GNAP makes the client wait before continuing; a quick person can beat it.
		var token opclient.AccessToken
		for intento := 0; ; intento++ {
			token, err = deps.OpenPayments.ContinueOutgoingPaymentGrant(r.Context(), opclient.ContinueOutgoingPaymentGrantParams{
				ClientWalletAddress: cupo.WalletAddress, ContinueURI: cupo.ContinueURI,
				ContinueToken: cupo.ContinueToken, InteractRef: body.InteractRef,
			})
			if err == nil || intento >= 3 || !strings.Contains(strings.ToLower(err.Error()), "wait") {
				break
			}
			select {
			case <-r.Context().Done():
				api.Fail(w, r.Context().Err())

				return
			case <-time.After(deps.Config.EsperaGrant):
			}
		}
		if err != nil {
			slog.Error("pagos: continuing the cupo grant", "grant_id", cupo.GrantID, "error", err)
			api.Fail(w, api.Errorf(api.CodeProveedor, "No se pudo terminar la aprobación del cupo."))

			return
		}

		sellado, err := cifrar(deps.Config.Secreto, token.Value)
		if err != nil {
			api.Fail(w, err)

			return
		}
		vence := time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
		if err := deps.Datos.AprobarCupo(r.Context(), cupo.GrantID, sellado, token.ManageURL, vence); err != nil {
			api.Fail(w, err)

			return
		}

		api.JSON(w, http.StatusOK, respuesta)
	}
}

// authServerDelHash is the authorization server the callback hash binds. The own ASE is
// configured with it (its public base plus the tenant); on the testnet it is the one the
// client's wallet announced, which is the one the grant was asked of.
func authServerDelHash(deps Dependencias, grant opclient.OutgoingPaymentGrant) string {
	if deps.Config.Testnet && grant.AuthServer != "" {
		return grant.AuthServer
	}

	return deps.Config.AuthServerHash
}
