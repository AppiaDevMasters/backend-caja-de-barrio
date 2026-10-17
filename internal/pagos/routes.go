// Package pagos serves the payment routes of the internal surface. For now only the
// virtual wallet of the demo (GET /pagos/saldo); the payments of 8.x come later.
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
}

// Register mounts the payment routes.
func Register(server *api.Server, saldos Saldos) {
	server.Handle("GET /pagos/saldo", saldo(saldos))
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
