package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/caja"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
)

// retiroTestnet runs ONE withdrawal on the Interledger testnet (OP_MODO=testnet), the way
// the backend's testnet mode does it: one incoming payment at the store's COP wallet, one
// quote by receiver from the client's USD wallet, the interactive grant approved BY A
// PERSON on the test wallet's own screen, and one outgoing payment. It prints every id.
//
// The grant goes first, so the person's approval cannot outlive the quote's expiry.
func retiroTestnet(ctx context.Context, cfg config.Config, cliente *opclient.Client, monto int64) error {
	if cfg.OP.Modo != config.ModoTestnet {
		return errors.New("-testnet needs OP_MODO=testnet and its variables (docs/modo-testnet.md)")
	}
	walletCliente, walletTienda := cfg.OP.TestnetWalletCliente, cfg.OP.TestnetWalletTienda
	comisiones := caja.ComisionesDe(monto, efectivoLibreDemo)
	tiendaRecibe := monto + comisiones.Tendero

	fmt.Printf("\n=== Retiro en el testnet: %d COP ===\n", monto)
	fmt.Printf("comisión del tendero %d · comisión de Caja de Barrio %d (libro interno)\n", comisiones.Tendero, comisiones.Plataforma)

	// STEP 0: both wallets resolve, with their assets.
	docCliente, err := cliente.ResolveWalletAddress(ctx, walletCliente)
	if err != nil {
		return fmt.Errorf("la wallet del cliente: %w", err)
	}
	docTienda, err := cliente.ResolveWalletAddress(ctx, walletTienda)
	if err != nil {
		return fmt.Errorf("la wallet de la tienda: %w", err)
	}
	fmt.Printf("cliente %s (%s, escala %d)\ntienda  %s (%s, escala %d)\n\n", docCliente.URL, docCliente.AssetCode,
		docCliente.AssetScale, docTienda.URL, docTienda.AssetCode, docTienda.AssetScale)

	consentimiento, err := escucharConsentimiento(puertoConsentimiento)
	if err != nil {
		return fmt.Errorf("no se pudo escuchar el consentimiento: %w", err)
	}
	defer consentimiento.cerrar()

	// STEP 1: the cupo, in the CLIENT's asset, approved by a person on the test wallet.
	hoy := time.Now().UTC().Format("2006-01-02")
	grant, err := cliente.RequestOutgoingPaymentGrant(ctx, opclient.OutgoingPaymentGrantParams{
		ClientWalletAddress: walletCliente,
		DebitLimit:          cfg.OP.TestnetCupo,
		Interval:            "R/" + hoy + "T00:00:00Z/P7D",
		FinishURL:           fmt.Sprintf("http://localhost:%d/op/finish", puertoConsentimiento),
		GrantID:             "gra_testnet_demo",
	})
	if err != nil {
		return fmt.Errorf("grant: %w", err)
	}
	fmt.Printf("1. cupo de %d %s pedido\n\n   >>> Abre este enlace en tu navegador, entra a la test wallet y APRUEBA:\n\n   %s\n\n   (esperando hasta 5 minutos a que el navegador vuelva a localhost:%d)\n",
		cfg.OP.TestnetCupo, docCliente.AssetCode, grant.RedirectURL, puertoConsentimiento)

	var valores map[string]string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case v := <-consentimiento.recibido:
		valores = map[string]string{"interact_ref": v.Get("interact_ref"), "hash": v.Get("hash"), "result": v.Get("result")}
	case <-time.After(5 * time.Minute):
		return errors.New("nadie aprobó el cupo en 5 minutos")
	}
	if valores["interact_ref"] == "" {
		return fmt.Errorf("el navegador volvió sin interact_ref (¿se rechazó el cupo?): %v", valores)
	}
	fmt.Printf("2. aprobado · interact_ref %s\n", valores["interact_ref"])

	// The hash binds the authorization server the grant was asked of.
	verificado := false
	for _, servidor := range []string{grant.AuthServer, strings.TrimSuffix(grant.AuthServer, "/"), strings.TrimSuffix(grant.AuthServer, "/") + "/"} {
		if opclient.VerifyInteractiveHash(grant.ClientNonce, grant.ServerNonce, valores["interact_ref"], servidor, valores["hash"]) {
			verificado = true
			fmt.Printf("   hash del callback verificado contra %s\n", servidor)

			break
		}
	}
	if !verificado {
		return fmt.Errorf("el hash del callback no verifica contra %s: el retiro se detiene", grant.AuthServer)
	}

	// GNAP's wait, honoured with a few retries.
	var token opclient.AccessToken
	for intento := 0; ; intento++ {
		token, err = cliente.ContinueOutgoingPaymentGrant(ctx, opclient.ContinueOutgoingPaymentGrantParams{
			ClientWalletAddress: walletCliente, ContinueURI: grant.ContinueURI,
			ContinueToken: grant.ContinueToken, InteractRef: valores["interact_ref"],
		})
		if err == nil || intento >= 3 || !strings.Contains(strings.ToLower(err.Error()), "wait") {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("continuar el grant: %w", err)
	}
	fmt.Printf("3. token del cupo (vence en %d s)\n", token.ExpiresIn)

	// STEP 2: one incoming payment at the store, in its pesos.
	cobro, err := cliente.CreateIncomingPayment(ctx, walletTienda, tiendaRecibe)
	if err != nil {
		return fmt.Errorf("cobro: %w", err)
	}
	fmt.Printf("4. cobro de la tienda\n   %s\n   pide %s %s (escala %d)\n", cobro.URL, cobro.Amount.Value, cobro.Amount.AssetCode, cobro.Amount.AssetScale)

	// STEP 3: one quote by receiver: the debit comes out in the client's asset.
	quote, err := cliente.CreateQuoteByReceiver(ctx, walletCliente, cobro.URL)
	if err != nil {
		return fmt.Errorf("cotización: %w", err)
	}
	fmt.Printf("5. cotización\n   %s\n   debita %s %s (escala %d) · recibe %s %s (escala %d) · vence %s\n", quote.URL,
		quote.DebitAmount.Value, quote.DebitAmount.AssetCode, quote.DebitAmount.AssetScale,
		quote.ReceiveAmount.Value, quote.ReceiveAmount.AssetCode, quote.ReceiveAmount.AssetScale, quote.ExpiresAt)

	// STEP 4: one outgoing payment with the cupo's token.
	pago, _, err := cliente.CreateOutgoingPayment(ctx, walletCliente, quote.URL, token)
	if pago.URL == "" {
		return fmt.Errorf("pago: %w", err)
	}
	if err != nil {
		fmt.Printf("   aviso: %v\n", err)
	}
	fmt.Printf("6. pago saliente\n   %s\n", pago.URL)

	// STEP 5: the money has to be THERE.
	recibido, err := esperarCobro(ctx, cobro.URL, tiendaRecibe*pow10(cobro.Amount.AssetScale), 60*time.Second)
	if err != nil {
		return fmt.Errorf("el cobro de la tienda no se completó: %w", err)
	}
	fmt.Printf("7. cobro COMPLETADO: la tienda recibió %s %s\n", recibido.Value, recibido.AssetCode)
	fmt.Println("\n=== el dinero se movió de USD a COP en el testnet ===")

	return nil
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}

	return v
}
