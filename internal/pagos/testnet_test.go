package pagos_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/opclient"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/pagos"
)

const (
	andres = "https://ilp.interledger-test.dev/andres-cajadebarrio"
	laura  = "https://ilp.interledger-test.dev/laura-cajadebarrio"
)

func modoTestnet(c *pagos.Config) {
	c.Testnet, c.WalletClienteTestnet, c.WalletTiendaTestnet, c.CupoTestnet = true, andres, laura, 100
}

// The demo's withdrawal on the Interledger testnet: a USD client pays a COP store with
// one quote and one payment, and none of the own ASE's machinery runs.
func TestElModoTestnetPagaDeUSDaCOPConUnSoloCobro(t *testing.T) {
	e := nuevoEscenarioCon(t, modoTestnet)
	e.pedir(t, http.MethodPost, "/caja/reservas/res_1/riesgo", `{"persona_id":"per_cristian","monto_cop":200000}`)

	// The cupo is asked on the testnet wallet, in USD.
	status, body := e.pedir(t, http.MethodPost, "/pagos/cupos", `{"persona_id":"per_cristian","wallet_cliente":"`+walletCristian+`"}`)
	if status != http.StatusCreated || body["limite"] != float64(100) {
		t.Fatalf("the testnet cupo goes in the client's asset: %d %v", status, body)
	}
	if e.datos.cupo.WalletAddress != andres {
		t.Fatalf("the cupo must be on the testnet wallet, got %q", e.datos.cupo.WalletAddress)
	}
	hash := opclient.InteractiveHash("cn", "sn", "ref", "https://auth.cajadebarrio.lat/tenant")
	e.pedir(t, http.MethodPost, "/pagos/cupos/"+body["grant_id"].(string)+"/continuar", `{"interact_ref":"ref","hash":"`+hash+`"}`)

	// One incoming payment, at the store's testnet wallet, for the cash plus its commission.
	_, body = e.pedir(t, http.MethodPost, "/pagos/cobros", `{"reserva_id":"res_1","wallet_tienda":"`+walletTienda+`","monto_cop":200000}`)
	if len(e.op.cobroUnico) != 1 || e.op.cobroUnico[0] != laura+"|203600" || body["cobro_plataforma_url"] != "" {
		t.Fatalf("one collection point at the testnet store: %v %v", e.op.cobroUnico, body)
	}

	// One quote by receiver: USD out, COP in, each with its own asset.
	_, body = e.pedir(t, http.MethodPost, "/pagos/cotizaciones", `{"reserva_id":"res_1"}`)
	if body["moneda"] != "USD" || body["debito"] != float64(52.14) || body["moneda_recibe"] != "COP" || body["recibe"] != float64(203600) {
		t.Fatalf("cross-currency quote = %v", body)
	}
	if !strings.HasPrefix(e.op.quoteUnica[0], andres+"|"+laura) {
		t.Fatalf("the quote goes from the testnet client to the store's incoming payment: %v", e.op.quoteUnica)
	}

	// APROBAR is tied to that quote, and the payment closes when the store's
	// collection point is complete.
	e.pedir(t, http.MethodPost, "/pagos/aprobaciones", `{"reserva_id":"res_1","persona_id":"per_cristian","texto_usuario":"APROBAR"}`)
	e.recibido = 203_600 * 100
	status, body = e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`)
	if status != http.StatusOK || body["estado"] != "completado" || body["modo"] != "testnet" {
		t.Fatalf("pay = %d %v", status, body)
	}
	if e.op.pagoUnico != 1 || e.op.pagos != 0 {
		t.Fatalf("one outgoing payment and none of the two-point calls: unico %d, dos %d", e.op.pagoUnico, e.op.pagos)
	}
	if len(e.liquidez.depositos) != 0 || len(e.liquidez.retiros) != 0 {
		t.Fatalf("the testnet must not touch this ASE's liquidity: %v %v", e.liquidez.depositos, e.liquidez.retiros)
	}
	if _, cobro := e.datos.movimientos[walletTienda+"|cobro_retiro|res_1"]; cobro {
		t.Fatal("the store's real wallet holds the money: no virtual credit")
	}
	if e.datos.movimientos[walletApp+"|comision_plataforma|res_1"] != 2_000 {
		t.Fatalf("Caja de Barrio's 1%% goes to the internal ledger: %v", e.datos.movimientos)
	}
	if _, debito := e.datos.movimientos[walletCristian+"|pago_retiro|res_1"]; debito {
		t.Fatal("the testnet client pays from her real wallet, not the virtual one")
	}
	if e.datos.cupo.Usado != 52.14 {
		t.Fatalf("the cupo is used in USD: %v", e.datos.cupo.Usado)
	}

	// The delivery rule holds: the code works only with the collection point complete.
	e.datos.reserva.Codigo = "4821"
	if _, body := e.pedir(t, http.MethodPost, "/caja/reservas/res_1/entrega-tendero", `{"codigo":"4821"}`); body["estado"] != "pagada" {
		t.Fatalf("delivery = %v", body)
	}
}

func TestEnTestnetSinFondosElPagoFallaConUnMensajeClaro(t *testing.T) {
	e := nuevoEscenarioCon(t, modoTestnet)
	e.op.sinFondos = true
	e.pedir(t, http.MethodPost, "/caja/reservas/res_1/riesgo", `{"persona_id":"per_cristian","monto_cop":200000}`)
	_, body := e.pedir(t, http.MethodPost, "/pagos/cupos", `{"persona_id":"per_cristian"}`)
	hash := opclient.InteractiveHash("cn", "sn", "ref", "https://auth.cajadebarrio.lat/tenant")
	e.pedir(t, http.MethodPost, "/pagos/cupos/"+body["grant_id"].(string)+"/continuar", `{"interact_ref":"ref","hash":"`+hash+`"}`)
	e.pedir(t, http.MethodPost, "/pagos/cobros", `{"reserva_id":"res_1"}`)
	e.pedir(t, http.MethodPost, "/pagos/cotizaciones", `{"reserva_id":"res_1"}`)
	e.pedir(t, http.MethodPost, "/pagos/aprobaciones", `{"reserva_id":"res_1","persona_id":"per_cristian","texto_usuario":"APROBAR"}`)

	// No virtual balance check (the virtual wallet has 1.000.000 COP, which would not
	// stop anything): the testnet itself says there is no money.
	_, body = e.pedir(t, http.MethodPost, "/pagos/pagos", `{"reserva_id":"res_1"}`)
	if body["estado"] != "fallido" || !strings.Contains(body["error"].(string), "fondos suficientes") {
		t.Fatalf("without funds = %v", body)
	}
	if e.datos.reserva.Estado != "reservada" {
		t.Fatalf("a failed payment leaves the reservation open, got %s", e.datos.reserva.Estado)
	}
}
