package store

import (
	"context"
	"testing"
)

// limpiarDatosDePrueba removes every row a store test can create, in the order the
// foreign keys demand: children first.
//
// It lives in one place because five tests need it and cleaning in the wrong order
// fails the whole test with a foreign key error that looks like a defect of the code
// under test. That mistake was made twice before this helper existed.
//
// The acceptances of the policies are NOT here on purpose: the schema refuses UPDATE
// and DELETE on that table, so a test that records one leaves it behind, which is the
// contract's rule working even against a test.
func limpiarDatosDePrueba(t *testing.T, database *Store, ctx context.Context) {
	t.Helper()

	for _, statement := range []string{
		"delete from reservas where id like 'tst_%' or tienda_id like 'tst_%' or persona_id like 'tst_%'",
		"delete from sesiones_verificacion where persona_id like 'tst_%'",
		"delete from cupos where persona_id like 'tst_%'",
		"delete from alertas where persona_id like 'tst_%' or tienda_id like 'tst_%'",
		"delete from efectivo_disponible where tienda_id like 'tst_%'",
		"delete from tiendas where id like 'tst_%' or whatsapp_tendero like 'tst_%'",
		"delete from personas where id like 'tst_%'",
	} {
		if _, err := database.pool.Exec(ctx, statement); err != nil {
			t.Errorf("cleaning the test data (%s): %v", statement, err)
		}
	}
}
