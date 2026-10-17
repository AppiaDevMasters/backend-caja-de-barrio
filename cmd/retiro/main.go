// Command retiro runs ONE whole withdrawal against the local Rafiki stack and prints
// every step: the two collection points, the two quotes, the interactive grant approved
// at this backend's own consent page, the two outgoing payments and the check that the
// store's collection point received the money.
//
// It exists because this flow is the only way to see money move, and it has to run INSIDE
// the compose network: the wallet addresses and the authorization server are announced by
// service name, and a container cannot reach the host's published ports.
//
// It is a tool of the demo and not part of the product: the product's routes will do the
// same with a reservation and the data layer behind them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/caja"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/config"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/rafikiadmin"
)

// The wallets of the demo, by the name the seed gives them.
const (
	walletTienda     = "tienda-cajadebarrio"
	walletLaura      = "laura-cajadebarrio"
	walletPlataforma = "app-cajadebarrio-cop"
)

// puertoConsentimiento is where this tool listens for the authorization server to send the
// user's browser back. It is a fixed port because the address travels inside the grant.
const puertoConsentimiento = 8099

// efectivoLibreDemo is the cash the demo's store has free, which is what the commission
// rule reads. The product has it in the database; this tool takes the example of the
// contract so the numbers it prints can be compared with the contract's.
const efectivoLibreDemo int64 = 800_000

// zonaDemo is the day of the demo. Colombia has no daylight saving time, so a fixed
// offset is exact.
var zonaDemo = time.FixedZone("America/Bogota", -5*60*60)

func main() {
	if err := run(); err != nil {
		slog.Error("el retiro no termino", "error", err)
		os.Exit(1)
	}
}

