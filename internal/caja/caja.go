// Package caja holds the money rules of Caja de Barrio: what a withdrawal costs on
// top of the amount, and the ceilings each store and each person has.
//
// They are the contract's rule 4 and the ceilings of its section 4, and they live
// here because the map estimates them and a reservation charges them: two
// computations of the same money that disagreed would be a defect nobody notices
// until a person is charged the wrong amount.
package caja

import "math"

// The ceilings of a store by level, in whole pesos (contract section 4).
const (
	TopeBasico   int64 = 300_000
	TopeComercio int64 = 1_000_000
	TopeAliado   int64 = 3_000_000
)

// The ceilings of a person by level, in whole pesos (contract section 4).
const (
	TopePersonaNivel1 int64 = 500_000
	TopePersonaNivel2 int64 = 2_000_000
)

// Niveles are the store levels, from the smallest ceiling to the largest.
var Niveles = []string{"basico", "comercio", "aliado"}

// TopeTienda is the ceiling of a store level, and zero for a level nobody defined:
// a store whose level cannot be read is a store no amount fits, which is the safe
// answer for money.
func TopeTienda(nivel string) int64 {
	switch nivel {
	case "basico":
		return TopeBasico
	case "comercio":
		return TopeComercio
	case "aliado":
		return TopeAliado
	default:
		return 0
	}
}

// TopePersona is the ceiling of a person level, and zero for a level nobody defined.
func TopePersona(nivel int) int64 {
	switch nivel {
	case 1:
		return TopePersonaNivel1
	case 2:
		return TopePersonaNivel2
	default:
		return 0
	}
}

// NivelesQueAlcanzan returns the store levels whose ceiling covers the amount. The
// contract filters the nearest-store search with it, and a level left out is a
// store that cannot serve the withdrawal.
func NivelesQueAlcanzan(montoCOP int64) []string {
	alcanzan := make([]string, 0, len(Niveles))
	for _, nivel := range Niveles {
		if TopeTienda(nivel) >= montoCOP {
			alcanzan = append(alcanzan, nivel)
		}
	}

	return alcanzan
}

// ComisionTenderoMinima is the floor of what a store charges: a withdrawal of a few
// thousand pesos still costs the storekeeper a thousand.
const ComisionTenderoMinima int64 = 1_000

// TasaPlataforma is what Caja de Barrio charges on every withdrawal.
const TasaPlataforma = 0.01

// TasaTenderoBase is where the store's rate starts, before the cash it still has
// free makes it grow.
const TasaTenderoBase = 0.01

// TasaTenderoExtra is how much the rate grows when the withdrawal takes all the
// cash the store has free.
const TasaTenderoExtra = 0.02

// Comisiones is what one withdrawal costs on top of the amount.
type Comisiones struct {
	// Tendero is what the store charges.
	Tendero int64

	// TasaTendero is the rate that commission corresponds to, that is the
	// commission over the amount. It is the EFFECTIVE rate and not the rate of the
	// formula, so the three numbers of this struct always agree: with the minimum of
	// a thousand pesos on a ten thousand withdrawal, the rate is ten percent, which
	// is what is being charged.
	TasaTendero float64

	// Plataforma is what Caja de Barrio charges.
	Plataforma int64

	// Total is the amount plus both commissions.
	Total int64
}

// ComisionesDe computes what a withdrawal of montoCOP costs when the store still has
// efectivoLibreCOP free:
//
//	comision_tendero    = max(1000, round(monto x (0.01 + 0.02 x min(monto / efectivo_libre, 1))))
//	comision_plataforma = round(monto x 0.01)
//
// The store charges more the bigger a share of its cash the withdrawal takes, up to
// three percent, and never less than a thousand pesos.
func ComisionesDe(montoCOP, efectivoLibreCOP int64) Comisiones {
	tasa := TasaTenderoBase + TasaTenderoExtra*proporcion(montoCOP, efectivoLibreCOP)

	tendero := int64(math.Round(float64(montoCOP) * tasa))
	if tendero < ComisionTenderoMinima {
		tendero = ComisionTenderoMinima
	}
	plataforma := int64(math.Round(float64(montoCOP) * TasaPlataforma))

	comisiones := Comisiones{
		Tendero:     tendero,
		Plataforma:  plataforma,
		Total:       montoCOP + tendero + plataforma,
		TasaTendero: 0,
	}
	if montoCOP > 0 {
		comisiones.TasaTendero = float64(tendero) / float64(montoCOP)
	}

	return comisiones
}

// proporcion is min(monto / efectivoLibre, 1): how much of the store's free cash the
// withdrawal takes, capped at all of it. A store with no free cash is a store whose
// cash is all taken, which is the most expensive rate and not a division by zero.
func proporcion(montoCOP, efectivoLibreCOP int64) float64 {
	if efectivoLibreCOP <= 0 {
		return 1
	}

	share := float64(montoCOP) / float64(efectivoLibreCOP)
	if share > 1 {
		return 1
	}

	return share
}
