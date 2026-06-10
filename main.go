package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aedatum/runway/config"
	"github.com/aedatum/runway/internal/api"
	"github.com/aedatum/runway/internal/auth"
	"github.com/aedatum/runway/internal/db"
	"github.com/aedatum/runway/internal/middleware"
	"github.com/aedatum/runway/internal/poller"
	"github.com/aedatum/runway/internal/queue"
	"github.com/aedatum/runway/internal/retention"
	"github.com/aedatum/runway/internal/secrets"
	"github.com/aedatum/runway/internal/watcher"
)

func main() {
	cfg := config.Load()

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	// Seed the admin user if none exists yet.
	if err := auth.Bootstrap(database, cfg.AdminPassword); err != nil {
		log.Fatalf("auth bootstrap: %v", err)
	}

	// Seed default settings (only written if key does not already exist).
	for key, val := range map[string]string{
		"docker_memory":         cfg.DockerMemory,
		"docker_cpus":           cfg.DockerCPUs,
		"secrets_file":          cfg.SecretsFile,
		"act_platform_mappings": cfg.PlatformMappings,
		"act_container_options": cfg.ContainerOpts,
	} {
		if err := db.SeedSetting(database, key, val); err != nil {
			log.Printf("warning: could not seed %s setting: %v", key, err)
		}
	}

	// Master key for encrypting stored secrets (env var, or a keyfile created
	// next to the database on first boot).
	cipher, err := secrets.LoadKey(cfg.SecretsKey, filepath.Dir(cfg.DBPath))
	if err != nil {
		log.Fatalf("secrets key: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Docker event watcher (optional — logs a warning if Docker is unavailable).
	dw, err := watcher.NewDockerWatcher(database)
	if err != nil {
		log.Printf("docker watcher unavailable: %v (runs won't be captured automatically)", err)
	} else {
		go dw.Watch(ctx)
	}

	// Git repo poller (scans REPOS_ROOT for git repos every 30 s).
	gp := poller.NewGitPoller(database, cfg.ReposRoot)
	go gp.Run(ctx)

	// Queue engine (drains queue table, runs act, streams logs to DB).
	qe := queue.NewEngine(database, cfg.ReposRoot, cipher)
	go qe.Run(ctx)

	// Retention cleanup (prunes old runs/logs per retention_days setting).
	rj := retention.NewJob(database)
	go rj.Run(ctx)

	// HTTP server — a single Server handles all routes (/api/*, /repos/*, /).
	srv := api.NewServer(database, cipher)
	handler := middleware.Auth(database, srv)

	httpSrv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE streams must not time out on writes
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Printf("runway listening on :%s  db=%s  repos=%s", cfg.Port, cfg.DBPath, cfg.ReposRoot)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
}
