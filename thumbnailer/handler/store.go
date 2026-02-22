package handler

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS thumbnails (
	path       TEXT PRIMARY KEY,
	status     TEXT NOT NULL DEFAULT 'in_flight',
	updated_at TEXT NOT NULL
);
`

// Store tracks per-file thumbnail generation status to prevent concurrent runs.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the SQLite dedup store at the given path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { s.db.Close() }

// IsInFlight returns true if the file has an active in_flight record.
func (s *Store) IsInFlight(path string) bool {
	var status string
	err := s.db.QueryRow(`SELECT status FROM thumbnails WHERE path = ?`, path).Scan(&status)
	if err != nil {
		return false
	}
	return status == "in_flight"
}

// MarkInFlight upserts the path with status in_flight.
func (s *Store) MarkInFlight(path string) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.db.Exec(
		`INSERT INTO thumbnails (path, status, updated_at) VALUES (?, 'in_flight', ?)
		 ON CONFLICT(path) DO UPDATE SET status='in_flight', updated_at=excluded.updated_at`,
		path, now,
	)
}

// MarkCompleted updates the path status to completed.
func (s *Store) MarkCompleted(path string) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.db.Exec(`UPDATE thumbnails SET status='completed', updated_at=? WHERE path=?`, now, path)
}

// MarkErrored updates the path status to errored.
func (s *Store) MarkErrored(path string) {
	now := time.Now().UTC().Format(time.RFC3339)
	s.db.Exec(`UPDATE thumbnails SET status='errored', updated_at=? WHERE path=?`, now, path)
}
