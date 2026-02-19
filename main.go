package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/whisper-darkly/sticky-backend/config"
	"github.com/whisper-darkly/sticky-backend/manager"
	"github.com/whisper-darkly/sticky-backend/overseer"
	"github.com/whisper-darkly/sticky-backend/router"
	"github.com/whisper-darkly/sticky-backend/store/sqlite"
)

var version = "dev"

func main() {
	port := env("BACKEND_PORT", "8080")
	overseerURL := env("OVERSEER_URL", "ws://localhost:8081/ws")
	confDir := env("CONF_DIR", "/data/conf")

	fmt.Printf("sticky-backend %s\n", version)

	if err := os.MkdirAll(confDir, 0o755); err != nil {
		log.Fatalf("conf dir: %v", err)
	}

	cfg, err := config.Load(confDir)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := sqlite.Open(filepath.Join(confDir, "sticky.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	mgr := manager.New(cfg, db)

	oc := overseer.NewClient(overseerURL, overseer.Handler{
		OnOutput: mgr.OnOutput,
		OnExited: mgr.OnExited,
	})
	mgr.SetOverseerClient(oc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go oc.Run(ctx)

	if err := mgr.Start(ctx); err != nil {
		log.Fatalf("manager: %v", err)
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router.New(mgr, cfg),
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