func run() error {
	monto := flag.Int64("monto", 10_000, "monto del retiro en pesos enteros")
	testnet := flag.Bool("testnet", false, "correr el retiro en el testnet de Interledger (OP_MODO=testnet): cliente USD, tienda COP, un cobro y aprobación humana del cupo")
	fondear := flag.Bool("fondear", false, "adelantar la liquidez de los dos pagos desde el ASE; solo para el ensayo local, donde el cliente y el receptor son el mismo ASE")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cliente, err := opclient.New(opclient.Config{
		WalletAddressURL: cfg.OP.ClientWalletAddress,
		PrivateKeyPath:   cfg.OP.PrivateKeyPath,
		KeyID:            cfg.OP.KeyID,
		HostRewrites:     cfg.HostRewrites(),
		TLS:              cfg.OP.TLS,
	})
	if err != nil {
		return err
	}

	if *testnet {
		return retiroTestnet(ctx, cfg, cliente, *monto)
	}

	base := strings.TrimSuffix(cfg.Rafiki.OpenPaymentsURL, "/")
	direccion := func(nombre string) string { return base + "/" + nombre }

	comisiones := caja.ComisionesDe(*monto, efectivoLibreDemo)
	fmt.Printf("\n=== Retiro de %d COP ===\n", *monto)
	fmt.Printf("comision del tendero %d · comision de la plataforma %d · total %d\n",
		comisiones.Tendero, comisiones.Plataforma, comisiones.Total)
	fmt.Printf("tienda %s\ncliente %s\nplataforma %s\n\n", direccion(walletTienda), direccion(walletLaura), direccion(walletPlataforma))

	// The consent listener is up before the grant is requested, because the address it
	// answers on travels inside the grant as the place the wallet sends the user back to.
	consentimiento, err := escucharConsentimiento(puertoConsentimiento)
	if err != nil {
		return fmt.Errorf("no se pudo escuchar el consentimiento: %w", err)
	}
	defer consentimiento.cerrar()

	// STEP 1: the two collection points, one at the store and one at the platform.
	cobros, err := cliente.CreateIncomingPayments(ctx, opclient.CreateIncomingPaymentsParams{
		StoreWalletAddress:    direccion(walletTienda),
		StoreAmountCOP:        *monto + comisiones.Tendero,
		PlatformWalletAddress: direccion(walletPlataforma),
		PlatformAmountCOP:     comisiones.Plataforma,
	})
	if err != nil {
		return fmt.Errorf("cobros: %w", err)
	}
	fmt.Printf("1. cobros\n   tienda %s\n   plataforma %s\n", cobros.Store.URL, cobros.Platform.URL)

	// STEP 2: the two quotes, from the client's wallet and towards each collection point.
	cotizaciones, err := cliente.CreateQuotes(ctx, opclient.CreateQuotesParams{
		ClientWalletAddress: direccion(walletLaura),
		StoreReceiver:       cobros.Store.URL,
		StoreAmountCOP:      *monto + comisiones.Tendero,
		PlatformReceiver:    cobros.Platform.URL,
		PlatformAmountCOP:   comisiones.Plataforma,
	})
	if err != nil {
		return fmt.Errorf("cotizaciones: %w", err)
	}
	fmt.Printf("2. cotizaciones\n   tienda %s\n   debito %s %s · vence %s\n",
		cotizaciones.Store.URL, cotizaciones.Debit.Value, cotizaciones.Debit.AssetCode, cotizaciones.ExpiresAt)

	// STEP 3: the interactive grant, with the ceiling of the cupo.
	hoy := time.Now().In(zonaDemo).Format("2006-01-02")
	grant, err := cliente.RequestOutgoingPaymentGrant(ctx, opclient.OutgoingPaymentGrantParams{
		ClientWalletAddress: direccion(walletLaura),
		DebitLimit:          comisiones.Total,
		Interval:            "R/" + hoy + "T00:00:00Z/P7D",
		FinishURL:           fmt.Sprintf("http://localhost:%d/op/finish", puertoConsentimiento),
		GrantID:             "gra_retiro_demo",
	})
	if err != nil {
		return fmt.Errorf("grant: %w", err)
	}
	fmt.Printf("3. grant interactivo\n   aprobar en %s\n   continuar en %s\n", grant.RedirectURL, grant.ContinueURI)

	// STEP 4: the approval, driven the way a browser drives it: this backend's own consent
	// page, and then the authorization server sends the user back here.
	interactRef, interactNonce, hash, err := consentimiento.aprobar(ctx, grant.RedirectURL)
	if err != nil {
		return fmt.Errorf("aprobacion: %w", err)
	}
	fmt.Printf("4. aprobado · interact_ref %s\n", interactRef)

	// The hash binds the two nonces, the reference and the URL the AUTHORIZATION SERVER has
	// configured for itself: its public base plus the tenant, and NOT the authServer of the
	// wallet address document, which inside the stack network is the service name. Rafiki's
	// interaction route computes it exactly like this (packages/auth/dist/interaction/routes.js).
	authServerURL := strings.TrimSuffix(cfg.Rafiki.AuthServerPublicURL, "/") + "/" + cfg.Rafiki.TenantID
	if cfg.Rafiki.AuthServerPublicURL == "" {
		authServerURL = strings.TrimSuffix(cfg.Rafiki.AuthServerURL, "/") + "/" + cfg.Rafiki.TenantID
	}

	if !opclient.VerifyInteractiveHash(grant.ClientNonce, interactNonce, interactRef, authServerURL, hash) {
		return errors.New("el hash del callback no verifica: el retiro se detiene")
	}
	fmt.Println("   hash del callback verificado")

	// GNAP gives the client a "wait" before it may continue the grant: the auth server's
	// WAIT_SECONDS (five by default) and it answers 400 "continued grant faster than wait
	// period" otherwise. The SDK's Grant does not carry that value, so this tool waits the
	// configured default; the product's route has to honour it too.
	fmt.Println("5. esperando el wait del grant (WAIT_SECONDS)")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(6 * time.Second):
	}

	// The authorization server announces its own endpoints with the PUBLIC host of the demo,
	// which a container cannot reach: the continue and the token management URLs are
	// rewritten to the service name, exactly like the consent hops are.
	token, err := cliente.ContinueOutgoingPaymentGrant(ctx, opclient.ContinueOutgoingPaymentGrantParams{
		ClientWalletAddress: direccion(walletLaura),
		ContinueURI:         interna2(grant.ContinueURI),
		ContinueToken:       grant.ContinueToken,
		InteractRef:         interactRef,
	})
	if err != nil {
		return fmt.Errorf("continuar el grant: %w", err)
	}
	fmt.Printf("6. token aprobado (vence en %d s)\n", token.ExpiresIn)

	// STEP 6: the two payments, which rotate the token after each use.
	pagos, err := cliente.CreateOutgoingPayments(ctx, opclient.CreateOutgoingPaymentsParams{
		ClientWalletAddress: direccion(walletLaura),
		StoreQuoteURL:       cotizaciones.Store.URL,
		PlatformQuoteURL:    cotizaciones.Platform.URL,
		// The management URL of the token comes back with the same public host: it is
		// rewritten before the payments rotate it.
		Token: opclient.AccessToken{Value: token.Value, ManageURL: interna2(token.ManageURL), ExpiresIn: token.ExpiresIn},
	})
	if err != nil {
		return fmt.Errorf("pagos: %w", err)
	}
	fmt.Printf("7. pagos\n   tienda %s\n   plataforma %s\n   token final %s\n", pagos.Store.URL, pagos.Platform.URL, pagos.Token.Value)

	// STEP 6.5, local trial only. The client's wallet lives at this same ASE, so the account that
	// pays has no balance and Rafiki parks both payments in FUNDING. The real demo never needs
	// this: there the client's wallet is the test wallet and it already holds funds.
	if *fondear {
		if err := fondearPagos(ctx, cfg, pagos); err != nil {
			return err
		}
		fmt.Println("7b. liquidez de los dos pagos adelantada por el ASE (solo ensayo local)")
	}

	// STEP 7: the money has to be THERE. The collection point of the store has to show the
	// amount it asked for, or the withdrawal did not happen.
	recibido, err := esperarCobro(ctx, cobros.Store.URL, *monto+comisiones.Tendero, 20*time.Second)
	if err != nil {
		return fmt.Errorf("el cobro de la tienda no se completo: %w", err)
	}
	fmt.Printf("8. cobro completado\n   la tienda recibio %s %s\n", recibido.Value, recibido.AssetCode)
	fmt.Println("\n=== el dinero se movio ===")

	return nil
}

