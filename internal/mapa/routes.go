// Package mapa owns the routes of the map: the nearest stores, the detail of one
// store and the cash a store publishes for the day (contract 6.1, 6.2 and 6.3).
//
// The routes register themselves on the internal surface and never touch a router
// directly: that is the only way in, and a route registered on the caller's own mux
// would answer before the token check and stay unauthenticated.
package mapa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/api"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/caja"
	"github.com/AppiaDevMasters/backend-caja-de-barrio/internal/store"
)

// defaultRadioM is how far the agent looks when the caller does not say.
const defaultRadioM = 1500

// maxTiendas is how many stores the agent offers: the contract fixes three.
const maxTiendas = 3

// zonaColombia is the day boundary of the demo. Colombia has no daylight saving
// time, so a fixed offset is exact and the container needs no timezone database.
var zonaColombia = time.FixedZone("America/Bogota", -5*60*60)

// Tiendas is the data layer this package needs, as an interface so the routes can
// be exercised without a database.
type Tiendas interface {
	TiendasCercanas(ctx context.Context, params store.TiendasCercanasParams) ([]store.TiendaCercana, error)
	Tienda(ctx context.Context, id string, fecha time.Time) (store.Tienda, error)
	PublicarEfectivo(ctx context.Context, tiendaID string, montoCOP int64, fecha time.Time) (int64, error)
}

// Register adds the map's routes to the surface.
//
// The paths carry the prefix the contract gives this section (/internal/mapa), and
// they are relative to the surface, which strips /internal itself.
func Register(server *api.Server, tiendas Tiendas) {
	server.Handle("GET /mapa/tiendas/cercanas", cercanas(tiendas))
	server.Handle("GET /mapa/tiendas/{tienda_id}", detalle(tiendas))
	server.Handle("PUT /mapa/tiendas/{tienda_id}/efectivo", publicar(tiendas))
}

// tiendaCercanaBody is one store of the answer of 6.1, with the keys the contract
// fixes: the agent parses them.
type tiendaCercanaBody struct {
	ID                    string  `json:"id"`
	Nombre                string  `json:"nombre"`
	Direccion             string  `json:"direccion"`
	Metros                float64 `json:"metros"`
	Lat                   float64 `json:"lat"`
	Lon                   float64 `json:"lon"`
	WalletAddress         string  `json:"wallet_address"`
	ComisionTenderoCOP    int64   `json:"comision_tendero_cop"`
	TasaTendero           float64 `json:"tasa_tendero"`
	ComisionPlataformaCOP int64   `json:"comision_plataforma_cop"`
	TotalCOP              int64   `json:"total_cop"`
}

// cercanas answers 6.1: the stores of the levels that cover the amount, that have
// the cash free, closest first, with what the withdrawal costs at each one.
//
// A search that finds nothing is an ANSWER and not a failure: it goes back as a 200
// with an empty list, so the agent tells the person that no store can serve that
// amount instead of telling them that something broke.
func cercanas(tiendas Tiendas) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		lat, err := coordenada(query.Get("lat"), "lat", -90, 90)
		if err != nil {
			api.Fail(w, err)

			return
		}
		lon, err := coordenada(query.Get("lon"), "lon", -180, 180)
		if err != nil {
			api.Fail(w, err)

			return
		}
		monto, err := montoRequerido(query.Get("monto_cop"), "monto_cop")
		if err != nil {
			api.Fail(w, err)

			return
		}
		radio, err := radioDe(query.Get("radio_m"))
		if err != nil {
			api.Fail(w, err)

			return
		}

		encontradas, err := tiendas.TiendasCercanas(r.Context(), store.TiendasCercanasParams{
			Lat:      lat,
			Lon:      lon,
			MontoCOP: monto,
			RadioM:   radio,
			Niveles:  caja.NivelesQueAlcanzan(monto),
			Fecha:    hoy(),
			Maximo:   maxTiendas,
		})
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudieron buscar las tiendas cercanas."))

			return
		}

		tiendasBody := make([]tiendaCercanaBody, 0, len(encontradas))
		for _, encontrada := range encontradas {
			comisiones := caja.ComisionesDe(monto, encontrada.EfectivoLibre)
			tiendasBody = append(tiendasBody, tiendaCercanaBody{
				ID:                    encontrada.ID,
				Nombre:                encontrada.Nombre,
				Direccion:             encontrada.Direccion,
				Metros:                math.Round(encontrada.Metros),
				Lat:                   encontrada.Lat,
				Lon:                   encontrada.Lon,
				WalletAddress:         encontrada.WalletAddress,
				ComisionTenderoCOP:    comisiones.Tendero,
				TasaTendero:           comisiones.TasaTendero,
				ComisionPlataformaCOP: comisiones.Plataforma,
				TotalCOP:              comisiones.Total,
			})
		}

		api.JSON(w, http.StatusOK, map[string]any{"tiendas": tiendasBody})
	}
}

