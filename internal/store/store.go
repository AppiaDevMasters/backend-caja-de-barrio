// Package store is the data layer of Caja de Barrio: the connection to the
// PostgreSQL database and the migrations that create the schema of the contract.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The migrations travel inside the binary, so the service can create its own
// schema and there is no file to lose between the repository and the deployment.
//
//go:embed migrations/*.sql
var migrations embed.FS

// migrationsDir is where the embedded files live.
const migrationsDir = "migrations"

// ledger is the table that records which migrations ran. It is not part of the
// contract's eleven tables: it is what makes a migration applied once stay applied.
const ledger = "esquema_migrado"

// Config is what the store needs to reach the database.
type Config struct {
	// DatabaseURL is the PostgreSQL DSN (DATABASE_URL). It carries a credential,
	// so nothing in this package ever prints it whole.
	DatabaseURL string
}

// Store is the connection to the database.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to the database and checks that it answers before returning, so a
// wrong DSN or a database that is down fails at start-up instead of at the first
// query of the first request.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	dsn := strings.TrimSpace(cfg.DatabaseURL)
	if dsn == "" {
		return nil, errors.New("store: DatabaseURL is empty")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: opening the database at %s: %s", target(dsn), sanitize(err.Error(), dsn))
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: the database at %s does not answer: %w", target(dsn), err)
	}

	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Ping checks that the database still answers.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: the database does not answer: %w", err)
	}

	return nil
}

// Migrate applies every embedded migration the database does not have yet, in
// name order, each one inside its own transaction and recorded in the ledger.
//
// Running it twice is a no-op the second time, which is what makes it safe to call
// on every start-up, and it returns the migrations it applied so that a second run
// can show that it applied none.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	if err := s.createLedger(ctx); err != nil {
		return nil, err
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return nil, err
	}

	files, err := MigrationFiles()
	if err != nil {
		return nil, err
	}

	var ran []string
	for _, name := range files {
		if applied[name] {
			continue
		}
		if err := s.applyOne(ctx, name); err != nil {
			return ran, err
		}
		ran = append(ran, name)
	}

	return ran, nil
}

// MigrationFiles lists the embedded migrations in the order they have to run.
func MigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrations, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("store: reading the embedded migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, errors.New("store: no embedded migration was found")
	}

	// The order is the contract of the directory: every file is numbered, and a
	// migration that runs out of order is worse than one that does not run.
	sort.Strings(names)

	return names, nil
}

func (s *Store) createLedger(ctx context.Context) error {
	statement := fmt.Sprintf(`create table if not exists %s (
		nombre text primary key,
		aplicada_en timestamptz not null default now()
	)`, ledger)
	if _, err := s.pool.Exec(ctx, statement); err != nil {
		return fmt.Errorf("store: creating the migration ledger: %w", err)
	}

	return nil
}

func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf("select nombre from %s", ledger))
	if err != nil {
		return nil, fmt.Errorf("store: reading the migration ledger: %w", err)
	}
	defer rows.Close()

	applied := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("store: reading the migration ledger: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the migration ledger: %w", err)
	}

	return applied, nil
}

// applyOne runs one migration and records it in the same transaction, so a
// migration that fails half way leaves nothing behind and the next run retries it.
func (s *Store) applyOne(ctx context.Context, name string) error {
	content, err := migrations.ReadFile(migrationsDir + "/" + name)
	if err != nil {
		return fmt.Errorf("store: reading the migration %s: %w", name, err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: starting the transaction of %s: %w", name, err)
	}
	defer func() {
		// The rollback is a no-op once the transaction is committed, and it is the
		// only thing that releases it when an error returns early.
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, string(content)); err != nil {
		return fmt.Errorf("store: applying the migration %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("insert into %s (nombre) values ($1)", ledger), name); err != nil {
		return fmt.Errorf("store: recording the migration %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: committing the migration %s: %w", name, err)
	}

	return nil
}

// target is the DSN as it may be printed: host, port and database, never the
// credential it carries.
func target(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" {
		return "the configured database"
	}

	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" {
		return parsed.Host
	}

	return parsed.Host + "/" + name
}

// sanitize removes the password of the DSN from a message that may carry it. The
// driver echoes what it could not parse, and that echo is a credential on its way
// to a log, which is the worst place for one.
func sanitize(message, dsn string) string {
	password := passwordOf(dsn)
	if password == "" {
		return message
	}

	return strings.ReplaceAll(message, password, "***")
}

// passwordOf reads the password of a DSN in either of the two forms the driver
// accepts: a URL, or the key=value list.
//
// The key=value form allows the value to be quoted, and a password with a space
// only survives that way, so the quoting is read here instead of splitting the
// line on spaces and cutting the password in half.
func passwordOf(dsn string) string {
	if parsed, err := url.Parse(dsn); err == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok {
			return password
		}
		return ""
	}

	index := strings.Index(dsn, "password=")
	// A key that merely ends in password is not the password.
	if index < 0 || (index > 0 && !isSpace(dsn[index-1])) {
		return ""
	}

	rest := dsn[index+len("password="):]
	if strings.HasPrefix(rest, "'") || strings.HasPrefix(rest, `"`) {
		quote := rest[:1]
		if end := strings.Index(rest[1:], quote); end >= 0 {
			return rest[1 : 1+end]
		}
	}
	if end := strings.IndexAny(rest, " \t\n"); end >= 0 {
		return strings.Trim(rest[:end], "'\"")
	}

	return strings.Trim(rest, "'\"")
}

func isSpace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n'
}
