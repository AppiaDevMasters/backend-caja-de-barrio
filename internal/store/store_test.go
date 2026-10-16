package store

import (
	"context"
	"strings"
	"testing"
)

// The credential must never reach a log or an error message. This package is the
// one that holds the DSN, so it is the one that has to keep the password inside.
func TestTargetNeverPrintsTheCredential(t *testing.T) {
	dsn := "postgres://caja:secreto-de-la-base@postgres:5432/caja_de_barrio"

	got := target(dsn)
	if strings.Contains(got, "secreto-de-la-base") {
		t.Errorf("target() = %q, and it carries the password of the database", got)
	}
	if got != "postgres:5432/caja_de_barrio" {
		t.Errorf("target() = %q, want the host, the port and the database", got)
	}
}

// The driver echoes what it could not parse, and that echo is a credential on its
// way to a log.
func TestSanitizeRemovesThePasswordFromADriverMessage(t *testing.T) {
	dsn := "postgres://caja:secreto-de-la-base@postgres:5432/caja_de_barrio"
	message := `cannot parse "postgres://caja:secreto-de-la-base@postgres:5432/caja_de_barrio": invalid port`

	got := sanitize(message, dsn)
	if strings.Contains(got, "secreto-de-la-base") {
		t.Errorf("sanitize() = %q, and it still carries the password", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("sanitize() = %q, want the password replaced by the placeholder", got)
	}
}

func TestSanitizeLeavesAMessageWithoutThePasswordAlone(t *testing.T) {
	message := "dial tcp 10.0.0.1:5432: connection refused"

	if got := sanitize(message, "postgres://caja:otra@postgres:5432/db"); got != message {
		t.Errorf("sanitize() = %q, want the message untouched", got)
	}
}

// The driver accepts two forms of DSN, and both carry the password the same way.
func TestPasswordOfReadsBothDSNForms(t *testing.T) {
	tests := map[string]string{
		"postgres://caja:secreto@postgres:5432/db":           "secreto",
		"host=postgres port=5432 password=secreto dbname=db": "secreto",
		`host=postgres password='con espacios' dbname=db`:    "con espacios",
		"postgres://caja@postgres:5432/db":                   "",
		"no es un dsn con contrasena":                        "",
	}

	for dsn, want := range tests {
		if got := passwordOf(dsn); got != want {
			t.Errorf("passwordOf(%q) = %q, want %q", dsn, got, want)
		}
	}
}

// The migrations run in the order their names say: one that runs out of order is
// worse than one that does not run at all.
func TestMigrationFilesComeInNameOrder(t *testing.T) {
	files, err := MigrationFiles()
	if err != nil {
		t.Fatalf("MigrationFiles() returned an error: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("MigrationFiles() returned nothing, and the schema lives in those files")
	}

	for _, name := range files {
		if !strings.HasSuffix(name, ".sql") {
			t.Errorf("MigrationFiles() returned %q, and every migration ends in .sql", name)
		}
	}
	for i := 1; i < len(files); i++ {
		if files[i-1] >= files[i] {
			t.Errorf("MigrationFiles() = %v, want name order", files)
		}
	}
	if !strings.HasPrefix(files[0], "0001") {
		t.Errorf("the first migration is %q, want the schema of the contract", files[0])
	}
}

// A store without a database to reach fails at start-up, naming what is missing,
// instead of at the first query of the first request.
func TestOpenRejectsAnEmptyDatabaseURL(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		_, err := Open(context.Background(), Config{DatabaseURL: dsn})
		if err == nil {
			t.Fatalf("Open(%q) opened a database, want an error", dsn)
		}
		if !strings.Contains(err.Error(), "DatabaseURL") {
			t.Errorf("error %q does not name DatabaseURL", err)
		}
	}
}
