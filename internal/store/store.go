// Package store wraps the SQLite database. One connection, all queries
// serialize: adequate for a single-operator service and removes write-race
// handling entirely. WAL, busy_timeout, FK enforcement are set via DSN.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/davidtorcivia/westward/internal/config"
	"github.com/davidtorcivia/westward/migrations"

	_ "modernc.org/sqlite"
)

const settingsKey = "runtime"

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single connection: no write races, at the cost of serializing reads.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(migrations.FS); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database connection is alive.
func (s *Store) Ping() error { return s.db.Ping() }

func (s *Store) migrate(fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY, applied_utc INTEGER NOT NULL)`); err != nil {
		return err
	}
	for _, name := range names {
		var one int
		if err := s.db.QueryRow(`SELECT 1 FROM schema_migrations WHERE name=?`, name).Scan(&one); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(name, applied_utc) VALUES(?,?)`,
			name, time.Now().UnixMilli()); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// GetSettings loads the runtime settings and their revision (0 = defaults,
// not yet persisted). Falls back to defaults when the row is absent.
func (s *Store) GetSettings() (config.Settings, int64, error) {
	var (
		raw string
		rev int64
	)
	s2 := config.Defaults()
	err := s.db.QueryRow(`SELECT value_json, revision FROM settings WHERE key=?`, settingsKey).Scan(&raw, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return s2, 0, nil
	}
	if err != nil {
		return s2, 0, err
	}
	if err := json.Unmarshal([]byte(raw), &s2); err != nil {
		return config.Defaults(), 0, fmt.Errorf("settings row corrupt: %w", err)
	}
	return s2, rev, nil
}

// SaveSettings validates and persists settings, incrementing the global
// config revision. First write inserts with revision 1.
func (s *Store) SaveSettings(next config.Settings) (int64, error) {
	if err := next.Validate(); err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	raw, err := json.Marshal(next)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	var rev int64
	err = tx.QueryRow(`SELECT revision FROM settings WHERE key=?`, settingsKey).Scan(&rev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		rev = 1
		_, err = tx.Exec(`INSERT INTO settings(key,value_json,updated_utc,revision) VALUES(?,?,?,?)`,
			settingsKey, string(raw), now, rev)
	case err != nil:
		tx.Rollback()
		return 0, err
	default:
		rev++
		_, err = tx.Exec(`UPDATE settings SET value_json=?, updated_utc=?, revision=? WHERE key=?`,
			string(raw), now, rev, settingsKey)
	}
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	return rev, tx.Commit()
}

// SetSettingRaw writes an arbitrary settings key (used for install_date).
func (s *Store) SetSettingRaw(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings(key,value_json,updated_utc,revision) VALUES(?,?,?,1)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_utc=excluded.updated_utc, revision=revision+1`,
		key, string(raw), time.Now().UnixMilli())
	return err
}

// GetSettingRaw reads an arbitrary settings key into out.
func (s *Store) GetSettingRaw(key string, out any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value_json FROM settings WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), out)
}

// Backup runs VACUUM INTO dst (a full, consistent SQLite snapshot).
func (s *Store) Backup(dst string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, dst)
	return err
}
