package pagos

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// Datos is what the payment routes read and write.
type Datos interface {
	Saldos

	PersonaPorID(ctx context.Context, id string) (store.Persona, bool, error)
	ReservaRetiroPorID(ctx context.Context, id string) (store.ReservaRetiro, bool, error)
	ReservaRetiroPorCobro(ctx context.Context, incomingPaymentURL string) (store.ReservaRetiro, bool, error)
	MarcarRiesgo(ctx context.Context, reservaID string, ok bool) error
	RetirosPagadosDesde(ctx context.Context, personaID string, desde time.Time) (int, error)
	BloquearPorRiesgo(ctx context.Context, alertaID, personaID, tiendaID, regla, detalle string) error
	GuardarCobros(ctx context.Context, reservaID, tiendaURL, plataformaURL string) error
	GuardarCotizacion(ctx context.Context, reservaID, quoteURL, quotePlataformaURL string, debito float64, moneda string, vence time.Time) error
	RegistrarAprobacion(ctx context.Context, a store.AprobacionPago) error
	AprobacionDeQuote(ctx context.Context, reservaID, quoteURL string) (bool, error)
	GuardarPagos(ctx context.Context, reservaID, outTienda, outPlataforma string, resultado any) error
	GuardarResultado(ctx context.Context, reservaID string, resultado any) error
	MarcarLiquidez(ctx context.Context, reservaID string) error
	MarcarPagada(ctx context.Context, reservaID string) (bool, error)
	GuardarCupoPendiente(ctx context.Context, c store.CupoGrant) error
	CupoPorGrant(ctx context.Context, grantID string) (store.CupoGrant, bool, error)
	CupoPorPersona(ctx context.Context, personaID string) (store.CupoGrant, bool, error)
	AprobarCupo(ctx context.Context, grantID, token, manageURL string, vence time.Time) error
	ActualizarToken(ctx context.Context, personaID, token, manageURL string, vence time.Time) error
	SumarUsado(ctx context.Context, personaID string, debito float64) error
	RegistrarMovimiento(ctx context.Context, id, wallet, tipo string, monto int64, referencia string) error
	ConfirmarEntrega(ctx context.Context, confirmacionID, reservaID, parte string, recibido bool, texto, alertaID string) (store.Entrega, error)
	CancelarReserva(ctx context.Context, reservaID, motivo string) error
	ReservasActivasDeTienda(ctx context.Context, tiendaID string) ([]store.ReservaActiva, error)
}

// OpenPayments is the authenticated client of the withdrawal (internal/opclient).
type OpenPayments interface {
	ResolveWalletAddress(ctx context.Context, walletAddressURL string) (opclient.WalletAddress, error)
	RequestOutgoingPaymentGrant(ctx context.Context, params opclient.OutgoingPaymentGrantParams) (opclient.OutgoingPaymentGrant, error)
	ContinueOutgoingPaymentGrant(ctx context.Context, params opclient.ContinueOutgoingPaymentGrantParams) (opclient.AccessToken, error)
	RotateAccessToken(ctx context.Context, manageURL, accessToken string) (opclient.AccessToken, error)
	CreateIncomingPayments(ctx context.Context, params opclient.CreateIncomingPaymentsParams) (opclient.IncomingPayments, error)
	CreateQuotes(ctx context.Context, params opclient.CreateQuotesParams) (opclient.Quotes, error)
	CreateOutgoingPayments(ctx context.Context, params opclient.CreateOutgoingPaymentsParams) (opclient.OutgoingPayments, error)
}

// Liquidez puts up the liquidity of an outgoing payment (Rafiki's Admin API).
type Liquidez interface {
	DepositOutgoingPaymentLiquidity(ctx context.Context, paymentID string) error
}

// Cobros reads how much a collection point has received, in the smallest unit of its
// asset. It is a read of the public document of the incoming payment.
type Cobros func(ctx context.Context, incomingPaymentURL string) (recibido int64, escala int, err error)

