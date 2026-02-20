// Command initdb is the sticky-dvr database initialisation container.
//
// It must run (and exit 0) before the backend container starts.
//
// What it does:
//
//  1. If PG_ADMIN_USER + PG_ADMIN_PASSWORD are set, connects to PostgreSQL
//     as that superuser and ensures the app database and app role exist:
//       CREATE DATABASE  <app-db>   (idempotent via pg_database check)
//       CREATE ROLE IF NOT EXISTS <app-user> WITH LOGIN
//       ALTER  ROLE      <app-user> WITH PASSWORD '<app-pass>'
//       GRANT  ALL PRIVILEGES ON DATABASE <app-db> TO <app-user>
//       GRANT  ALL ON SCHEMA public TO <app-user>   (run inside app-db)
//
//  2. Regardless of admin credentials, connects using DB_DSN and runs
//     all pending golang-migrate up-migrations from the embedded SQL files.
//
//  3. Exits 0 on success, non-zero on any failure.
//
// Required env vars:
//
//	DB_DSN — app database connection string
//	          e.g. postgres://sticky:changeme@postgres:5432/sticky?sslmode=disable
//
// Optional env vars (both required together for superuser setup):
//
//	PG_ADMIN_USER     — postgres superuser name (e.g. "postgres")
//	PG_ADMIN_PASSWORD — postgres superuser password
package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/whisper-darkly/sticky-dvr/backend/store/postgres"
)

func main() {
	dbDSN := os.Getenv("DB_DSN")
	if dbDSN == "" {
		log.Fatal("DB_DSN is required")
	}

	adminUser := os.Getenv("PG_ADMIN_USER")
	adminPass := os.Getenv("PG_ADMIN_PASSWORD")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if adminUser != "" && adminPass != "" {
		log.Println("initdb: admin credentials present — ensuring app database and role exist")
		if err := ensureDB(ctx, dbDSN, adminUser, adminPass); err != nil {
			log.Fatalf("initdb: db/role setup failed: %v", err)
		}
		log.Println("initdb: database and role OK")
	} else {
		log.Println("initdb: no admin credentials — skipping database/role creation")
	}

	log.Println("initdb: running migrations…")
	if err := postgres.RunMigrations(dbDSN); err != nil {
		log.Fatalf("initdb: migrations failed: %v", err)
	}
	log.Println("initdb: migrations OK — exiting")
}

// ensureDB connects as the postgres superuser and idempotently creates
// the app database and app role, then grants necessary privileges.
func ensureDB(ctx context.Context, appDSN, adminUser, adminPass string) error {
	u, err := url.Parse(appDSN)
	if err != nil {
		return fmt.Errorf("parse DB_DSN: %w", err)
	}

	appDB := u.Path // "/sticky" → trimmed below
	if len(appDB) > 0 && appDB[0] == '/' {
		appDB = appDB[1:]
	}
	appUser := u.User.Username()
	appPass, _ := u.User.Password()

	if appDB == "" {
		return fmt.Errorf("DB_DSN must include a database name")
	}
	if appUser == "" {
		return fmt.Errorf("DB_DSN must include a username")
	}

	// Build admin DSN pointing at the maintenance 'postgres' database.
	adminDSN := fmt.Sprintf("postgres://%s:%s@%s/postgres", adminUser, adminPass, u.Host)
	if u.RawQuery != "" {
		adminDSN += "?" + u.RawQuery
	}

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("admin connect: %w", err)
	}
	defer conn.Close(ctx)

	// Create database if it doesn't already exist.
	// PostgreSQL has no "CREATE DATABASE IF NOT EXISTS" — check pg_database.
	var exists bool
	err = conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, appDB,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check database existence: %w", err)
	}
	if !exists {
		// Database name can't be parameterised in DDL; safe here because it
		// comes from our own DSN env var, not user input.
		_, err = conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, appDB))
		if err != nil {
			return fmt.Errorf("create database %q: %w", appDB, err)
		}
		log.Printf("initdb: created database %q", appDB)
	} else {
		log.Printf("initdb: database %q already exists", appDB)
	}

	// Create role if not exists and set password.
	_, err = conn.Exec(ctx,
		fmt.Sprintf(`CREATE ROLE %q WITH LOGIN NOINHERIT`, appUser))
	if err != nil {
		// "duplicate_object" (42710) means role already exists — that's fine.
		if !isDuplicateObject(err) {
			return fmt.Errorf("create role %q: %w", appUser, err)
		}
		log.Printf("initdb: role %q already exists", appUser)
	} else {
		log.Printf("initdb: created role %q", appUser)
	}

	// Always update password (handles rotation) and grant on database.
	if appPass != "" {
		_, err = conn.Exec(ctx,
			fmt.Sprintf(`ALTER ROLE %q WITH PASSWORD '%s'`, appUser, appPass))
		if err != nil {
			return fmt.Errorf("set password for role %q: %w", appUser, err)
		}
	}

	_, err = conn.Exec(ctx,
		fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE %q TO %q`, appDB, appUser))
	if err != nil {
		return fmt.Errorf("grant on database: %w", err)
	}

	// Connect to the app database to grant schema access (required in PG 15+).
	appAdminDSN := fmt.Sprintf("postgres://%s:%s@%s/%s", adminUser, adminPass, u.Host, appDB)
	if u.RawQuery != "" {
		appAdminDSN += "?" + u.RawQuery
	}
	appConn, err := pgx.Connect(ctx, appAdminDSN)
	if err != nil {
		return fmt.Errorf("admin connect to app db: %w", err)
	}
	defer appConn.Close(ctx)

	_, err = appConn.Exec(ctx,
		fmt.Sprintf(`GRANT ALL ON SCHEMA public TO %q`, appUser))
	if err != nil {
		return fmt.Errorf("grant schema to role: %w", err)
	}

	log.Printf("initdb: privileges granted on %q to %q", appDB, appUser)
	return nil
}

// isDuplicateObject returns true if err is a PostgreSQL "duplicate_object" (42710) error.
func isDuplicateObject(err error) bool {
	if err == nil {
		return false
	}
	// pgconn.PgError carries the SQLState code.
	type pgErr interface{ SQLState() string }
	if pe, ok := err.(pgErr); ok {
		return pe.SQLState() == "42710"
	}
	return false
}
