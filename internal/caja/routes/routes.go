// Package routes owns the HTTP routes of the cash box. It is a subpackage rather
// than package caja because the store imports caja for the money rules; keeping the
// handlers here avoids a store -> caja -> store import cycle.
package routes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/identidad"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

const reservaVigencia = 15 * time.Minute

// Datos is the reservation data layer. The transaction remains in store.Reservar;
// the route only decides which request may reach it and shapes the contract reply.
type Datos interface {
	ElegibilidadDe(ctx context.Context, personaID, versionPoliticas string, ahora time.Time) (store.Elegibilidad, error)
	Reservar(ctx context.Context, params store.ReservarParams) (store.Reserva, error)
}

// Expirador is the part of the store used by the background release loop.
type Expirador interface {
	LiberarVencidas(ctx context.Context, ahora time.Time) (int, error)
}

// StartExpiryReleaser releases reservations whose fifteen minutes elapsed. The
// goroutine stops with the backend context, so shutdown never leaves a ticker or a
// database call behind.
func StartExpiryReleaser(ctx context.Context, expirador Expirador, cada time.Duration, logger *slog.Logger) {
	if cada <= 0 {
		cada = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}

	go func() {
		ticker := time.NewTicker(cada)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ahora := <-ticker.C:
				liberadas, err := expirador.LiberarVencidas(ctx, ahora.UTC())
				if err != nil {
					logger.Error("could not release expired reservations", "error", err)
					continue
				}
				if liberadas > 0 {
					logger.Info("released expired reservations", "count", liberadas)
				}
			}
		}
	}()
}

// Register adds the cash-box routes to the authenticated internal surface.
func Register(server *api.Server, datos Datos) {
	server.Handle("POST /caja/reservas", reservar(datos))
}

type reservaRequest struct {
	TiendaID  string `json:"tienda_id"`
	PersonaID string `json:"persona_id"`
	MontoCOP  int64  `json:"monto_cop"`
}

type reservaResponse struct {
	ReservaID             string  `json:"reserva_id"`
	Codigo                string  `json:"codigo_4_digitos"`
	VenceEn               string  `json:"vence_en"`
	ComisionTenderoCOP    int64   `json:"comision_tendero_cop"`
	TasaTendero           float64 `json:"tasa_tendero"`
	ComisionPlataformaCOP int64   `json:"comision_plataforma_cop"`
	TotalCOP              int64   `json:"total_cop"`
}

func reservar(datos Datos) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input reservaRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la reserva no es válido."))
			return
		}
		input.TiendaID = strings.TrimSpace(input.TiendaID)
		input.PersonaID = strings.TrimSpace(input.PersonaID)
		if input.TiendaID == "" || input.PersonaID == "" || input.MontoCOP <= 0 {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "La tienda, la persona y el monto son obligatorios."))
			return
		}

		ahora := time.Now().UTC()
		eligibilidad, err := datos.ElegibilidadDe(r.Context(), input.PersonaID, identidad.VersionPoliticas, ahora)
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo comprobar la elegibilidad de la persona."))
			return
		}
		if !eligibilidad.Existe {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe la persona indicada."))
			return
		}
		if !eligibilidad.PoliticasAceptadas {
			api.Fail(w, api.Errorf(api.CodeSinPoliticas, "La persona debe aceptar las políticas vigentes."))
			return
		}
		if eligibilidad.EstadoRegistro != "aprobada" {
			api.Fail(w, api.Errorf(api.CodePersonaNoAprobada, "La persona todavía no está aprobada para operar."))
			return
		}
		if !eligibilidad.VerificacionVigente {
			api.Fail(w, api.Errorf(api.CodeSinVerificacion, "La verificación de identidad no está vigente."))
			return
		}

		id, err := randomID("res_")
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo generar la reserva."))
			return
		}
		codigo, err := randomCode()
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo generar el código de la reserva."))
			return
		}

		reserva, err := datos.Reservar(r.Context(), store.ReservarParams{
			ID:        id,
			TiendaID:  input.TiendaID,
			PersonaID: input.PersonaID,
			MontoCOP:  input.MontoCOP,
			Codigo:    codigo,
			Ahora:     ahora,
			Vigencia:  reservaVigencia,
		})
		if err != nil {
			api.Fail(w, reservationError(err))
			return
		}

		tasa := 0.0
		if reserva.MontoCOP > 0 {
			tasa = float64(reserva.ComisionTenderoCOP) / float64(reserva.MontoCOP)
		}
		api.JSON(w, http.StatusCreated, reservaResponse{
			ReservaID:             reserva.ID,
			Codigo:                reserva.Codigo,
			VenceEn:               reserva.VenceEn.UTC().Format(time.RFC3339),
			ComisionTenderoCOP:    reserva.ComisionTenderoCOP,
			TasaTendero:           tasa,
			ComisionPlataformaCOP: reserva.ComisionPlataformaCOP,
			TotalCOP:              reserva.MontoCOP + reserva.ComisionTenderoCOP + reserva.ComisionPlataformaCOP,
		})
	}
}

func reservationError(err error) error {
	switch {
	case errors.Is(err, store.ErrTiendaNoEncontrada):
		return api.Errorf(api.CodeNoEncontrado, "No existe la tienda indicada.")
	case errors.Is(err, store.ErrTopeTienda):
		return api.Errorf(api.CodeTopeExcedido, "El monto supera el tope de la tienda.")
	case errors.Is(err, store.ErrSinEfectivoLibre):
		return api.Errorf(api.CodeTiendaNoDisponible, "La tienda no tiene efectivo libre suficiente.")
	default:
		return api.Errorf(api.CodeInterno, "No se pudo reservar el efectivo.")
	}
}

func randomID(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generating random id: %w", err)
	}
	return prefix + hex.EncodeToString(bytes[:]), nil
}

func randomCode() (string, error) {
	number, err := rand.Int(rand.Reader, big.NewInt(10_000))
	if err != nil {
		return "", fmt.Errorf("generating random code: %w", err)
	}
	return fmt.Sprintf("%04d", number.Int64()), nil
}