// fondearPagos brings the liquidity of the two payments up. It exists only for the local trial,
// which runs with the client's wallet at the very ASE that receives: with nobody on the client's
// side, the ASE has to put up the money that the client's real wallet already holds in the demo.
//
// The product has no such call, on purpose: a caja that funds its clients' payments is a caja
// paying with its own money.
func fondearPagos(ctx context.Context, cfg config.Config, pagos opclient.OutgoingPayments) error {
	admin, err := rafikiadmin.New(rafikiadmin.Config{
		URL:       cfg.Rafiki.AdminURL,
		TenantID:  cfg.Rafiki.TenantID,
		APISecret: cfg.Rafiki.AdminAPISecret,
	})
	if err != nil {
		return err
	}

	for _, pago := range []opclient.OutgoingPayment{pagos.Store, pagos.Platform} {
		id, err := idDePago(pago.URL)
		if err != nil {
			return err
		}
		if err := admin.DepositOutgoingPaymentLiquidity(ctx, id); err != nil {
			return fmt.Errorf("adelantar la liquidez del pago %s: %w", id, err)
		}
	}

	return nil
}

// idDePago takes the identifier out of an outgoing payment URL, which is its last segment.
func idDePago(url string) (string, error) {
	id := url[strings.LastIndex(url, "/")+1:]
	if id == "" {
		return "", fmt.Errorf("la URL del pago %q no trae identificador", url)
	}

	return id, nil
}

// consentimiento listens for the authorization server to send the user back, which is what
// the client's finish URL is for.
type consentimiento struct {
	server   *http.Server
	listener net.Listener
	recibido chan url.Values
}

func escucharConsentimiento(puerto int) (*consentimiento, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", puerto))
	if err != nil {
		return nil, err
	}

	c := &consentimiento{listener: listener, recibido: make(chan url.Values, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("/op/finish", func(w http.ResponseWriter, r *http.Request) {
		select {
		case c.recibido <- r.URL.Query():
		default:
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "listo")
	})

	c.server = &http.Server{Handler: mux}
	go func() {
		_ = c.server.Serve(listener)
	}()

	return c, nil
}

func (c *consentimiento) cerrar() {
	_ = c.server.Close()
}

// aprobar drives the interactive part the way a browser does: it opens the consent page
// of THIS backend, approves there, and waits for the authorization server to send the user
// back with the interaction reference, the interaction's own nonce and the hash that binds
// them.
func (c *consentimiento) aprobar(ctx context.Context, redirect string) (string, string, string, error) {
	// The front-channel of an interactive grant is a SESSION: opening the consent page
	// sets a cookie and finishing the interaction needs it. Without a jar the cookie is
	// dropped and the finish answers 401 "invalid session", which is what the fourth live
	// run found.
	jarra, err := cookiejar.New(nil)
	if err != nil {
		return "", "", "", err
	}

	cliente := &http.Client{
		Jar: jarra,
		// The authorization server announces itself and the identity provider with the
		// host of the demo, which does not resolve inside the network: every hop is
		// rewritten to the service name of the same port before it is followed.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			req.URL = interna(req.URL)
			return nil
		},
	}

	// The consent page, whose query carries the interaction and its nonce.
	consulta, err := url.Parse(interna2(redirect))
	if err != nil {
		return "", "", "", err
	}
	pagina, err := cliente.Get(consulta.String())
	if err != nil {
		return "", "", "", fmt.Errorf("abriendo la pagina de consentimiento: %w", err)
	}
	cuerpo, _ := io.ReadAll(io.LimitReader(pagina.Body, 1<<20))
	pagina.Body.Close()
	if pagina.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("la pagina de consentimiento contesto %d: %s", pagina.StatusCode, resumen(string(cuerpo)))
	}

	interactID := pagina.Request.URL.Query().Get("interactId")
	nonce := pagina.Request.URL.Query().Get("nonce")
	if interactID == "" || nonce == "" {
		return "", "", "", fmt.Errorf("la pagina de consentimiento no trae la interaccion: %s", pagina.Request.URL)
	}

	// The approval, which is the same form post the button of the page sends.
	formulario := url.Values{
		"interactId": {interactID},
		"nonce":      {nonce},
		"choice":     {"accept"},
	}
	aprobacion := interna2(fmt.Sprintf("http://%s/idp/approve", "backend:8000"))
	respuesta, err := cliente.PostForm(aprobacion, formulario)
	if err != nil {
		return "", "", "", fmt.Errorf("aprobando: %w", err)
	}
	cuerpoAprobacion, _ := io.ReadAll(io.LimitReader(respuesta.Body, 1<<20))
	respuesta.Body.Close()
	if respuesta.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("la aprobacion contesto %d: %s", respuesta.StatusCode, resumen(string(cuerpoAprobacion)))
	}

	select {
	case <-ctx.Done():
		return "", "", "", ctx.Err()
	case valores := <-c.recibido:
		interactRef := valores.Get("interact_ref")
		if interactRef == "" {
			return "", "", "", fmt.Errorf("el callback llego sin interact_ref: %v", valores)
		}

		// The interaction's own nonce is the one that travelled in the redirect URL, and it is
		// the second field of the hash.
		return interactRef, nonce, valores.Get("hash"), nil
	case <-time.After(20 * time.Second):
		return "", "", "", errors.New("el servidor de autorizacion no volvio al cliente")
	}
}

