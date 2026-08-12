package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	s, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.SetMaxOpenConns(1)
	d := &DB{sql: s}
	if err := d.migrate(); err != nil {
		s.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) Close() error { return d.sql.Close() }

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			pass_hash TEXT NOT NULL,
			idx INTEGER UNIQUE NOT NULL,
			ip TEXT NOT NULL,
			ssh_port INTEGER UNIQUE NOT NULL,
			start_port INTEGER NOT NULL,
			cpu INTEGER NOT NULL DEFAULT 10,
			mem_mb INTEGER NOT NULL DEFAULT 1024,
			disk_gb INTEGER NOT NULL DEFAULT 10,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS domains(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			domain TEXT UNIQUE NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions(
			token TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS traffic(
			user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			period TEXT NOT NULL,
			upload_bytes INTEGER NOT NULL DEFAULT 0,
			download_bytes INTEGER NOT NULL DEFAULT 0,
			last_rx INTEGER NOT NULL DEFAULT 0,
			last_tx INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_domains_user ON domains(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`,
	}
	for _, s := range stmts {
		if _, err := d.sql.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return d.migrateCPU()
}

// migrateCPU converts the cpu column from whole cores to tenths of a core
// (cpu=1 now means 0.1 cores; cpu=10 means 1 core). Old databases stored whole
// cores as integers, so multiply by 10. Guarded by PRAGMA user_version so it
// runs exactly once — the fresh-install default (10) must never be doubled.
func (d *DB) migrateCPU() error {
	var v int
	if err := d.sql.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("migrate: read user_version: %w", err)
	}
	if v >= 1 {
		return nil
	}
	if _, err := d.sql.Exec(`UPDATE users SET cpu = cpu * 10`); err != nil {
		return fmt.Errorf("migrate: scale cpu to tenths: %w", err)
	}
	if _, err := d.sql.Exec(`PRAGMA user_version = 1`); err != nil {
		return fmt.Errorf("migrate: set user_version: %w", err)
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