// detalleBody is the store of 6.2.
type detalleBody struct {
	ID               string  `json:"id"`
	Nombre           string  `json:"nombre"`
	Direccion        string  `json:"direccion"`
	Lat              float64 `json:"lat"`
	Lon              float64 `json:"lon"`
	Nivel            string  `json:"nivel"`
	WalletAddress    string  `json:"wallet_address"`
	EfectivoLibreCOP int64   `json:"efectivo_libre_cop"`
}

// detalle answers 6.2: one store with the cash it still has free that day.
func detalle(tiendas Tiendas) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("tienda_id")
		if id == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta el identificador de la tienda."))

			return
		}

		tienda, err := tiendas.Tienda(r.Context(), id, hoy())
		if errors.Is(err, store.ErrTiendaNoEncontrada) {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una tienda con ese identificador."))

			return
		}
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo leer la tienda."))

			return
		}

		api.JSON(w, http.StatusOK, detalleBody{
			ID:               tienda.ID,
			Nombre:           tienda.Nombre,
			Direccion:        tienda.Direccion,
			Lat:              tienda.Lat,
			Lon:              tienda.Lon,
			Nivel:            tienda.Nivel,
			WalletAddress:    tienda.WalletAddress,
			EfectivoLibreCOP: tienda.EfectivoLibre,
		})
	}
}

// publicarEfectivoBody is the request of 6.3.
type publicarEfectivoBody struct {
	MontoCOP int64 `json:"monto_cop"`
}

// publicar answers 6.3: the storekeeper publishes the cash of the day.
func publicar(tiendas Tiendas) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("tienda_id")
		if id == "" {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "Falta el identificador de la tienda."))

			return
		}

		// A field the contract does not fix is a caller bug, and reading it and
		// ignoring it would hide the typo until the cash is wrong.
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		var body publicarEfectivoBody
		if err := decoder.Decode(&body); err != nil {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El cuerpo de la petición no es válido: se espera {\"monto_cop\": <entero>}."))

			return
		}
		if body.MontoCOP < 0 {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El efectivo publicado no puede ser negativo."))

			return
		}

		libre, err := tiendas.PublicarEfectivo(r.Context(), id, body.MontoCOP, hoy())
		if errors.Is(err, store.ErrTiendaNoEncontrada) {
			api.Fail(w, api.Errorf(api.CodeNoEncontrado, "No existe una tienda con ese identificador."))

			return
		}
		if errors.Is(err, store.ErrEfectivoMenorQueReservado) {
			api.Fail(w, api.Errorf(api.CodeDatosInvalidos, "El efectivo publicado no puede ser menor que lo ya reservado."))

			return
		}
		if err != nil {
			api.Fail(w, api.Errorf(api.CodeInterno, "No se pudo publicar el efectivo."))

			return
		}

		api.JSON(w, http.StatusOK, map[string]any{"ok": true, "libre_cop": libre})
	}
}

// hoy is the day the cash counts for, in the zone of the demo.
func hoy() time.Time {
	return time.Now().In(zonaColombia)
}

// coordenada reads one coordinate of the search, with the range that coordinate
// has: a latitude goes from -90 to 90 and a longitude from -180 to 180, and the two
// are not the same range.
func coordenada(raw, nombre string, minimo, maximo float64) (float64, error) {
	valor, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, api.Errorf(api.CodeDatosInvalidos, fmt.Sprintf("El parámetro %s es obligatorio y tiene que ser un número.", nombre))
	}
	if valor < minimo || valor > maximo {
		return 0, api.Errorf(api.CodeDatosInvalidos, fmt.Sprintf("El parámetro %s está fuera del rango de coordenadas.", nombre))
	}

	return valor, nil
}

// montoRequerido reads an amount that has to be there and be positive.
func montoRequerido(raw, nombre string) (int64, error) {
	valor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, api.Errorf(api.CodeDatosInvalidos, fmt.Sprintf("El parámetro %s es obligatorio y tiene que ser un entero en pesos.", nombre))
	}
	if valor <= 0 {
		return 0, api.Errorf(api.CodeDatosInvalidos, fmt.Sprintf("El parámetro %s tiene que ser mayor que cero.", nombre))
	}

	return valor, nil
}

// radioDe reads the optional radius of the search.
func radioDe(raw string) (int, error) {
	if raw == "" {
		return defaultRadioM, nil
	}

	valor, err := strconv.Atoi(raw)
	if err != nil {
		return 0, api.Errorf(api.CodeDatosInvalidos, "El parámetro radio_m tiene que ser un entero en metros.")
	}
	if valor <= 0 {
		return 0, api.Errorf(api.CodeDatosInvalidos, "El parámetro radio_m tiene que ser mayor que cero.")
	}

	return valor, nil
}
