package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/collector"
	"github.com/caliper-apm/caliper/server/internal/otlp"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/storage/clickhouse"
	"github.com/caliper-apm/caliper/server/internal/storage/objectstore"
	"github.com/caliper-apm/caliper/server/internal/storage/postgres"
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
	metaDriver := flag.String("meta-driver", "embedded", "Metadata store engine: embedded | postgres (use postgres + clickhouse + s3 for stateless multi-replica HA)")
	pgDSN := flag.String("postgres-dsn", os.Getenv("CALIPER_POSTGRES_DSN"), "PostgreSQL DSN (when --meta-driver=postgres)")
	pgMigrate := flag.Bool("postgres-migrate", true, "Run idempotent PostgreSQL schema migration on startup")
	pgMaxConns := flag.Int("postgres-max-conns", 25, "Maximum open PostgreSQL connections")
	objectStoreDriver := flag.String("object-store", "local", "Blob store for symbols & remote logs: local | s3")
	s3Endpoint := flag.String("s3-endpoint", os.Getenv("CALIPER_S3_ENDPOINT"), "S3/MinIO endpoint URL (when --object-store=s3)")
	s3Bucket := flag.String("s3-bucket", os.Getenv("CALIPER_S3_BUCKET"), "S3/MinIO bucket name")
	s3Region := flag.String("s3-region", os.Getenv("CALIPER_S3_REGION"), "S3 region (default us-east-1)")
	s3AccessKey := flag.String("s3-access-key", os.Getenv("CALIPER_S3_ACCESS_KEY"), "S3 access key")
	s3SecretKey := flag.String("s3-secret-key", os.Getenv("CALIPER_S3_SECRET_KEY"), "S3 secret key")
	apiKey := flag.String("api-key", "", "Optional single X-Caliper-Key for write endpoints (v0.1 compat mode)")
	authConfig := flag.String("auth-config", "", "Optional JSON RBAC config file (--auth-config overrides --api-key)")
	consoleDist := flag.String("console-dist", "", "Optional path to built console/dist static assets served at /")
	alertInterval := flag.Duration("alert-interval", 0, "Optional background alert evaluation interval (e.g., 30s; 0 disables background loop)")
	otlpTracesEndpoint := flag.String("otlp-traces-endpoint", os.Getenv("CALIPER_OTLP_TRACES_ENDPOINT"), "Optional OTLP/HTTP JSON trace push endpoint (e.g. http://tempo:4318/v1/traces)")
	otlpTracesHeaders := flag.String("otlp-traces-headers", os.Getenv("CALIPER_OTLP_TRACES_HEADERS"), "Optional comma-separated headers for OTLP trace push (e.g. Authorization=Bearer xxx)")
	flag.Parse()

	// 1. Initialize ObjectStore (S3/MinIO for stateless HA, or local disk/memory fallback).
	var blobStore objectstore.ObjectStore
	switch *objectStoreDriver {
	case "local", "":
		if *dataDir != "" {
			ls, err := objectstore.NewLocal(filepath.Join(*dataDir, "blobs"))
			if err != nil {
				log.Fatalf("init local object store: %v", err)
			}
			blobStore = ls
		} else {
			blobStore = objectstore.NewMemory()
		}
	case "s3":
		s3Store, err := objectstore.NewS3(objectstore.S3Config{
			Endpoint:  *s3Endpoint,
			Bucket:    *s3Bucket,
			Region:    *s3Region,
			AccessKey: *s3AccessKey,
			SecretKey: *s3SecretKey,
		})
		if err != nil {
			log.Fatalf("init s3 object store: %v", err)
		}
		blobStore = s3Store
	default:
		log.Fatalf("unsupported --object-store %q (expected local or s3)", *objectStoreDriver)
	}

	// 2. Open EmbeddedStore only if needed by either analytics or metadata driver.
	var embeddedStore *storage.EmbeddedStore
	if *storageDriver == "embedded" || *metaDriver == "embedded" || *metaDriver == "" {
		es, err := storage.OpenEmbedded(*dataDir)
		if err != nil {
			log.Fatalf("open embedded store: %v", err)
		}
		if *objectStoreDriver == "s3" {
			es.SetObjectStore(blobStore)
		}
		defer func() {
			if err := es.Close(); err != nil {
				log.Printf("close embedded store error: %v", err)
			}
		}()
		embeddedStore = es
	}

	// 3. Initialize MetaStore (embedded WAL or PostgreSQL HA).
	var metaStore storage.MetaStore
	activeMetaEngine := "embedded"
	switch *metaDriver {
	case "embedded", "":
		metaStore = embeddedStore
	case "postgres":
		pgCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		pgStore, err := postgres.Open(pgCtx, postgres.Config{
			DSN:          *pgDSN,
			MaxOpenConns: *pgMaxConns,
			ObjectStore:  blobStore,
		})
		if err != nil {
			cancel()
			log.Fatalf("init postgres meta store: %v", err)
		}
		if *pgMigrate {
			if err := pgStore.Migrate(pgCtx); err != nil {
				cancel()
				_ = pgStore.Close()
				log.Fatalf("postgres migrate: %v", err)
			}
		}
		cancel()
		defer func() {
			if err := pgStore.Close(); err != nil {
				log.Printf("close postgres store error: %v", err)
			}
		}()
		metaStore = pgStore
		activeMetaEngine = "postgres"
	default:
		log.Fatalf("unsupported --meta-driver %q (expected embedded or postgres)", *metaDriver)
	}

	// 4. Initialize AnalyticsStore (embedded WAL or ClickHouse OLAP).
	var analyticsStore storage.AnalyticsStore
	switch *storageDriver {
	case "embedded":
		analyticsStore = embeddedStore
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
	var err error
	if *authConfig != "" {
		authorizer, err = auth.LoadRBACFile(*authConfig)
		if err != nil {
			log.Fatalf("load auth config: %v", err)
		}
	} else {
		authorizer = auth.NewSingleAPIKey(*apiKey)
	}

	var traceExporter *otlp.TracePushExporter
	if *otlpTracesEndpoint != "" {
		traceExporter = otlp.NewTracePushExporter(otlp.PushConfig{
			Endpoint: *otlpTracesEndpoint,
			Headers:  otlp.ParseHeaderList(*otlpTracesHeaders),
		})
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = traceExporter.Close(flushCtx)
		}()
	}

	handler := collector.NewHandler(analyticsStore, metaStore, collector.Config{
		APIKey:         *apiKey,
		Authorizer:     authorizer,
		ConsoleDistDir: *consoleDist,
		MetaEngine:     activeMetaEngine,
		TraceExporter:  traceExporter,
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
		log.Printf("Caliper APM server listening on http://%s (storage=%s meta=%s object-store=%s data-dir=%q)",
			*addr, *storageDriver, activeMetaEngine, *objectStoreDriver, *dataDir)
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
