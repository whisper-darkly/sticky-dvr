// Package postgres provides the PostgreSQL-backed Store implementation.
// It uses pgx/v5 (pure Go, no CGO) and runs embedded migrations at startup.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whisper-darkly/sticky-dvr/backend/auth"
	"github.com/whisper-darkly/sticky-dvr/backend/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB implements store.Store using PostgreSQL via pgx/v5.
type DB struct {
	pool *pgxpool.Pool
}

// Open creates a connection pool, runs migrations, and returns a ready DB.
func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}

	if err := runMigrations(dsn); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrations: %w", err)
	}

	return &DB{pool: pool}, nil
}

// RunMigrations applies all pending up-migrations against dsn.
// Safe to call multiple times — ErrNoChange is treated as success.
// Called by initdb (as exported) and by Open (internally).
func RunMigrations(dsn string) error { return runMigrations(dsn) }

func runMigrations(dsn string) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("iofs source: %w", err)
	}
	migrateURL := toMigrateURL(dsn)
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL)
	if err != nil {
		return fmt.Errorf("migrate.New: %w", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

// toMigrateURL converts a postgres:// or postgresql:// DSN to the pgx5:// scheme
// expected by golang-migrate's pgx/v5 driver.
func toMigrateURL(dsn string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(dsn, prefix) {
			return "pgx5://" + dsn[len(prefix):]
		}
	}
	return "pgx5://" + dsn
}

func (d *DB) Close() error {
	d.pool.Close()
	return nil
}

// SeedAdminUser creates an admin user with the given credentials only when the
// users table is empty (i.e. fresh deployment). It is a no-op if any user
// already exists.
func (d *DB) SeedAdminUser(ctx context.Context, username, password string) error {
	var count int
	if err := d.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count > 0 {
		return nil // already seeded
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	_, err = d.CreateUser(ctx, username, hash, "admin")
	return err
}

// ---- users ----

func (d *DB) CreateUser(ctx context.Context, username, passwordHash, role string) (*store.User, error) {
	var u store.User
	err := d.pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, $2, $3)
		RETURNING id, username, password_hash, role, created_at, updated_at
	`, username, passwordHash, role).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) GetUser(ctx context.Context, id int64) (*store.User, error) {
	var u store.User
	err := d.pool.QueryRow(ctx,
		`SELECT id, username, password_hash, role, created_at, updated_at FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

func (d *DB) GetUserByUsername(ctx context.Context, username string) (*store.User, error) {
	var u store.User
	err := d.pool.QueryRow(ctx,
		`SELECT id, username, password_hash, role, created_at, updated_at FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

func (d *DB) ListUsers(ctx context.Context) ([]*store.User, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT id, username, password_hash, role, created_at, updated_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*store.User
	for rows.Next() {
		var u store.User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, &u)
	}
	return users, rows.Err()
}

func (d *DB) UpdateUser(ctx context.Context, id int64, fields store.UserUpdate) (*store.User, error) {
	var u store.User
	err := d.pool.QueryRow(ctx, `
		UPDATE users SET
			username      = COALESCE($2, username),
			password_hash = COALESCE($3, password_hash),
			role          = COALESCE($4, role),
			updated_at    = now()
		WHERE id = $1
		RETURNING id, username, password_hash, role, created_at, updated_at
	`, id, fields.Username, fields.PasswordHash, fields.Role).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

// ---- sessions ----

func (d *DB) CreateSession(ctx context.Context, userID int64, refreshToken string, expiresAt time.Time) (*store.Session, error) {
	var s store.Session
	err := d.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, refresh_token, expires_at)
		VALUES ($1, $2, $3)
		RETURNING id, user_id, refresh_token, expires_at, created_at
	`, userID, refreshToken, expiresAt).
		Scan(&s.ID, &s.UserID, &s.RefreshToken, &s.ExpiresAt, &s.CreatedAt)
	return &s, err
}

func (d *DB) GetSessionByRefreshToken(ctx context.Context, refreshToken string) (*store.Session, error) {
	var s store.Session
	err := d.pool.QueryRow(ctx,
		`SELECT id, user_id, refresh_token, expires_at, created_at FROM sessions WHERE refresh_token = $1`,
		refreshToken,
	).Scan(&s.ID, &s.UserID, &s.RefreshToken, &s.ExpiresAt, &s.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

func (d *DB) DeleteSession(ctx context.Context, id uuid.UUID) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

func (d *DB) DeleteExpiredSessions(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
	return err
}

// ---- sources ----

func (d *DB) GetOrCreateSource(ctx context.Context, driver, username string) (*store.Source, error) {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO sources (driver, username)
		VALUES ($1, $2)
		ON CONFLICT (driver, username) DO NOTHING
	`, driver, username)
	if err != nil {
		return nil, err
	}
	return d.GetSourceByKey(ctx, driver, username)
}

func (d *DB) GetSourceByKey(ctx context.Context, driver, username string) (*store.Source, error) {
	var s store.Source
	var taskID *string
	err := d.pool.QueryRow(ctx,
		`SELECT id, driver, username, overseer_task_id, created_at FROM sources WHERE driver = $1 AND username = $2`,
		driver, username,
	).Scan(&s.ID, &s.Driver, &s.Username, &taskID, &s.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if taskID != nil {
		s.OverseerTaskID = *taskID
	}
	return &s, nil
}

func (d *DB) ListSources(ctx context.Context) ([]*store.Source, error) {
	rows, err := d.pool.Query(ctx,
		`SELECT id, driver, username, overseer_task_id, created_at FROM sources ORDER BY driver, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []*store.Source
	for rows.Next() {
		var s store.Source
		var taskID *string
		if err := rows.Scan(&s.ID, &s.Driver, &s.Username, &taskID, &s.CreatedAt); err != nil {
			return nil, err
		}
		if taskID != nil {
			s.OverseerTaskID = *taskID
		}
		sources = append(sources, &s)
	}
	return sources, rows.Err()
}

func (d *DB) SetSourceTaskID(ctx context.Context, sourceID int64, taskID string) error {
	_, err := d.pool.Exec(ctx,
		`UPDATE sources SET overseer_task_id = $2 WHERE id = $1`, sourceID, taskID)
	return err
}

// ---- subscriptions ----

func (d *DB) CreateSubscription(ctx context.Context, userID, sourceID int64) (*store.Subscription, error) {
	var sub store.Subscription
	err := d.pool.QueryRow(ctx, `
		INSERT INTO subscriptions (user_id, source_id, posture)
		VALUES ($1, $2, 'active')
		ON CONFLICT (user_id, source_id) DO UPDATE
			SET posture = 'active', updated_at = now()
		RETURNING id, user_id, source_id, posture, created_at, updated_at
	`, userID, sourceID).
		Scan(&sub.ID, &sub.UserID, &sub.SourceID, &sub.Posture, &sub.CreatedAt, &sub.UpdatedAt)
	return &sub, err
}

func (d *DB) GetSubscription(ctx context.Context, userID, sourceID int64) (*store.Subscription, error) {
	var sub store.Subscription
	err := d.pool.QueryRow(ctx, `
		SELECT id, user_id, source_id, posture, created_at, updated_at
		FROM subscriptions WHERE user_id = $1 AND source_id = $2
	`, userID, sourceID).
		Scan(&sub.ID, &sub.UserID, &sub.SourceID, &sub.Posture, &sub.CreatedAt, &sub.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

func (d *DB) ListSubscriptionsByUser(ctx context.Context, userID int64) ([]*store.Subscription, error) {
	return d.querySubs(ctx, `
		SELECT id, user_id, source_id, posture, created_at, updated_at
		FROM subscriptions WHERE user_id = $1 ORDER BY id
	`, userID)
}

func (d *DB) ListActiveSubscriptions(ctx context.Context) ([]*store.Subscription, error) {
	return d.querySubs(ctx, `
		SELECT id, user_id, source_id, posture, created_at, updated_at
		FROM subscriptions WHERE posture = 'active' ORDER BY source_id, user_id
	`)
}

func (d *DB) ListAllSubscriptions(ctx context.Context) ([]*store.Subscription, error) {
	return d.querySubs(ctx, `
		SELECT id, user_id, source_id, posture, created_at, updated_at
		FROM subscriptions ORDER BY id
	`)
}

func (d *DB) SetPosture(ctx context.Context, id int64, posture store.Posture) error {
	_, err := d.pool.Exec(ctx,
		`UPDATE subscriptions SET posture = $2, updated_at = now() WHERE id = $1`, id, string(posture))
	return err
}

func (d *DB) GetSourceActiveSubscriberCount(ctx context.Context, sourceID int64) (int, error) {
	var count int
	err := d.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM subscriptions WHERE source_id = $1 AND posture = 'active'`, sourceID,
	).Scan(&count)
	return count, err
}

func (d *DB) querySubs(ctx context.Context, q string, args ...any) ([]*store.Subscription, error) {
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []*store.Subscription
	for rows.Next() {
		var sub store.Subscription
		if err := rows.Scan(&sub.ID, &sub.UserID, &sub.SourceID, &sub.Posture, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, &sub)
	}
	return subs, rows.Err()
}

// ---- worker events ----

func (d *DB) RecordWorkerEvent(ctx context.Context, sourceID int64, pid int, eventType store.EventType, exitCode *int) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO worker_events (source_id, pid, event_type, exit_code)
		VALUES ($1, $2, $3, $4)
	`, sourceID, pid, string(eventType), exitCode)
	return err
}

func (d *DB) RecentWorkerEvents(ctx context.Context, sourceID int64, limit int) ([]store.WorkerEvent, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, source_id, pid, event_type, exit_code, ts
		FROM worker_events
		WHERE source_id = $1
		ORDER BY ts DESC, id DESC
		LIMIT $2
	`, sourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []store.WorkerEvent
	for rows.Next() {
		var ev store.WorkerEvent
		var et string
		if err := rows.Scan(&ev.ID, &ev.SourceID, &ev.PID, &et, &ev.ExitCode, &ev.TS); err != nil {
			return nil, err
		}
		ev.EventType = store.EventType(et)
		events = append(events, ev)
	}
	return events, rows.Err()
}

// ---- config ----

func (d *DB) GetConfig(ctx context.Context) (map[string]any, error) {
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT data FROM config WHERE id = 1`).Scan(&raw)
	if err == pgx.ErrNoRows {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (d *DB) SetConfig(ctx context.Context, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `
		INSERT INTO config (id, data) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET data = $1
	`, raw)
	return err
}