// Config is what the payment routes need besides their collaborators.
type Config struct {
	// FinishURL is where the wallet sends the person back after approving the cupo:
	// the agent's /op/finish (OP_FINISH_URL).
	FinishURL string
	// WalletPlataforma is the COP wallet that collects Caja de Barrio's 1%.
	WalletPlataforma string
	// AuthServerHash is the URL the authorization server binds into the callback
	// hash: its public base plus the tenant.
	AuthServerHash string
	// HostASE is the host of the wallets this backend serves. A client wallet there
	// holds its money in the virtual wallet, so the ASE puts up the liquidity.
	HostASE string
	// Secreto encrypts the access tokens at rest.
	Secreto []byte
	// EsperaCobro is how long a payment waits for the store's collection point.
	EsperaCobro time.Duration
	// EsperaGrant is the wait GNAP imposes before a grant can be continued.
	EsperaGrant time.Duration
}

// Dependencias are the collaborators of the payment routes.
type Dependencias struct {
	Datos        Datos
	OpenPayments OpenPayments
	Liquidez     Liquidez
	Cobros       Cobros
	Config       Config
}

// limitesCupo are the ceilings of the contract's 8.2, in whole units of the asset.
var limitesCupo = map[string]int64{"COP": 1_000_000, "USD": 300}

// normalizarWallet turns the $ilp form into the URL the SDK needs.
func normalizarWallet(raw string) string {
	w := strings.TrimSpace(raw)
	if strings.HasPrefix(w, "$") {
		w = "https://" + strings.TrimPrefix(w, "$")
	}

	return strings.TrimSuffix(w, "/")
}

func mismaWallet(a, b string) bool {
	return normalizarWallet(a) == normalizarWallet(b)
}

// enteros turns an amount in the smallest unit into whole units (pesos, dollars).
func enteros(a opclient.Amount) (float64, error) {
	valor, err := strconv.ParseFloat(a.Value, 64)
	if err != nil {
		return 0, fmt.Errorf("pagos: the amount %q is not a number: %w", a.Value, err)
	}

	return valor / math.Pow10(a.AssetScale), nil
}

func nuevoID(prefijo string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return prefijo + hex.EncodeToString(b), nil
}

// cifrar seals an access token with AES-GCM: the contract keeps tokens encrypted.
func cifrar(secreto []byte, texto string) (string, error) {
	clave := sha256.Sum256(append([]byte("caja-de-barrio/cupos:"), secreto...))
	bloque, err := aes.NewCipher(clave[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(bloque)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}

	return base64.RawStdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(texto), nil)), nil
}

func descifrar(secreto []byte, sellado string) (string, error) {
	datos, err := base64.RawStdEncoding.DecodeString(sellado)
	if err != nil {
		return "", err
	}
	clave := sha256.Sum256(append([]byte("caja-de-barrio/cupos:"), secreto...))
	bloque, err := aes.NewCipher(clave[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(bloque)
	if err != nil {
		return "", err
	}
	if len(datos) < gcm.NonceSize() {
		return "", errors.New("pagos: the sealed token is too short")
	}
	abierto, err := gcm.Open(nil, datos[:gcm.NonceSize()], datos[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}

	return string(abierto), nil
}

// LeerCobroPublico reads the public document of an incoming payment. It is the read
// the withdrawal driver proved against Rafiki (cmd/retiro).
func LeerCobroPublico(client *http.Client) Cobros {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	return func(ctx context.Context, incomingPaymentURL string) (int64, int, error) {
		if _, err := url.Parse(incomingPaymentURL); err != nil {
			return 0, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, incomingPaymentURL, nil)
		if err != nil {
			return 0, 0, err
		}
		req.Header.Set("Accept", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return 0, 0, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return 0, 0, fmt.Errorf("pagos: the collection point answered %d", res.StatusCode)
		}

		var documento struct {
			ReceivedAmount *struct {
				Value      string `json:"value"`
				AssetScale int    `json:"assetScale"`
			} `json:"receivedAmount"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&documento); err != nil {
			return 0, 0, err
		}
		if documento.ReceivedAmount == nil {
			return 0, 0, nil
		}
		recibido, err := strconv.ParseInt(documento.ReceivedAmount.Value, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("pagos: the received amount %q is not whole", documento.ReceivedAmount.Value)
		}

		return recibido, documento.ReceivedAmount.AssetScale, nil
	}
}
