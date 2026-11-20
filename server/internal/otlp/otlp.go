package otlp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

// OpenTelemetry OTLP/HTTP JSON data model (opentelemetry-proto v1.x compatible, zero external deps).

type AnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	IntValue    any      `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
}

type KeyValue struct {
	Key   string   `json:"key"`
	Value AnyValue `json:"value"`
}

type Resource struct {
	Attributes []KeyValue `json:"attributes"`
}

type SpanEvent struct {
	TimeUnixNano string     `json:"timeUnixNano,omitempty"`
	Name         string     `json:"name"`
	Attributes   []KeyValue `json:"attributes,omitempty"`
}

type SpanStatus struct {
	Code    int    `json:"code,omitempty"` // 0=UNSET, 1=OK, 2=ERROR
	Message string `json:"message,omitempty"`
}

type Span struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	Name              string      `json:"name"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []KeyValue  `json:"attributes,omitempty"`
	Events            []SpanEvent `json:"events,omitempty"`
	Status            *SpanStatus `json:"status,omitempty"`
}

type ScopeSpans struct {
	Spans []Span `json:"spans"`
}

type ResourceSpans struct {
	Resource   Resource     `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}

// ExportTraceServiceRequest matches standard POST /v1/traces OTLP JSON payloads.
type ExportTraceServiceRequest struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}

type LogRecord struct {
	TimeUnixNano   string     `json:"timeUnixNano,omitempty"`
	SeverityNumber int        `json:"severityNumber,omitempty"`
	SeverityText   string     `json:"severityText,omitempty"`
	Body           AnyValue   `json:"body"`
	Attributes     []KeyValue `json:"attributes,omitempty"`
}

type ScopeLogs struct {
	LogRecords []LogRecord `json:"logRecords"`
}

type ResourceLogs struct {
	Resource  Resource    `json:"resource"`
	ScopeLogs []ScopeLogs `json:"scopeLogs"`
}

// ExportLogsServiceRequest matches standard POST /v1/logs OTLP JSON payloads.
type ExportLogsServiceRequest struct {
	ResourceLogs []ResourceLogs `json:"resourceLogs"`
}

// TranslateTraces converts standard OTLP/HTTP JSON resourceSpans into Caliper BatchEnvelopes.
func TranslateTraces(req ExportTraceServiceRequest) ([]*protocol.BatchEnvelope, error) {
	nowMS := time.Now().UTC().UnixMilli()
	var batches []*protocol.BatchEnvelope

	for _, rs := range req.ResourceSpans {
		res := mapResource(rs.Resource.Attributes)
		var events []protocol.Event

		for _, ss := range rs.ScopeSpans {
			for idx, sp := range ss.Spans {
				startMS := nanoToMS(sp.StartTimeUnixNano, nowMS)
				endMS := nanoToMS(sp.EndTimeUnixNano, startMS)
				durMS := float64(max(0, endMS-startMS))
				attrs := attrsMap(sp.Attributes)
				scene := strAttr(attrs, "caliper.scene", "app.screen", "scene")
				if scene == "" {
					scene = "otlp_trace"
				}
				eventID := stableEventID("otlp-sp-", sp.TraceID+":"+sp.SpanID+":"+strconv.Itoa(idx))

				switch {
				case strings.HasPrefix(strings.ToLower(sp.Name), "app.startup") || strAttr(attrs, "caliper.kind") == protocol.KindStartup:
					payload, _ := json.Marshal(protocol.StartupPayload{
						TotalDurationMS: durMS,
						ColdStart:       true,
						StartAnchor:     "otlp_span_start",
						EndAnchor:       "otlp_span_end",
						Result:          spanStartupResult(sp.Status),
					})
					events = append(events, protocol.Event{
						EventID:         eventID,
						Kind:            protocol.KindStartup,
						OccurredAtMS:    startMS,
						ContractVersion: 1,
						Scene:           scene,
						Payload:         payload,
					})

				case strAttr(attrs, "http.request.method", "http.method") != "":
					method := normalizeHTTPMethod(strAttr(attrs, "http.request.method", "http.method"))
					op := strAttr(attrs, "http.route", "url.path", "http.target")
					if op == "" {
						op = sp.Name
					}
					statusCode := int(intAttr(attrs, "http.response.status_code", "http.status_code"))
					result := "success"
					if (sp.Status != nil && sp.Status.Code == 2) || statusCode >= 500 {
						result = "failed"
					}
					payload, _ := json.Marshal(protocol.NetworkTransportPayload{
						Operation:  truncate(op, 128),
						Method:     method,
						StatusCode: statusCode,
						Result:     result,
						TotalMS:    durMS,
					})
					events = append(events, protocol.Event{
						EventID:         eventID,
						Kind:            protocol.KindNetworkStage,
						OccurredAtMS:    startMS,
						ContractVersion: 1,
						Scene:           scene,
						Payload:         payload,
					})

				default:
					status := "success"
					if sp.Status != nil && sp.Status.Code == 2 {
						status = "failure"
					}
					payload, _ := json.Marshal(protocol.CustomScenePayload{
						JourneyName: truncate(sp.Name, 64),
						Status:      status,
						DurationMS:  &durMS,
					})
					events = append(events, protocol.Event{
						EventID:         eventID,
						Kind:            protocol.KindCustomScene,
						OccurredAtMS:    startMS,
						ContractVersion: 1,
						Scene:           scene,
						Payload:         payload,
					})
				}

				for eIdx, se := range sp.Events {
					if se.Name != "exception" {
						continue
					}
					eAttrs := attrsMap(se.Attributes)
					excType := strAttr(eAttrs, "exception.type")
					excMsg := strAttr(eAttrs, "exception.message")
					stack := strAttr(eAttrs, "exception.stacktrace")
					title := strings.TrimSpace(excType + ": " + excMsg)
					if title == ":" || title == "" {
						title = "OTLP Span Exception in " + sp.Name
					}
					payload, _ := json.Marshal(protocol.StabilityPayload{
						Fingerprint:   stableEventID("otlp-exc-", excType+"|"+excMsg+"|"+sp.Name),
						Title:         truncate(title, 256),
						Fatal:         boolAttr(eAttrs, "exception.escaped"),
						ExceptionType: excType,
						Frames:        parseStackLines(stack),
					})
					events = append(events, protocol.Event{
						EventID:         stableEventID("otlp-ex-", sp.TraceID+":"+sp.SpanID+":"+strconv.Itoa(eIdx)),
						Kind:            protocol.KindCrash,
						OccurredAtMS:    nanoToMS(se.TimeUnixNano, startMS),
						ContractVersion: 1,
						Scene:           scene,
						Payload:         payload,
					})
				}
			}
		}

		for len(events) > 0 {
			chunk := events
			if len(chunk) > protocol.MaxBatchEvents {
				chunk = events[:protocol.MaxBatchEvents]
			}
			batches = append(batches, &protocol.BatchEnvelope{
				SchemaVersion: protocol.CurrentSchemaVersion,
				SentAtMS:      nowMS,
				Resource:      res,
				Events:        chunk,
			})
			events = events[len(chunk):]
		}
	}
	return batches, nil
}

// TranslateLogs converts OTLP ERROR/FATAL log records into Stability crash events.
func TranslateLogs(req ExportLogsServiceRequest) ([]*protocol.BatchEnvelope, error) {
	nowMS := time.Now().UTC().UnixMilli()
	var batches []*protocol.BatchEnvelope

	for _, rl := range req.ResourceLogs {
		res := mapResource(rl.Resource.Attributes)
		var events []protocol.Event
		for _, sl := range rl.ScopeLogs {
			for idx, rec := range sl.LogRecords {
				if rec.SeverityNumber < 17 && !strings.EqualFold(rec.SeverityText, "ERROR") && !strings.EqualFold(rec.SeverityText, "FATAL") {
					continue
				}
				attrs := attrsMap(rec.Attributes)
				msg := ""
				if rec.Body.StringValue != nil {
					msg = *rec.Body.StringValue
				}
				if msg == "" {
					msg = "OTLP error log"
				}
				ts := nanoToMS(rec.TimeUnixNano, nowMS)
				stack := strAttr(attrs, "exception.stacktrace", "code.stacktrace")
				payload, _ := json.Marshal(protocol.StabilityPayload{
					Fingerprint:   stableEventID("otlp-log-", msg),
					Title:         truncate(msg, 256),
					Fatal:         rec.SeverityNumber >= 21 || strings.EqualFold(rec.SeverityText, "FATAL"),
					ExceptionType: strAttr(attrs, "exception.type"),
					Frames:        parseStackLines(stack),
				})
				events = append(events, protocol.Event{
					EventID:         stableEventID("otlp-lg-", strconv.FormatInt(ts, 10)+":"+strconv.Itoa(idx)+":"+msg),
					Kind:            protocol.KindCrash,
					OccurredAtMS:    ts,
					ContractVersion: 1,
					Scene:           strAttr(attrs, "caliper.scene", "app.screen"),
					Payload:         payload,
				})
			}
		}
		if len(events) > 0 {
			batches = append(batches, &protocol.BatchEnvelope{
				SchemaVersion: protocol.CurrentSchemaVersion,
				SentAtMS:      nowMS,
				Resource:      res,
				Events:        events,
			})
		}
	}
	return batches, nil
}

// ExportOverviewAsOTLPMetrics renders a Caliper OverviewReport as standard OTLP JSON resourceMetrics.
func ExportOverviewAsOTLPMetrics(appID string, ov analytics.OverviewReport) map[string]any {
	nowNano := strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	metrics := make([]map[string]any, 0, 8)
	addGauge := func(name, unit string, val *float64, extraAttrs ...map[string]any) {
		if val == nil {
			return
		}
		dp := map[string]any{
			"timeUnixNano": nowNano,
			"asDouble":     *val,
		}
		if len(extraAttrs) > 0 {
			dp["attributes"] = extraAttrs
		}
		metrics = append(metrics, map[string]any{
			"name": name,
			"unit": unit,
			"gauge": map[string]any{
				"dataPoints": []any{dp},
			},
		})
	}

	addGauge("caliper.startup.cold.p50", "ms", ov.Startup.TotalP50.Value)
	addGauge("caliper.startup.cold.p90", "ms", ov.Startup.TotalP90.Value)
	addGauge("caliper.network.api.success_rate", "%", ov.Network.SuccessRatePct)
	addGauge("caliper.network.api.p90", "ms", ov.Network.DurationP90.Value)

	for _, fl := range ov.Fluency {
		addGauge("caliper.fluency.jank_rate", "%", fl.JankRatePct,
			kvString("scene", fl.Scene),
			kvString("algorithm", fl.Algorithm),
		)
	}
	for _, res := range ov.Resources {
		addGauge("caliper.resource.cpu.avg", "%", res.AvgCPUPct, kvString("scene", res.Scene))
		addGauge("caliper.resource.memory.peak_p90", "MiB", res.MemoryPeakP90MiB.Value, kvString("scene", res.Scene))
	}

	return map[string]any{
		"resourceMetrics": []any{
			map[string]any{
				"resource": map[string]any{
					"attributes": []any{
						kvString("service.name", nonEmpty(appID, "caliper.mobile")),
						kvString("os.name", ov.Platform),
						kvString("service.version", nonEmpty(ov.AppVersion, "all")),
					},
				},
				"scopeMetrics": []any{
					map[string]any{
						"scope":   map[string]any{"name": "io.caliper.apm.otlp", "version": "1.0.0"},
						"metrics": metrics,
					},
				},
			},
		},
	}
}

func mapResource(kvs []KeyValue) protocol.Resource {
	attrs := attrsMap(kvs)
	plat := strings.ToLower(strAttr(attrs, "os.name", "caliper.platform"))
	if plat != "android" && plat != "ios" {
		plat = "android"
	}
	env := strings.ToLower(strAttr(attrs, "deployment.environment", "deployment.environment.name", "caliper.env"))
	if env != "prod" && env != "staging" && env != "dev" {
		env = "prod"
	}
	install := strAttr(attrs, "device.id", "caliper.install_hash")
	if len(install) < 16 || !isLowerHex(install) {
		sum := sha256.Sum256([]byte(nonEmpty(install, "otlp-default-device")))
		install = hex.EncodeToString(sum[:16])
	}
	userHash := strAttr(attrs, "caliper.user_hash")
	if userHash != "" && (len(userHash) < 16 || !isLowerHex(userHash)) {
		sum := sha256.Sum256([]byte(userHash))
		userHash = hex.EncodeToString(sum[:16])
	}
	return protocol.Resource{
		AppID:       truncate(nonEmpty(strAttr(attrs, "service.name", "app.id"), "dev.caliper.otlp"), 128),
		Platform:    plat,
		Env:         env,
		AppVersion:  truncate(nonEmpty(strAttr(attrs, "service.version", "app.version"), "1.0.0"), 64),
		BuildNumber: truncate(nonEmpty(strAttr(attrs, "app.build_id", "caliper.build_number"), "1"), 64),
		Adapter:     "native",
		SDKName:     truncate(nonEmpty(strAttr(attrs, "telemetry.sdk.name"), "opentelemetry-otlp"), 64),
		SDKVersion:  truncate(nonEmpty(strAttr(attrs, "telemetry.sdk.version"), "1.0.0"), 32),
		InstallHash: install,
		UserHash:    userHash,
		OSVersion:   truncate(strAttr(attrs, "os.version"), 32),
		DeviceModel: truncate(strAttr(attrs, "device.model.identifier", "device.model.name"), 64),
	}
}

func attrsMap(kvs []KeyValue) map[string]AnyValue {
	m := make(map[string]AnyValue, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

func strAttr(m map[string]AnyValue, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v.StringValue != nil && *v.StringValue != "" {
			return *v.StringValue
		}
	}
	return ""
}

func intAttr(m map[string]AnyValue, keys ...string) int64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v.IntValue == nil {
			continue
		}
		switch x := v.IntValue.(type) {
		case float64:
			return int64(x)
		case string:
			if n, err := strconv.ParseInt(x, 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

func boolAttr(m map[string]AnyValue, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok && v.BoolValue != nil {
			return *v.BoolValue
		}
	}
	return false
}

func nanoToMS(ns string, fallback int64) int64 {
	if ns == "" {
		return fallback
	}
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n / 1_000_000
}

func spanStartupResult(st *SpanStatus) string {
	if st != nil && st.Code == 2 {
		return "timeout"
	}
	return "success"
}

func normalizeHTTPMethod(m string) string {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return strings.ToUpper(strings.TrimSpace(m))
	default:
		return "GET"
	}
}

func parseStackLines(raw string) []protocol.StackFrame {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var frames []protocol.StackFrame
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		frames = append(frames, protocol.StackFrame{Symbol: truncate(line, 256), InApp: true})
		if len(frames) >= 32 {
			break
		}
	}
	return frames
}

func stableEventID(prefix, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return prefix + hex.EncodeToString(sum[:12])
}

func kvString(k, v string) map[string]any {
	return map[string]any{"key": k, "value": map[string]any{"stringValue": v}}
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func isLowerHex(s string) bool {
	if len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
