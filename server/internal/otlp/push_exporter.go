package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

// PushConfig configures the asynchronous OTLP/HTTP JSON trace push exporter
// for forwarding mobile W3C traceparent CLIENT spans to Jaeger, Grafana Tempo, or an OTel Collector.
type PushConfig struct {
	Endpoint      string            // e.g. http://tempo:4318/v1/traces (auto-appends /v1/traces if omitted)
	Headers       map[string]string // optional auth/routing headers (e.g. Authorization)
	BatchSize     int               // default 128 spans
	FlushInterval time.Duration     // default 2s
	QueueCapacity int               // default 4096 spans (bounded non-blocking buffer)
	Timeout       time.Duration     // default 5s per HTTP POST
}

// PushStats exposes operational health counters for the trace push pipeline.
type PushStats struct {
	Enqueued int64 `json:"enqueued"`
	Exported int64 `json:"exported"`
	Dropped  int64 `json:"dropped"`
	Errors   int64 `json:"errors"`
}

// TracePushExporter batches mobile W3C traceparent network_transport samples off the ingest
// hot path and pushes standard OTLP/HTTP JSON payloads (POST /v1/traces) asynchronously.
type TracePushExporter struct {
	endpoint      string
	headers       map[string]string
	batchSize     int
	flushInterval time.Duration
	client        *http.Client
	ch            chan storage.NetworkTransportSample
	done          chan struct{}
	stopOnce      sync.Once
	closed        atomic.Bool
	wg            sync.WaitGroup

	enqueued atomic.Int64
	exported atomic.Int64
	dropped  atomic.Int64
	errCount atomic.Int64
}

// ParseHeaderList parses comma-separated "Key=Value,Key2=Value2" header flags/env strings.
func ParseHeaderList(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// NewTracePushExporter starts a bounded, fail-closed background OTLP trace push worker.
// Returns nil if cfg.Endpoint is empty.
func NewTracePushExporter(cfg PushConfig) *TracePushExporter {
	ep := strings.TrimSpace(cfg.Endpoint)
	if ep == "" {
		return nil
	}
	ep = strings.TrimRight(ep, "/")
	if !strings.HasSuffix(ep, "/v1/traces") {
		ep += "/v1/traces"
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 128
	}
	flushInterval := cfg.FlushInterval
	if flushInterval <= 0 {
		flushInterval = 2 * time.Second
	}
	queueCap := cfg.QueueCapacity
	if queueCap <= 0 {
		queueCap = 4096
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	hdrs := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		hdrs[k] = v
	}

	e := &TracePushExporter{
		endpoint:      ep,
		headers:       hdrs,
		batchSize:     batchSize,
		flushInterval: flushInterval,
		client:        &http.Client{Timeout: timeout},
		ch:            make(chan storage.NetworkTransportSample, queueCap),
		done:          make(chan struct{}),
	}
	e.wg.Add(1)
	go e.run()
	return e
}

// EnqueueBatch extracts valid W3C traceparent network_transport samples from batch
// and enqueues them non-blockingly. Never blocks or fails mobile ingestion.
func (e *TracePushExporter) EnqueueBatch(batch *protocol.BatchEnvelope) {
	if e == nil || batch == nil || e.closed.Load() {
		return
	}
	for i := range batch.Events {
		ev := &batch.Events[i]
		if ev.Kind != protocol.KindNetworkStage {
			continue
		}
		var p protocol.NetworkTransportPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		traceID := normalizeW3CHex(p.TraceID, 32)
		if traceID == "" {
			continue
		}
		sample := storage.NetworkTransportSample{
			EventID:          ev.EventID,
			OccurredAtMS:     ev.OccurredAtMS,
			AppID:            batch.Resource.AppID,
			Platform:         batch.Resource.Platform,
			Env:              batch.Resource.Env,
			AppVersion:       batch.Resource.AppVersion,
			BuildNumber:      batch.Resource.BuildNumber,
			Adapter:          batch.Resource.Adapter,
			Scene:            ev.Scene,
			DeviceModel:      batch.Resource.DeviceModel,
			OSVersion:        batch.Resource.OSVersion,
			InstallHash:      batch.Resource.InstallHash,
			UserHash:         batch.Resource.UserHash,
			Operation:        p.Operation,
			Method:           p.Method,
			StatusCode:       p.StatusCode,
			Result:           p.Result,
			DNSMS:            p.DNSMS,
			TCPMS:            p.TCPMS,
			TLSMS:            p.TLSMS,
			TTFBMS:           p.TTFBMS,
			BodyMS:           p.BodyMS,
			TotalMS:          p.TotalMS,
			ConnectionReused: p.ConnectionReused,
			Protocol:         p.Protocol,
			NetworkType:      p.NetworkType,
			TraceID:          traceID,
			SpanID:           normalizeW3CHex(p.SpanID, 16),
		}
		select {
		case e.ch <- sample:
			e.enqueued.Add(1)
		default:
			e.dropped.Add(1)
		}
	}
}

// Stats returns thread-safe export counters.
func (e *TracePushExporter) Stats() PushStats {
	if e == nil {
		return PushStats{}
	}
	return PushStats{
		Enqueued: e.enqueued.Load(),
		Exported: e.exported.Load(),
		Dropped:  e.dropped.Load(),
		Errors:   e.errCount.Load(),
	}
}

// Close stops accepting new spans, flushes queued spans best-effort, and waits until worker exits
// or ctx expires.
func (e *TracePushExporter) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	e.stopOnce.Do(func() {
		e.closed.Store(true)
		close(e.done)
	})
	finished := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *TracePushExporter) run() {
	defer e.wg.Done()
	ticker := time.NewTicker(e.flushInterval)
	defer ticker.Stop()

	buf := make([]storage.NetworkTransportSample, 0, e.batchSize)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		e.sendBatch(buf)
		buf = buf[:0]
	}

	for {
		select {
		case sample := <-e.ch:
			buf = append(buf, sample)
			if len(buf) >= e.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-e.done:
			// Drain remaining queued items best-effort before exit.
			for {
				select {
				case sample := <-e.ch:
					buf = append(buf, sample)
					if len(buf) >= e.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (e *TracePushExporter) sendBatch(samples []storage.NetworkTransportSample) {
	payload := ExportNetworkSamplesAsOTLPTraces("", samples)
	if len(payload.ResourceSpans) == 0 {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		e.errCount.Add(1)
		return
	}
	req, err := http.NewRequest(http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		e.errCount.Add(1)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range e.headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.errCount.Add(1)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e.errCount.Add(1)
		return
	}
	e.exported.Add(int64(len(samples)))
}
