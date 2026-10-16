package config

import "testing"

// The data layer is configured by DATABASE_URL, and it is not required: the
// backend serves the endpoints Rafiki calls before there is a database, and the
// component that needs the database is the one that reports it missing.
func TestLoadReadsTheDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "  postgres://caja:secreto@postgres:5432/caja_de_barrio  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}

	if want := "postgres://caja:secreto@postgres:5432/caja_de_barrio"; cfg.Database.URL != want {
		t.Errorf("Database.URL = %q, want %q", cfg.Database.URL, want)
	}
}

func TestLoadWithoutADatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned an error: %v", err)
	}

	if cfg.Database.URL != "" {
		t.Errorf("Database.URL = %q, want it empty when DATABASE_URL is not set", cfg.Database.URL)
	}
}
