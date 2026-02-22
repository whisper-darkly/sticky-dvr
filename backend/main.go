package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whisper-darkly/sticky-dvr/backend/config"
	"github.com/whisper-darkly/sticky-dvr/backend/converter"
	"github.com/whisper-darkly/sticky-dvr/backend/manager"
	"github.com/whisper-darkly/sticky-dvr/backend/overseer"
	"github.com/whisper-darkly/sticky-dvr/backend/router"
	"github.com/whisper-darkly/sticky-dvr/backend/store/postgres"
	"github.com/whisper-darkly/sticky-dvr/backend/thumbnailer"
)

var version = "dev"

func main() {
	port := env("BACKEND_PORT", "8080")
	overseerURL := env("OVERSEER_URL", "ws://localhost:8081/ws")

	dbDSN := os.Getenv("DB_DSN")
	if dbDSN == "" {
		log.Fatal("DB_DSN environment variable is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET environment variable is required")
	}

	fmt.Printf("sticky-backend %s\n", version)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open postgres store + run migrations.
	db, err := postgres.Open(ctx, dbDSN)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	// Seed admin user if ADMIN_PASSWORD is set and no users exist yet.
	adminUser := env("ADMIN_USERNAME", "admin")
	adminPass := os.Getenv("ADMIN_PASSWORD")
	if adminPass != "" {
		if err := db.SeedAdminUser(ctx, adminUser, adminPass); err != nil {
			log.Fatalf("seed admin user: %v", err)
		}
		log.Printf("seeded admin user: %s", adminUser)
	} else {
		log.Println("ADMIN_PASSWORD not set; skipping admin user seeding")
	}

	// Load config (seeds defaults into DB if first run).
	cfg, err := config.Load(ctx, db)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	mgr := manager.New(cfg, db)

	oc := overseer.NewClient(overseerURL, overseer.Handler{
		OnStarted:    mgr.OnStarted,
		OnOutput:     mgr.OnOutput,
		OnExited:     mgr.OnExited,
		OnRestarting: mgr.OnRestarting,
		OnErrored:    mgr.OnErrored,
		OnConnected:  mgr.OnConnected,
	})
	mgr.SetOverseerClient(oc)

	go oc.Run(ctx)

	if err := mgr.Start(ctx); err != nil {
		log.Fatalf("manager: %v", err)
	}

	// Converter client (optional — graceful degradation if CONVERTER_URL not set).
	var convClient *converter.Client
	if converterURL := os.Getenv("CONVERTER_URL"); converterURL != "" {
		convClient = converter.NewClient(converterURL)
		log.Printf("converter client: %s", converterURL)
	} else {
		log.Println("CONVERTER_URL not set; /files endpoint will return empty list")
	}

	// Thumbnailer client (optional — graceful degradation if THUMBNAILER_URL not set).
	var thumbClient *thumbnailer.Client
	if thumbnailerURL := os.Getenv("THUMBNAILER_URL"); thumbnailerURL != "" {
		thumbClient = thumbnailer.NewClient(thumbnailerURL)
		log.Printf("thumbnailer client: %s", thumbnailerURL)
	} else {
		log.Println("THUMBNAILER_URL not set; thumbnailer diagnostics unavailable")
	}

	// Periodically delete expired sessions (every hour).
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := db.DeleteExpiredSessions(ctx); err != nil {
				log.Printf("delete expired sessions: %v", err)
			}
		}
	}()

	srv := &http.Server{
		Addr: ":" + port,
		Handler: router.New(router.Deps{
			Store:             db,
			Manager:           mgr,
			Config:            cfg,
			JWTSecret:         []byte(jwtSecret),
			ConverterClient:   convClient,
			ThumbnailerClient: thumbClient,
		}),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	<-sigCh
	log.Println("shutting down…")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
