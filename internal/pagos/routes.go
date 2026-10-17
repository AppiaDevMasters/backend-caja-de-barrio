// Package pagos serves the payment routes of the internal surface (contract 8.1 to
// 8.8), the virtual wallet of the demo (GET /pagos/saldo), and the two reads of the
// reservation the payment needs first: its state and its risk check (7.2 and 7.3).
package pagos

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// Saldos reads the virtual wallets.
type Saldos interface {
	BilleteraDe(ctx context.Context, whatsapp string) (store.Billetera, bool, error)
	MovimientosDe(ctx context.Context, wallet string) (store.Billetera, error)
}

// Register mounts the virtual wallet and, when Open Payments is configured, the
// payment routes.
func Register(server *api.Server, deps Dependencias) {
	server.Handle("GET /pagos/saldo", saldo(deps.Datos))
	server.Handle("GET /caja/reservas/{id}", consultarReserva(deps))
	server.Handle("POST /caja/reservas/{id}/riesgo", evaluarRiesgo(deps))
	server.Handle("POST /caja/reservas/{id}/cancelar", cancelar(deps))
	server.Handle("POST /caja/reservas/{id}/entrega-tendero", entregaTendero(deps))
	server.Handle("POST /caja/reservas/{id}/recibido-cliente", recibidoCliente(deps))
	server.Handle("GET /caja/tiendas/{tienda_id}/reservas", reservasDeTienda(deps))
	if deps.OpenPayments == nil {
		return
	}
	if deps.Config.EsperaCobro == 0 {
		deps.Config.EsperaCobro = 15 * time.Second
	}
	if deps.Config.EsperaGrant == 0 {
		deps.Config.EsperaGrant = 3 * time.Second
	}

	server.Handle("GET /pagos/wallets", wallets(deps))
	server.Handle("POST /pagos/cupos", pedirCupo(deps))
	server.Handle("POST /pagos/cupos/{grant_id}/continuar", continuarCupo(deps))
	server.Handle("POST /pagos/cobros", crearCobros(deps))
	server.Handle("POST /pagos/cotizaciones", cotizar(deps))
	server.Handle("POST /pagos/aprobaciones", aprobar(deps))
	server.Handle("POST /pagos/pagos", pagar(deps))
	server.Handle("GET /pagos/cobros/estado", estadoCobro(deps))
}

type movimientoBody struct {
	Tipo  string    `json:"tipo"`
	Monto int64     `json:"monto"`
	En    time.Time `json:"en"`
}

type saldoBody struct {
	WalletAddress string           `json:"wallet_address"`
	Saldo         int64            `json:"saldo"`
	Moneda        string           `json:"moneda"`
	Movimientos   []movimientoBody `json:"movimientos"`
}

func saldo(saldos Saldos) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		whatsapp := strings.TrimSpace(r.URL.Query().Get("whatsapp"))
		if whatsapp == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta el whatsapp."))

			return
		}

		billetera, ok, err := saldos.BilleteraDe(r.Context(), whatsapp)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer la billetera."))

			return
		}
		if !ok {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "Ese número todavía no tiene billetera: primero debe registrarse."))

			return
		}

		body := saldoBody{
			WalletAddress: billetera.WalletAddress,
			Saldo:         billetera.Saldo,
			Moneda:        "COP",
			Movimientos:   []movimientoBody{},
		}
		for _, movimiento := range billetera.Movimientos {
			body.Movimientos = append(body.Movimientos, movimientoBody{Tipo: movimiento.Tipo, Monto: movimiento.Monto, En: movimiento.En})
		}
		api.JSON(w, http.StatusOK, body)
	}
}
