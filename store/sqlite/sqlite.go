// Package sqlite provides the SQLite-backed Store implementation.
// It uses modernc.org/sqlite (pure Go, no CGO) so the binary is fully static
// and works in scratch/alpine Docker images without a C compiler.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/whisper-darkly/sticky-backend/store"
)

// DB implements store.Store using SQLite via database/sql.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies migrations.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// SQLite serialises writes; one connection avoids SQLITE_BUSY on writes.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	s := &DB{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// migrate applies the schema.  New versions should only ADD statements here
// so that existing databases keep working without a migration tool.
func (s *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS subscriptions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			driver        TEXT    NOT NULL,
			source        TEXT    NOT NULL,
			state         TEXT    NOT NULL DEFAULT 'active',
			error_message TEXT    NOT NULL DEFAULT '',
			created_at    TEXT    NOT NULL,
			updated_at    TEXT    NOT NULL,
			UNIQUE (driver, source)
		)`,

		`CREATE TABLE IF NOT EXISTS worker_events (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			subscription_id INTEGER NOT NULL REFERENCES subscriptions(id),
			pid             INTEGER NOT NULL,
			event_type      TEXT    NOT NULL,
			exit_code       INTEGER,          -- NULL for started / stopped
			ts              TEXT    NOT NULL
		)`,

		// Queries filter primarily on subscription_id + ts (threshold check) and
		// subscription_id + pid + event_type (intentional-exit correlation).
		`CREATE INDEX IF NOT EXISTS idx_we_sub_ts
			ON worker_events(subscription_id, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_we_sub_pid_type
			ON worker_events(subscription_id, pid, event_type)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// ---- subscriptions ----

func (s *DB) CreateSubscription(ctx context.Context, driver, source string) (*store.Subscription, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO subscriptions (driver, source, state, error_message, created_at, updated_at)
		VALUES (?, ?, 'active', '', ?, ?)
		ON CONFLICT(driver, source) DO UPDATE SET
			state         = 'active',
			error_message = '',
			updated_at    = excluded.updated_at
	`, driver, source, now, now)
	if err != nil {
		return nil, err
	}
	return s.GetSubscriptionByKey(ctx, driver, source)
}

func (s *DB) GetSubscription(ctx context.Context, id int64) (*store.Subscription, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, driver, source, state, error_message, created_at, updated_at
		   FROM subscriptions WHERE id = ?`, id)
	return scanSub(row.Scan)
}

func (s *DB) GetSubscriptionByKey(ctx context.Context, driver, source string) (*store.Subscription, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, driver, source, state, error_message, created_at, updated_at
		   FROM subscriptions WHERE driver = ? AND source = ?`, driver, source)
	return scanSub(row.Scan)
}

func (s *DB) ListVisible(ctx context.Context) ([]*store.Subscription, error) {
	return s.querySubs(ctx, `
		SELECT id, driver, source, state, error_message, created_at, updated_at
		  FROM subscriptions
		 WHERE state != 'inactive'
		 ORDER BY driver, source
	`)
}

func (s *DB) ListActive(ctx context.Context) ([]*store.Subscription, error) {
	return s.querySubs(ctx, `
		SELECT id, driver, source, state, error_message, created_at, updated_at
		  FROM subscriptions
		 WHERE state = 'active'
		 ORDER BY driver, source
	`)
}

func (s *DB) SetState(ctx context.Context, id int64, state store.State, errorMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE subscriptions SET state = ?, error_message = ?, updated_at = ? WHERE id = ?`,
		string(state), errorMsg, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// ---- worker events ----

func (s *DB) RecordWorkerEvent(
	ctx context.Context,
	target store.Target,
	pid int,
	eventType store.EventType,
	exitCode *int,
) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO worker_events (subscription_id, pid, event_type, exit_code, ts)
		VALUES (?, ?, ?, ?, ?)
	`, target.Sub.ID, pid, string(eventType), exitCode, time.Now().UTC().Format(time.RFC3339))
	return err
}

// CycleResetAt returns the timestamp of the first EventStarted that arrived
// after the most recent EventStopped.
//
// SQL approach: find the most recent 'stopped' timestamp, then find the
// earliest 'started' after that point.  If no 'stopped' exists, return the
// zero time so the caller falls back to the error window alone.
func (s *DB) CycleResetAt(ctx context.Context, subscriptionID int64) (time.Time, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(
			(SELECT MIN(ts)
			   FROM worker_events
			  WHERE subscription_id = ?
			    AND event_type = 'started'
			    AND ts > COALESCE(
			          (SELECT MAX(ts)
			             FROM worker_events
			            WHERE subscription_id = ?
			              AND event_type = 'stopped'),
			          '1970-01-01T00:00:00Z'
			        )
			),
			''
		)
	`, subscriptionID, subscriptionID)

	var raw string
	if err := row.Scan(&raw); err != nil {
		return time.Time{}, err
	}
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// ErrorExitsSince counts non-intentional error exits after `since`.
//
// An exit is "intentional" (and excluded) if a 'stopped' event exists for
// the same subscription_id and PID — meaning the backend sent SIGTERM before
// the process terminated.
func (s *DB) ErrorExitsSince(ctx context.Context, subscriptionID int64, since time.Time) (int, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM worker_events we
		 WHERE we.subscription_id = ?
		   AND we.event_type      = 'exited'
		   AND we.exit_code       IS NOT NULL
		   AND we.exit_code       != 0
		   AND we.ts              > ?
		   AND NOT EXISTS (
		         SELECT 1
		           FROM worker_events ws
		          WHERE ws.subscription_id = we.subscription_id
		            AND ws.pid             = we.pid
		            AND ws.event_type      = 'stopped'
		       )
	`, subscriptionID, since.UTC().Format(time.RFC3339))

	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *DB) RecentWorkerEvents(ctx context.Context, subscriptionID int64, limit int) ([]store.WorkerEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, subscription_id, pid, event_type, exit_code, ts
		  FROM worker_events
		 WHERE subscription_id = ?
		 ORDER BY ts DESC, id DESC
		 LIMIT ?
	`, subscriptionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []store.WorkerEvent
	for rows.Next() {
		var ev store.WorkerEvent
		var ts string
		if err := rows.Scan(&ev.ID, &ev.SubscriptionID, &ev.PID, &ev.EventType, &ev.ExitCode, &ts); err != nil {
			return nil, err
		}
		ev.TS, _ = time.Parse(time.RFC3339, ts)
		events = append(events, ev)
	}
	return events, rows.Err()
}

func (s *DB) Close() error { return s.db.Close() }

// ---- internal helpers ----

// scanFn is the common signature of (*sql.Row).Scan and (*sql.Rows).Scan.
type scanFn func(dest ...any) error

func scanSub(scan scanFn) (*store.Subscription, error) {
	var sub store.Subscription
	var createdAt, updatedAt string
	err := scan(&sub.ID, &sub.Driver, &sub.Source, &sub.State, &sub.ErrorMessage, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sub.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	sub.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &sub, nil
}

func (s *DB) querySubs(ctx context.Context, q string, args ...any) ([]*store.Subscription, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*store.Subscription
	for rows.Next() {
		sub, err := scanSub(rows.Scan)
		if err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}
