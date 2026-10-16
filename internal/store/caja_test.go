package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// The reservation is the operation that takes money out of a store's reach, so it
// is exercised against a real database, with its own data and its own cleanup.
func TestReservarAgainstADatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set: the reservation runs against the stack")
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

	ahora := time.Now().UTC()
	remove := func() {
		for _, statement := range []string{
			"delete from reservas where id like 'tst_%'",
			"delete from efectivo_disponible where tienda_id like 'tst_%'",
			"delete from tiendas where id like 'tst_%'",
			"delete from personas where id like 'tst_%'",
		} {
			if _, err := database.pool.Exec(ctx, statement); err != nil {
				t.Errorf("cleaning the test data (%s): %v", statement, err)
			}
		}
	}
	remove()
	// Deferred and not registered with t.Cleanup: cleanups run after the defers, so
	// the pool would already be closed and the test would leave rows behind.
	defer remove()

	crearPersona(t, database, ctx, "tst_laura")
	crearTienda(t, database, ctx, "tst_jaime", "comercio", "activa", 800_000, ahora)

	reserva, err := database.Reservar(ctx, ReservarParams{
		ID:        "tst_res_1",
		TiendaID:  "tst_jaime",
		PersonaID: "tst_laura",
		MontoCOP:  200_000,
		Codigo:    "6565",
		Ahora:     ahora,
		Vigencia:  15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Reservar() returned an error: %v", err)
	}

	// The numbers of the contract for a 200.000 withdrawal from a store with 800.000
	// free, the state the cash starts in and the fifteen minutes it stays taken.
	if reserva.ComisionTenderoCOP != 3_000 || reserva.ComisionPlataformaCOP != 2_000 {
		t.Errorf("the reservation costs %d + %d, want 3000 + 2000 as the contract's example", reserva.ComisionTenderoCOP, reserva.ComisionPlataformaCOP)
	}
	if reserva.Estado != "reservada" {
		t.Errorf("Estado = %s, want reservada", reserva.Estado)
	}
	if !reserva.VenceEn.Equal(ahora.Add(15 * time.Minute)) {
		t.Errorf("VenceEn = %v, want fifteen minutes after the reservation (%v)", reserva.VenceEn, ahora.Add(15*time.Minute))
	}
	if libre := libreDe(t, database, ctx, "tst_jaime", ahora); libre != 600_000 {
		t.Errorf("the store has %d free after reserving 200.000 of 800.000, want 600.000", libre)
	}

	// The cash already reserved is not available to anybody else.
	_, err = database.Reservar(ctx, ReservarParams{
		ID: "tst_res_2", TiendaID: "tst_jaime", PersonaID: "tst_laura",
		MontoCOP: 700_000, Codigo: "1111", Ahora: ahora, Vigencia: 15 * time.Minute,
	})
	if !errors.Is(err, ErrSinEfectivoLibre) {
		t.Errorf("reserving more than the free cash returned %v, want ErrSinEfectivoLibre", err)
	}

	// The ceiling of the store's level is not a suggestion.
	crearTienda(t, database, ctx, "tst_basico", "basico", "activa", 800_000, ahora)
	_, err = database.Reservar(ctx, ReservarParams{
		ID: "tst_res_3", TiendaID: "tst_basico", PersonaID: "tst_laura",
		MontoCOP: 400_000, Codigo: "2222", Ahora: ahora, Vigencia: 15 * time.Minute,
	})
	if !errors.Is(err, ErrTopeTienda) {
		t.Errorf("reserving above the level ceiling returned %v, want ErrTopeTienda", err)
	}

	// A store that is not active cannot serve anybody.
	crearTienda(t, database, ctx, "tst_inactiva", "aliado", "inactiva", 800_000, ahora)
	_, err = database.Reservar(ctx, ReservarParams{
		ID: "tst_res_4", TiendaID: "tst_inactiva", PersonaID: "tst_laura",
		MontoCOP: 100_000, Codigo: "3333", Ahora: ahora, Vigencia: 15 * time.Minute,
	})
	if !errors.Is(err, ErrSinEfectivoLibre) {
		t.Errorf("reserving at an inactive store returned %v, want ErrSinEfectivoLibre", err)
	}

	_, err = database.Reservar(ctx, ReservarParams{
		ID: "tst_res_5", TiendaID: "tst_fantasma", PersonaID: "tst_laura",
		MontoCOP: 100_000, Codigo: "4444", Ahora: ahora, Vigencia: 15 * time.Minute,
	})
	if !errors.Is(err, ErrTiendaNoEncontrada) {
		t.Errorf("reserving at a store that does not exist returned %v, want ErrTiendaNoEncontrada", err)
	}

	// THE RACE: two reservations of the same cash, at the same time. Only one can
	// win, and the cash reserved never goes above the cash published.
	crearTienda(t, database, ctx, "tst_carrera", "comercio", "activa", 500_000, ahora)

	var (
		waiting sync.WaitGroup
		mutex   sync.Mutex
		results []error
	)
	for i := 0; i < 2; i++ {
		waiting.Add(1)
		go func(i int) {
			defer waiting.Done()
			_, err := database.Reservar(ctx, ReservarParams{
				ID:        fmt.Sprintf("tst_res_carrera_%d", i),
				TiendaID:  "tst_carrera",
				PersonaID: "tst_laura",
				MontoCOP:  300_000,
				Codigo:    "5555",
				Ahora:     ahora,
				Vigencia:  15 * time.Minute,
			})
			mutex.Lock()
			results = append(results, err)
			mutex.Unlock()
		}(i)
	}
	waiting.Wait()

	if len(results) != 2 {
		t.Fatalf("the race produced %d results, want 2", len(results))
	}
	winners := 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrSinEfectivoLibre):
			// The loser is refused for the right reason.
		default:
			t.Errorf("one of the two reservations failed with %v, and the only reason it can fail is the cash", err)
		}
	}
	if winners != 1 {
		t.Errorf("%d reservations took the same cash, and exactly one can win", winners)
	}
	if libre := libreDe(t, database, ctx, "tst_carrera", ahora); libre != 200_000 {
		t.Errorf("the store has %d free, and one reservation of 300.000 out of 500.000 has to leave 200.000: the reserved cash went past the published one", libre)
	}
}