// esperarCobro polls the PUBLIC document of the collection point until it shows the amount
// it asked for.
//
// This tool can compare amounts because it knows the amount it asked for. The signed read
// is what the product's route uses, because it also has to tell "not yet" from "no".
func esperarCobro(ctx context.Context, cobroURL string, esperado int64, presupuesto time.Duration) (opclient.Amount, error) {
	limite := time.Now().Add(presupuesto)
	for {
		documento, err := leerCobro(ctx, cobroURL)
		if err == nil && documento.recibido >= esperado {
			return opclient.Amount{Value: fmt.Sprint(documento.recibido), AssetCode: documento.moneda}, nil
		}

		if time.Now().After(limite) {
			if err != nil {
				return opclient.Amount{}, fmt.Errorf("despues de %s: %w", presupuesto, err)
			}

			return opclient.Amount{}, fmt.Errorf("despues de %s recibio %d y se esperaban %d", presupuesto, documento.recibido, esperado)
		}

		select {
		case <-ctx.Done():
			return opclient.Amount{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type cobroPublico struct {
	recibido int64
	moneda   string
}

func leerCobro(ctx context.Context, cobroURL string) (cobroPublico, error) {
	peticion, err := http.NewRequestWithContext(ctx, http.MethodGet, interna2(cobroURL), nil)
	if err != nil {
		return cobroPublico{}, err
	}
	peticion.Header.Set("Accept", "application/json")

	respuesta, err := http.DefaultClient.Do(peticion)
	if err != nil {
		return cobroPublico{}, err
	}
	defer respuesta.Body.Close()
	if respuesta.StatusCode != http.StatusOK {
		return cobroPublico{}, fmt.Errorf("el cobro contesto %d", respuesta.StatusCode)
	}

	var documento struct {
		ReceivedAmount *struct {
			Value      string `json:"value"`
			AssetCode  string `json:"assetCode"`
			AssetScale int    `json:"assetScale"`
		} `json:"receivedAmount"`
	}
	if err := json.NewDecoder(io.LimitReader(respuesta.Body, 1<<20)).Decode(&documento); err != nil {
		return cobroPublico{}, err
	}
	if documento.ReceivedAmount == nil {
		return cobroPublico{}, nil
	}

	recibido := int64(0)
	if _, err := fmt.Sscan(documento.ReceivedAmount.Value, &recibido); err != nil {
		return cobroPublico{}, fmt.Errorf("el monto recibido no es un entero: %q", documento.ReceivedAmount.Value)
	}

	return cobroPublico{recibido: recibido, moneda: documento.ReceivedAmount.AssetCode}, nil
}

// interna rewrites a URL of the demo to the service name that resolves inside the compose
// network, keeping the port. The authorization server announces the identity provider and
// itself with the public host, and inside the network only the service names answer.
func interna(u *url.URL) *url.URL {
	puertos := map[string]string{
		"3000": "rafiki-backend",
		"3006": "rafiki-auth",
		"8000": "backend",
	}
	nombre, ok := puertos[u.Port()]
	if !ok || u.Hostname() == nombre {
		return u
	}

	interna := *u
	interna.Host = nombre + ":" + u.Port()

	return &interna
}

// interna2 is interna for a URL in text form.
func interna2(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	return interna(u).String()
}

// resumen bounds what a failed hop prints, so one bad answer does not fill the terminal.
func resumen(cuerpo string) string {
	limpio := strings.Join(strings.Fields(cuerpo), " ")
	if len(limpio) > 200 {
		return limpio[:200] + "…"
	}

	return limpio
}
