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

	"github.com/caliper-apm/caliper/server/internal/collector"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	dataDir := flag.String("data-dir", ".data", "Directory for WAL and metadata persistence (empty for in-memory)")
	apiKey := flag.String("api-key", "", "Optional X-Caliper-Key required for write endpoints")
	consoleDist := flag.String("console-dist", "", "Optional path to built console/dist static assets served at /")
	alertInterval := flag.Duration("alert-interval", 0, "Optional background alert evaluation interval (e.g., 30s; 0 disables background loop)")
	flag.Parse()

	store, err := storage.OpenEmbedded(*dataDir)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("close storage error: %v", err)
		}
	}()

	handler := collector.NewHandler(store, store, collector.Config{
		APIKey:         *apiKey,
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
		log.Printf("Caliper APM server listening on http://%s (data-dir=%q)", *addr, *dataDir)
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
