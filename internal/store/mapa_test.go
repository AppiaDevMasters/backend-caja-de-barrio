package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// The nearest-store search is PostGIS work, so it is exercised against a real
// database when one is configured. It brings its own stores and removes them, so a
// run never leaves data behind for the demo or for the next run.
func TestTiendasCercanasAgainstADatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set: this search runs against the stack")
	}

	ctx := context.Background()
	database, err := Open(ctx, Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatalf("Open() returned an error: %v", err)
	}
	defer database.Close()

	if _, err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() returned an error: %v", err)
	}

	// The origin and the amount are the ones of the contract's own example (6.1),
	// so the distances this test sees are the ones the contract prints.
	const (
		lat      = 6.2766
		lon      = -75.5583
		amount   = int64(200000)
		radioM   = 1500
		niveles  = "comercio,aliado"
		publicad = int64(200000)
	)
	today := time.Now().UTC()

	remove := func() {
		if _, err := database.pool.Exec(ctx, "delete from efectivo_disponible where tienda_id like 'tst_%'"); err != nil {
			t.Errorf("removing the test cash: %v", err)
		}
		if _, err := database.pool.Exec(ctx, "delete from tiendas where id like 'tst_%'"); err != nil {
			t.Errorf("removing the test stores: %v", err)
		}
	}
	remove()
	// The removal is deferred HERE and not registered with t.Cleanup on purpose:
	// cleanups run after the defers, so the pool of this test would already be
	// closed and the test would leave its stores behind for the demo.
	defer remove()

	// Every store below is here to be included or excluded for exactly one reason:
	// a, b and c qualify and their order is their distance; d has a level that
	// cannot serve the amount; e published less than the amount; f is inactive; and
	// g is outside the radius.
	stores := []struct {
		id     string
		lat    float64
		lon    float64
		nivel  string
		estado string
		cash   int64
	}{
		{"tst_a", 6.2761, -75.5580, "comercio", "activa", publicad},
		{"tst_b", 6.2770, -75.5560, "aliado", "activa", publicad},
		{"tst_c", 6.2800, -75.5600, "comercio", "activa", publicad},
		{"tst_d", 6.2761, -75.5580, "basico", "activa", publicad},
		{"tst_e", 6.2761, -75.5580, "comercio", "activa", 50000},
		{"tst_f", 6.2760, -75.5580, "comercio", "inactiva", publicad},
		{"tst_g", 6.2900, -75.5800, "comercio", "activa", publicad},
	}
	for _, s := range stores {
		if _, err := database.pool.Exec(ctx, `
			insert into tiendas (id, nombre, direccion, ubicacion, nivel, estado, wallet_address, whatsapp_tendero, nombre_tendero)
			values ($1, $2, 'Cra 50 #92-15', ST_MakePoint($3, $4)::geography, $5, $6, 'https://x/tienda', '573000000010', 'Don Jaime')`,
			s.id, "Tienda "+s.id, s.lon, s.lat, s.nivel, s.estado,
		); err != nil {
			t.Fatalf("inserting the store %s: %v", s.id, err)
		}
		if _, err := database.pool.Exec(ctx, `
			insert into efectivo_disponible (tienda_id, fecha, publicado_cop, reservado_cop)
			values ($1, $2::date, $3, 0)`, s.id, today, s.cash,
		); err != nil {
			t.Fatalf("publishing the cash of %s: %v", s.id, err)
		}
	}

	search := func(mutate func(*TiendasCercanasParams)) ([]TiendaCercana, error) {
		params := TiendasCercanasParams{
			Lat:      lat,
			Lon:      lon,
			MontoCOP: amount,
			RadioM:   radioM,
			Niveles:  []string{"comercio", "aliado"},
			Fecha:    today,
			Maximo:   3,
		}
		mutate(&params)

		return database.TiendasCercanas(ctx, params)
	}

	got, err := search(func(*TiendasCercanasParams) {})
	if err != nil {
		t.Fatalf("TiendasCercanas() returned an error: %v", err)
	}

	want := []string{"tst_a", "tst_b", "tst_c"}
	if len(got) != len(want) {
		t.Fatalf("TiendasCercanas() returned %d stores (%v), want %v: only those can serve the amount", len(got), ids(got), want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("TiendasCercanas()[%d] = %s, want %s: the order is the distance", i, got[i].ID, id)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].Metros < got[i-1].Metros {
			t.Errorf("the stores are not ordered by distance: %v", got)
		}
	}

	// The fields the contract asks for, and the cash the commissions are computed
	// from.
	first := got[0]
	if first.Nombre == "" || first.Direccion == "" || first.WalletAddress == "" {
		t.Errorf("the answer is missing the fields the contract prints: %+v", first)
	}
	if first.EfectivoLibre != publicad {
		t.Errorf("EfectivoLibre = %d, want what the store published (%d)", first.EfectivoLibre, publicad)
	}
	if first.Lat < 6.27 || first.Lat > 6.28 || first.Lon < -75.56 || first.Lon > -75.55 {
		t.Errorf("Lat/Lon = %v/%v, want the store's own point", first.Lat, first.Lon)
	}
	if first.Metros < 60 || first.Metros > 70 {
		t.Errorf("Metros = %v, want about 65 as the contract's own example prints", first.Metros)
	}

	// The list is capped: the agent offers three stores, not a directory.
	capped, err := search(func(p *TiendasCercanasParams) { p.Maximo = 2 })
	if err != nil {
		t.Fatalf("TiendasCercanas() returned an error: %v", err)
	}
	if len(capped) != 2 {
		t.Errorf("TiendasCercanas() with Maximo 2 returned %d stores, want 2", len(capped))
	}

	// A store that published nothing that day cannot serve anybody, and the day is
	// the caller's.
	tomorrow, err := search(func(p *TiendasCercanasParams) { p.Fecha = today.AddDate(0, 0, 1) })
	if err != nil {
		t.Fatalf("TiendasCercanas() returned an error: %v", err)
	}
	if len(tomorrow) != 0 {
		t.Errorf("TiendasCercanas() for a day without published cash returned %v, want nothing", ids(tomorrow))
	}

	// The filters cannot be forgotten silently.
	if _, err := search(func(p *TiendasCercanasParams) { p.Niveles = nil }); err == nil {
		t.Error("TiendasCercanas() accepted an empty level list, want an error: no level means no store")
	}
	if _, err := search(func(p *TiendasCercanasParams) { p.Maximo = 0 }); err == nil {
		t.Error("TiendasCercanas() accepted Maximo 0, want an error")
	}
	if _, err := search(func(p *TiendasCercanasParams) { p.RadioM = -1 }); err == nil {
		t.Error("TiendasCercanas() accepted a negative radius, want an error")
	}
}

func ids(stores []TiendaCercana) []string {
	names := make([]string, 0, len(stores))
	for _, store := range stores {
		names = append(names, store.ID)
	}

	return names
}
