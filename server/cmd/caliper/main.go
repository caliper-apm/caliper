package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/collector"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/storage/clickhouse"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dataDir := flag.String("data-dir", ".data", "Directory for WAL and metadata persistence (empty for in-memory)")
	storageDriver := flag.String("storage-driver", "embedded", "Analytics storage engine: embedded | clickhouse")
	chEndpoint := flag.String("clickhouse-endpoint", "http://127.0.0.1:8123", "ClickHouse HTTP endpoint (when --storage-driver=clickhouse)")
	chDB := flag.String("clickhouse-db", "caliper", "ClickHouse database name")
	chUser := flag.String("clickhouse-user", "", "Optional ClickHouse HTTP username")
	chPass := flag.String("clickhouse-password", "", "Optional ClickHouse HTTP password")
	chMigrate := flag.Bool("clickhouse-migrate", true, "Run idempotent ClickHouse DDL migration on startup")
	apiKey := flag.String("api-key", "", "Optional single X-Caliper-Key for write endpoints (v0.1 compat mode)")
	authConfig := flag.String("auth-config", "", "Optional JSON RBAC config file (--auth-config overrides --api-key)")
	consoleDist := flag.String("console-dist", "", "Optional path to built console/dist static assets served at /")
	alertInterval := flag.Duration("alert-interval", 0, "Optional background alert evaluation interval (e.g., 30s; 0 disables background loop)")
	flag.Parse()

	metaStore, err := storage.OpenEmbedded(*dataDir)
	if err != nil {
		log.Fatalf("open metadata/embedded store: %v", err)
	}
	defer func() {
		if err := metaStore.Close(); err != nil {
			log.Printf("close metadata store error: %v", err)
		}
	}()

	var analyticsStore storage.AnalyticsStore = metaStore
	switch *storageDriver {
	case "embedded":
		// Already wired to metaStore.
	case "clickhouse":
		chStore, err := clickhouse.New(clickhouse.Config{
			Endpoint: *chEndpoint,
			Database: *chDB,
			Username: *chUser,
			Password: *chPass,
			Meta:     metaStore,
		})
		if err != nil {
			log.Fatalf("init clickhouse store: %v", err)
		}
		if *chMigrate {
			migCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := chStore.Migrate(migCtx); err != nil {
				cancel()
				log.Fatalf("clickhouse migrate: %v", err)
			}
			cancel()
		}
		defer func() { _ = chStore.Close() }()
		analyticsStore = chStore
	default:
		log.Fatalf("unsupported --storage-driver %q (expected embedded or clickhouse)", *storageDriver)
	}

	var authorizer *auth.Authorizer
	if *authConfig != "" {
		authorizer, err = auth.LoadRBACFile(*authConfig)
		if err != nil {
			log.Fatalf("load auth config: %v", err)
		}
	} else {
		authorizer = auth.NewSingleAPIKey(*apiKey)
	}

	handler := collector.NewHandler(analyticsStore, metaStore, collector.Config{
		APIKey:         *apiKey,
		Authorizer:     authorizer,
		ConsoleDistDir: *consoleDist,
	})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *alertInterval > 0 {
		go func() {
			ticker := time.NewTicker(*alertInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-ticker.C:
					outcomes, err := handler.AlertEngine().EvaluateAll(ctx, now.UTC())
					if err != nil {
						log.Printf("alert evaluator error: %v", err)
						continue
					}
					for _, o := range outcomes {
						if o.State == "firing" || o.State == "resolved" {
							log.Printf("alert rule=%s state=%s samples=%d", o.RuleID, o.State, o.SampleSize)
						}
					}
				}
			}
		}()
	}

	go func() {
		log.Printf("Caliper APM server listening on http://%s (storage=%s data-dir=%q)", *addr, *storageDriver, *dataDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down Caliper APM server...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
}
