// Package store is the tunnelxd control plane: accounts, authtokens, reserved
// subdomains and a log of tunnel sessions, kept in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so cross-compiling stays trivial
)

// Store is a handle on the control-plane database.
type Store struct {
	db *sql.DB
}

// Open opens (and if needed creates) the database at path, applying the schema.
//
// The pragmas matter for a server that writes on every connect and disconnect
// while reading on every auth: WAL lets readers proceed during a write, and the
// busy timeout turns a momentary lock conflict into a short wait instead of an
// immediate "database is locked" failure.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// SQLite takes a single writer. Capping the pool avoids a pile of
	// connections contending for the write lock.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory opens a private in-memory database, for tests.
func OpenMemory(ctx context.Context) (*Store, error) {
	return Open(ctx, "file::memory:?cache=shared&_x=")
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for callers that need a direct query.
func (s *Store) DB() *sql.DB { return s.db }

// schema is applied in order. Statements must be idempotent so startup can run
// them on every boot without a separate migration tool.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS accounts (
		id         TEXT PRIMARY KEY,
		email      TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		disabled   INTEGER NOT NULL DEFAULT 0
	)`,

	`CREATE TABLE IF NOT EXISTS tokens (
		id           TEXT PRIMARY KEY,
		account_id   TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		token_hash   BLOB NOT NULL UNIQUE,
		prefix       TEXT NOT NULL,
		name         TEXT NOT NULL DEFAULT '',
		created_at   INTEGER NOT NULL,
		last_used_at INTEGER,
		revoked      INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tokens_account ON tokens(account_id)`,

	`CREATE TABLE IF NOT EXISTS reserved_subdomains (
		label      TEXT PRIMARY KEY,
		account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_reserved_account ON reserved_subdomains(account_id)`,

	`CREATE TABLE IF NOT EXISTS tunnel_sessions (
		id         TEXT PRIMARY KEY,
		account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
		subdomain  TEXT NOT NULL,
		local_addr TEXT NOT NULL DEFAULT '',
		client_ip  TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL,
		ended_at   INTEGER
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_account ON tunnel_sessions(account_id, started_at)`,
}

func (s *Store) migrate(ctx context.Context) error {
	for i, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema statement %d: %w", i, err)
		}
	}
	return nil
}

// Errors callers distinguish.
var (
	ErrNotFound      = errors.New("not found")
	ErrEmailTaken    = errors.New("an account with that email already exists")
	ErrLabelReserved = errors.New("subdomain is reserved by another account")
)

func now() int64 { return time.Now().Unix() }

func unixPtr(t *int64) *time.Time {
	if t == nil {
		return nil
	}
	v := time.Unix(*t, 0)
	return &v
}