// The cash of an expired reservation goes back to the store: a reservation nobody
// paid cannot keep holding money.
func TestLiberarVencidasAgainstADatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set: the release runs against the stack")
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

	ahora := time.Now().UTC()
	remove := func() {
		for _, statement := range []string{
			"delete from reservas where id like 'tst_%'",
			"delete from efectivo_disponible where tienda_id like 'tst_%'",
			"delete from tiendas where id like 'tst_%'",
			"delete from personas where id like 'tst_%'",
		} {
			if _, err := database.pool.Exec(ctx, statement); err != nil {
				t.Errorf("cleaning the test data (%s): %v", statement, err)
			}
		}
	}
	remove()
	defer remove()

	crearPersona(t, database, ctx, "tst_laura")
	crearTienda(t, database, ctx, "tst_vencimiento", "comercio", "activa", 500_000, ahora)

	// A reservation that expires one minute after it is made.
	if _, err := database.Reservar(ctx, ReservarParams{
		ID:        "tst_res_vencida",
		TiendaID:  "tst_vencimiento",
		PersonaID: "tst_laura",
		MontoCOP:  200_000,
		Codigo:    "9999",
		Ahora:     ahora,
		Vigencia:  time.Minute,
	}); err != nil {
		t.Fatalf("Reservar() returned an error: %v", err)
	}
	if libre := libreDe(t, database, ctx, "tst_vencimiento", ahora); libre != 300_000 {
		t.Fatalf("the store has %d free after reserving 200.000 of 500.000, want 300.000", libre)
	}

	// Before the deadline nothing is released.
	liberadas, err := database.LiberarVencidas(ctx, ahora)
	if err != nil {
		t.Fatalf("LiberarVencidas() returned an error: %v", err)
	}
	if liberadas != 0 {
		t.Errorf("LiberarVencidas() released %d reservations before their deadline, want none", liberadas)
	}

	// After it, the reservation expires and the whole cash is back.
	liberadas, err = database.LiberarVencidas(ctx, ahora.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("LiberarVencidas() returned an error: %v", err)
	}
	if liberadas != 1 {
		t.Errorf("LiberarVencidas() released %d reservations, want one", liberadas)
	}
	if libre := libreDe(t, database, ctx, "tst_vencimiento", ahora); libre != 500_000 {
		t.Errorf("the store has %d free after the release, want the whole 500.000 back", libre)
	}

	var estado string
	if err := database.pool.QueryRow(ctx, `select estado from reservas where id = 'tst_res_vencida'`).Scan(&estado); err != nil {
		t.Fatalf("reading the state of the expired reservation: %v", err)
	}
	if estado != "vencida" {
		t.Errorf("estado = %s, want vencida", estado)
	}

	// Running it again does nothing: the cash cannot be given back twice.
	liberadas, err = database.LiberarVencidas(ctx, ahora.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("LiberarVencidas() returned an error on the second run: %v", err)
	}
	if liberadas != 0 {
		t.Errorf("LiberarVencidas() released %d reservations on the second run, want none", liberadas)
	}
	if libre := libreDe(t, database, ctx, "tst_vencimiento", ahora); libre != 500_000 {
		t.Errorf("the cash of the store changed on the second run: %d free", libre)
	}
}

func crearPersona(t *testing.T, database *Store, ctx context.Context, id string) {
	t.Helper()

	if _, err := database.pool.Exec(ctx, `
		insert into personas (id, whatsapp, cedula, nombre, wallet_address, nivel)
		values ($1, $2, '1036123456', 'Laura', 'https://x/laura', 1)`, id, "5730000"+id[len(id)-3:],
	); err != nil {
		t.Fatalf("inserting the persona %s: %v", id, err)
	}
}

func crearTienda(t *testing.T, database *Store, ctx context.Context, id, nivel, estado string, publicado int64, fecha time.Time) {
	t.Helper()

	if _, err := database.pool.Exec(ctx, `
		insert into tiendas (id, nombre, direccion, ubicacion, nivel, estado, wallet_address, whatsapp_tendero, nombre_tendero)
		values ($1, $2, 'Cra 50 #92-15', ST_MakePoint(-75.558, 6.2761)::geography, $3, $4, 'https://x/tienda', $5, 'Don Jaime')`,
		id, "Tienda "+id, nivel, estado, "5730000"+id[len(id)-3:],
	); err != nil {
		t.Fatalf("inserting the store %s: %v", id, err)
	}
	if _, err := database.pool.Exec(ctx, `
		insert into efectivo_disponible (tienda_id, fecha, publicado_cop, reservado_cop)
		values ($1, $2::date, $3, 0)`, id, fecha, publicado,
	); err != nil {
		t.Fatalf("publishing the cash of %s: %v", id, err)
	}
}

func libreDe(t *testing.T, database *Store, ctx context.Context, tiendaID string, fecha time.Time) int64 {
	t.Helper()

	var libre int64
	if err := database.pool.QueryRow(ctx, `
		select publicado_cop - reservado_cop
		from efectivo_disponible
		where tienda_id = $1 and fecha = $2::date`, tiendaID, fecha).Scan(&libre); err != nil {
		t.Fatalf("reading the free cash of %s: %v", tiendaID, err)
	}

	return libre
}
