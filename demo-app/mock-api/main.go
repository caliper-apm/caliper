package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// ChaosConfig controls runtime fault injection on the demo backend.
type ChaosConfig struct {
	LatencyMS   int     `json:"latency_ms"`
	ErrorRate   float64 `json:"error_rate"`
	TTFTDelayMS int     `json:"ttft_delay_ms"`
	TokenGapMS  int     `json:"token_gap_ms"`
}

type Server struct {
	mu  sync.RWMutex
	cfg ChaosConfig
	rng *rand.Rand
	mux *http.ServeMux
}

func NewServer() *Server {
	s := &Server{
		cfg: ChaosConfig{TokenGapMS: 25},
		rng: rand.New(rand.NewSource(20261120)),
		mux: http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /chaos/config", s.handleGetChaos)
	s.mux.HandleFunc("POST /chaos/config", s.handleSetChaos)
	s.mux.HandleFunc("GET /api/v1/feed", s.handleFeed)
	s.mux.HandleFunc("GET /api/v1/gallery", s.handleGallery)
	s.mux.HandleFunc("GET /api/v1/stream/article", s.handleStreamArticle)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) snapshotCfg() ChaosConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Server) shouldFail(rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Float64() < rate
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "atlas-reader-mock-api"})
}

func (s *Server) handleGetChaos(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshotCfg())
}

func (s *Server) handleSetChaos(w http.ResponseWriter, r *http.Request) {
	var next ChaosConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if next.LatencyMS < 0 || next.LatencyMS > 30000 || next.TTFTDelayMS < 0 || next.TTFTDelayMS > 30000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "delays must be in [0, 30000] ms"})
		return
	}
	if next.ErrorRate < 0 || next.ErrorRate > 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "error_rate must be in [0, 1]"})
		return
	}
	if next.TokenGapMS < 0 || next.TokenGapMS > 5000 {
		next.TokenGapMS = 25
	}
	s.mu.Lock()
	s.cfg = next
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, next)
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	cfg := s.snapshotCfg()
	if cfg.LatencyMS > 0 {
		select {
		case <-time.After(time.Duration(cfg.LatencyMS) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	if s.shouldFail(cfg.ErrorRate) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "chaos_injected_502"})
		return
	}
	items := make([]map[string]any, 0, 20)
	for i := 1; i <= 20; i++ {
		items = append(items, map[string]any{
			"id":          fmt.Sprintf("card-%03d", i),
			"title":       fmt.Sprintf("Atlas Field Report #%d: Mobile Rendering Pipelines", i),
			"kind":        []string{"article", "video", "gallery"}[i%3],
			"read_min":    3 + (i % 7),
			"image_bytes": 180_000 + i*12_000,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": "page-2"})
}

func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	cfg := s.snapshotCfg()
	if cfg.LatencyMS > 0 {
		select {
		case <-time.After(time.Duration(cfg.LatencyMS) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	if s.shouldFail(cfg.ErrorRate) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "chaos_injected_503"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"album": "High-Resolution Architecture Series",
		"photos": []map[string]any{
			{"id": "img-1", "width": 3840, "height": 2160, "format": "webp"},
			{"id": "img-2", "width": 3840, "height": 2160, "format": "webp"},
			{"id": "img-3", "width": 2560, "height": 1440, "format": "avif"},
		},
	})
}

func (s *Server) handleStreamArticle(w http.ResponseWriter, r *http.Request) {
	cfg := s.snapshotCfg()
	if s.shouldFail(cfg.ErrorRate) {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "chaos_stream_error"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	if cfg.TTFTDelayMS > 0 {
		select {
		case <-time.After(time.Duration(cfg.TTFTDelayMS) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	tokens := []string{
		"# Understanding Frame Budgets",
		"At 60Hz each frame has 16.67ms to measure, layout, and rasterize.",
		"Main-thread bitmap decoding or synchronous JSON parsing steals that budget.",
		"Caliper APM correlates dropped frames with same-window stall stacks.",
		"[DONE]",
	}
	for i, tok := range tokens {
		fmt.Fprintf(w, "id: %d\nevent: token\ndata: %s\n\n", i+1, tok)
		flusher.Flush()
		if cfg.TokenGapMS > 0 && i < len(tokens)-1 {
			select {
			case <-time.After(time.Duration(cfg.TokenGapMS) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8090", "Listen address for Atlas Reader mock API")
	flag.Parse()
	log.Printf("Atlas Reader mock-api listening on http://%s", *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           NewServer(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("mock-api failed: %v", err)
	}
}
