package caja

import (
	"math"
	"testing"
)

// The contract prints this exact case in its section 6.1 (a withdrawal of 200.000
// from a store with 800.000 free), so it is the anchor of the whole rule: if this
// one moves, the money the agent shows moves with it.
func TestComisionesDeMatchesTheContractsExample(t *testing.T) {
	got := ComisionesDe(200_000, 800_000)

	want := Comisiones{Tendero: 3_000, TasaTendero: 0.015, Plataforma: 2_000, Total: 205_000}
	if !sameRate(got.TasaTendero, want.TasaTendero) || got.Tendero != want.Tendero || got.Plataforma != want.Plataforma || got.Total != want.Total {
		t.Errorf("ComisionesDe(200000, 800000) = %+v, want %+v", got, want)
	}
}

// The floor of a thousand pesos: a withdrawal of ten thousand still costs the
// storekeeper a thousand, and the rate it reports is the one that is really being
// charged (ten percent), not the rate of the formula, so the numbers of the answer
// always agree with each other.
func TestComisionesDeAppliesTheMinimum(t *testing.T) {
	got := ComisionesDe(10_000, 800_000)

	if got.Tendero != ComisionTenderoMinima {
		t.Errorf("Tendero = %d, want the minimum of %d", got.Tendero, ComisionTenderoMinima)
	}
	if !sameRate(got.TasaTendero, 0.1) {
		t.Errorf("TasaTendero = %v, want the effective ten percent of a floored commission", got.TasaTendero)
	}
	if got.Plataforma != 100 || got.Total != 11_100 {
		t.Errorf("ComisionesDe(10000, 800000) = %+v, want 100 of platform and 11100 in total", got)
	}
}

// The store charges more the bigger a share of its cash the withdrawal takes, and
// never more than three percent.
func TestComisionesDeGrowsWithTheShareOfCashAndCapsAtThreePercent(t *testing.T) {
	tests := []struct {
		name       string
		efectivo   int64
		wantTender int64
	}{
		{"a withdrawal of one percent of the cash", 10_000_000, 1_020},
		{"a withdrawal of ten percent of the cash", 1_000_000, 1_200},
		{"a withdrawal of all the cash", 100_000, 3_000},
		{"a withdrawal bigger than the cash", 50_000, 3_000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ComisionesDe(100_000, test.efectivo)
			if got.Tendero != test.wantTender {
				t.Errorf("ComisionesDe(100000, %d).Tendero = %d, want %d", test.efectivo, got.Tendero, test.wantTender)
			}
		})
	}
}

// A store with no free cash is not a division by zero: it is the most expensive
// rate, and the search never offers such a store anyway.
func TestComisionesDeWithoutFreeCash(t *testing.T) {
	got := ComisionesDe(100_000, 0)

	if got.Tendero != 3_000 {
		t.Errorf("Tendero = %d, want the most expensive rate (%d)", got.Tendero, 3_000)
	}
}

// The store levels whose ceiling covers the amount: a level left out of this list
// is a store the agent will not offer, so the amount has to decide it.
func TestNivelesQueAlcanzan(t *testing.T) {
	tests := []struct {
		monto int64
		want  []string
	}{
		{200_000, []string{"basico", "comercio", "aliado"}},
		{500_000, []string{"comercio", "aliado"}},
		{2_000_000, []string{"aliado"}},
		{4_000_000, []string{}},
	}

	for _, test := range tests {
		got := NivelesQueAlcanzan(test.monto)
		if len(got) != len(test.want) {
			t.Fatalf("NivelesQueAlcanzan(%d) = %v, want %v", test.monto, got, test.want)
		}
		for i := range got {
			if got[i] != test.want[i] {
				t.Errorf("NivelesQueAlcanzan(%d) = %v, want %v", test.monto, got, test.want)
			}
		}
	}
}

// A level nobody defined has no ceiling, and no ceiling means no amount fits: the
// safe answer for money is zero and not a generous default.
func TestTopesOfUnknownLevelsAreZero(t *testing.T) {
	if got := TopeTienda("platino"); got != 0 {
		t.Errorf("TopeTienda(platino) = %d, want 0", got)
	}
	if got := TopePersona(9); got != 0 {
		t.Errorf("TopePersona(9) = %d, want 0", got)
	}
	if got := TopeTienda("comercio"); got != TopeComercio {
		t.Errorf("TopeTienda(comercio) = %d, want %d", got, TopeComercio)
	}
	if got := TopePersona(2); got != TopePersonaNivel2 {
		t.Errorf("TopePersona(2) = %d, want %d", got, TopePersonaNivel2)
	}
}

// sameRate compares two rates without the noise of binary floating point.
func sameRate(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
