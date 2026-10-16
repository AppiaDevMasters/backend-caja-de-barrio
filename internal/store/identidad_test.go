package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// The identity queries run against a real database, with their own rows, because what
// they read decides whether a person can take a store's cash.
func TestIdentidadAgainstADatabase(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set: the identity queries run against the stack")
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

	ahora := time.Now().In(zonaBogota)
	// The acceptances of the policies are the one thing this test cannot clean: the
	// schema refuses UPDATE and DELETE on that table. So it records its acceptance with
	// a number of its own, which belongs to nobody, and leaves that row behind on
	// purpose.
	limpiarDatosDePrueba(t, database, ctx)
	defer limpiarDatosDePrueba(t, database, ctx)

	// A person who has accepted nothing and is not approved yet.
	persona := Persona{
		ID:             "tst_persona",
		Whatsapp:       "573000000777",
		Cedula:         "1036123456",
		Nombre:         "Laura",
		WalletAddress:  "https://x/laura",
		Nivel:          1,
		EstadoRegistro: "pendiente",
	}
	if err := database.CrearPersona(ctx, persona); err != nil {
		t.Fatalf("CrearPersona() returned an error: %v", err)
	}

	// The same number twice is the contract's YA_REGISTRADA and not a database error
	// the route would have to read.
	if err := database.CrearPersona(ctx, persona); !errors.Is(err, ErrYaRegistrada) {
		t.Errorf("registering the same number twice returned %v, want ErrYaRegistrada", err)
	}

	porID, existe, err := database.PersonaPorID(ctx, persona.ID)
	if err != nil || !existe {
		t.Fatalf("PersonaPorID() = %+v, %v, %v, want the person that was just registered", porID, existe, err)
	}
	if porID.Nombre != "Laura" || porID.EstadoRegistro != "pendiente" || porID.Cedula != "1036123456" {
		t.Errorf("PersonaPorID() = %+v, want what was registered", porID)
	}

	porWhatsapp, existe, err := database.PersonaPorWhatsapp(ctx, persona.Whatsapp)
	if err != nil || !existe || porWhatsapp.ID != persona.ID {
		t.Errorf("PersonaPorWhatsapp() = %+v, %v, %v, want the same person", porWhatsapp, existe, err)
	}

	_, existe, err = database.PersonaPorWhatsapp(ctx, "573000000000")
	if err != nil || existe {
		t.Errorf("PersonaPorWhatsapp() of an unknown number = %v, %v, want it to not exist and not to be an error", existe, err)
	}

	// Nothing accepted and nothing verified: the reservation has to be refused for
	// both reasons.
	elegibilidad, err := database.ElegibilidadDe(ctx, persona.ID, "v1-2026-10", ahora)
	if err != nil {
		t.Fatalf("ElegibilidadDe() returned an error: %v", err)
	}
	if !elegibilidad.Existe || elegibilidad.PoliticasAceptadas || elegibilidad.VerificacionVigente {
		t.Errorf("ElegibilidadDe() = %+v, want the person to exist without policies and without verification", elegibilidad)
	}
	if elegibilidad.EstadoRegistro != "pendiente" {
		t.Errorf("EstadoRegistro = %s, want the stored state, because the route needs it to choose its code", elegibilidad.EstadoRegistro)
	}

	// Accepting the current version is what the contract's 5.2 records.
	if err := database.AceptarPoliticas(ctx, "tst_pol", "tst_sin_persona", "v1-2026-10", "Acepto"); err != nil {
		t.Fatalf("AceptarPoliticas() returned an error: %v", err)
	}
	// Twice adds nothing to the trail.
	if err := database.AceptarPoliticas(ctx, "tst_pol_otra", "tst_sin_persona", "v1-2026-10", "Acepto"); err != nil {
		t.Fatalf("AceptarPoliticas() returned an error on the second call: %v", err)
	}
	var aceptaciones int
	if err := database.pool.QueryRow(ctx, `select count(*) from aceptaciones_politicas where whatsapp = 'tst_sin_persona'`).Scan(&aceptaciones); err != nil {
		t.Fatalf("counting the acceptances: %v", err)
	}
	if aceptaciones != 1 {
		t.Errorf("the number has %d acceptances of the same version, want one", aceptaciones)
	}

	// An acceptance of ANOTHER version does not count for the current one. Each
	// acceptance carries its own id: it is the primary key, and reusing it is a caller
	// bug that the database refuses.
	if err := database.AceptarPoliticas(ctx, "tst_pol_vieja", persona.Whatsapp, "v0-2025-01", "Acepto"); err != nil {
		t.Fatalf("AceptarPoliticas() returned an error: %v", err)
	}
	elegibilidad, err = database.ElegibilidadDe(ctx, persona.ID, "v1-2026-10", ahora)
	if err != nil {
		t.Fatalf("ElegibilidadDe() returned an error: %v", err)
	}
	if elegibilidad.PoliticasAceptadas {
		t.Error("PoliticasAceptadas = true, and the person only accepted another version")
	}
	if err := database.AceptarPoliticas(ctx, "tst_pol_actual", persona.Whatsapp, "v1-2026-10", "Acepto"); err != nil {
		t.Fatalf("AceptarPoliticas() returned an error: %v", err)
	}

	// Approved with a verification that is still inside its minutes.
	if _, err := database.pool.Exec(ctx, `update personas set estado_registro = 'aprobada', verificado_hasta = $2 where id = $1`, persona.ID, ahora.Add(30*time.Minute)); err != nil {
		t.Fatalf("approving the person: %v", err)
	}
	elegibilidad, err = database.ElegibilidadDe(ctx, persona.ID, "v1-2026-10", ahora)
	if err != nil {
		t.Fatalf("ElegibilidadDe() returned an error: %v", err)
	}
	if !elegibilidad.PoliticasAceptadas || !elegibilidad.VerificacionVigente || elegibilidad.EstadoRegistro != "aprobada" {
		t.Errorf("ElegibilidadDe() = %+v, want an approved person with policies and a current verification", elegibilidad)
	}

	// The thirty minutes are a window and not a promise: a verification that already
	// passed does not let anybody reserve.
	if _, err := database.pool.Exec(ctx, `update personas set verificado_hasta = $2 where id = $1`, persona.ID, ahora.Add(-time.Minute)); err != nil {
		t.Fatalf("expiring the verification: %v", err)
	}
	elegibilidad, err = database.ElegibilidadDe(ctx, persona.ID, "v1-2026-10", ahora)
	if err != nil {
		t.Fatalf("ElegibilidadDe() returned an error: %v", err)
	}
	if elegibilidad.VerificacionVigente {
		t.Error("VerificacionVigente = true with a verification that already passed, and the window is the rule")
	}

	elegibilidad, err = database.ElegibilidadDe(ctx, "tst_fantasma", "v1-2026-10", ahora)
	if err != nil {
		t.Fatalf("ElegibilidadDe() of a person that does not exist returned an error: %v", err)
	}
	if elegibilidad.Existe {
		t.Error("ElegibilidadDe() says a person that does not exist exists")
	}

	// The other shape of 5.1: the same kind of number, but the store's.
	crearTienda(t, database, ctx, "tst_tienda_tendero", "comercio", "activa", 400_000, ahora)
	if _, err := database.pool.Exec(ctx, `update tiendas set whatsapp_tendero = '573000000778' where id = 'tst_tienda_tendero'`); err != nil {
		t.Fatalf("setting the storekeeper's number: %v", err)
	}

	tendero, existe, err := database.TenderoPorWhatsapp(ctx, "573000000778", ahora)
	if err != nil || !existe {
		t.Fatalf("TenderoPorWhatsapp() = %+v, %v, %v, want the store of that storekeeper", tendero, existe, err)
	}
	if tendero.TiendaID != "tst_tienda_tendero" || tendero.NombreTienda == "" || tendero.Nombre == "" {
		t.Errorf("TenderoPorWhatsapp() = %+v, want the store, its name and the storekeeper's", tendero)
	}
	if tendero.EfectivoLibre != 400_000 {
		t.Errorf("EfectivoLibre = %d, want the cash the store published", tendero.EfectivoLibre)
	}

	if _, existe, err := database.TenderoPorWhatsapp(ctx, "573000000000", ahora); err != nil || existe {
		t.Errorf("TenderoPorWhatsapp() of an unknown number = %v, %v, want it to not exist and not to be an error", existe, err)
	}
}
