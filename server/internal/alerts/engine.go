package alerts

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

// EvaluationOutcome captures one rule evaluation pass (including gated / healthy / firing / resolved).
type EvaluationOutcome struct {
	RuleID         string                 `json:"rule_id"`
	RuleName       string                 `json:"rule_name"`
	State          string                 `json:"state"` // firing | resolved | healthy | gated | disabled
	ObservedValue  *float64               `json:"observed_value"`
	SampleSize     int64                  `json:"sample_size"`
	GateReason     string                 `json:"gate_reason,omitempty"`
	Incident       *storage.AlertIncident `json:"incident,omitempty"`
	EvaluatedAtMS  int64                  `json:"evaluated_at_ms"`
}

// Engine evaluates Zero-Lie alert rules against live analytical metrics and sends real webhooks.
type Engine struct {
	store      storage.AnalyticsStore
	meta       storage.MetaStore
	analytics  *analytics.Engine
	dispatcher *WebhookDispatcher
	mu         sync.Mutex
}

// NewEngine creates a new alert evaluation & notification engine.
func NewEngine(store storage.AnalyticsStore, meta storage.MetaStore, eng *analytics.Engine, dispatcher *WebhookDispatcher) *Engine {
	if dispatcher == nil {
		dispatcher = NewWebhookDispatcher(5 * time.Second)
	}
	return &Engine{
		store:      store,
		meta:       meta,
		analytics:  eng,
		dispatcher: dispatcher,
	}
}

// EvaluateAll evaluates every configured rule and returns per-rule outcomes.
func (e *Engine) EvaluateAll(ctx context.Context, now time.Time) ([]EvaluationOutcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rules, err := e.meta.ListAlertRules(ctx, "", "")
	if err != nil {
		return nil, err
	}
	outcomes := make([]EvaluationOutcome, 0, len(rules))
	for _, rule := range rules {
		outcome, err := e.evaluateRuleLocked(ctx, rule, now)
		if err != nil {
			return outcomes, err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

// EvaluateRule evaluates a single rule by ID.
func (e *Engine) EvaluateRule(ctx context.Context, ruleID string, now time.Time) (EvaluationOutcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	rules, err := e.meta.ListAlertRules(ctx, "", "")
	if err != nil {
		return EvaluationOutcome{}, err
	}
	for _, r := range rules {
		if r.RuleID == ruleID {
			return e.evaluateRuleLocked(ctx, r, now)
		}
	}
	return EvaluationOutcome{}, fmt.Errorf("alert rule %q not found", ruleID)
}

func (e *Engine) evaluateRuleLocked(ctx context.Context, rule storage.AlertRule, now time.Time) (EvaluationOutcome, error) {
	nowMS := now.UnixMilli()
	out := EvaluationOutcome{
		RuleID:        rule.RuleID,
		RuleName:      rule.Name,
		EvaluatedAtMS: nowMS,
	}
	if !rule.Enabled {
		out.State = "disabled"
		return out, nil
	}

	val, n, missing, err := e.readMetric(ctx, rule)
	if err != nil {
		return out, err
	}
	out.ObservedValue = val
	out.SampleSize = n

	// Zero-Lie Gate: missing observations or samples below rule.MinSamples MUST NEVER fire or falsely resolve.
	if missing != "" {
		out.State = "gated"
		out.GateReason = missing
		return out, nil
	}
	if val == nil {
		out.State = "gated"
		out.GateReason = contract.MissingNoObservations
		return out, nil
	}
	if n < rule.MinSamples {
		out.State = "gated"
		out.GateReason = contract.MissingInsufficientSamples
		return out, nil
	}

	breached := (rule.Operator == "gt" && *val > rule.Threshold) || (rule.Operator == "lt" && *val < rule.Threshold)
	existing, _ := e.findActiveIncident(ctx, rule.RuleID)

	if breached {
		out.State = "firing"
		inc := storage.AlertIncident{
			IncidentID:      fmt.Sprintf("inc-%s-%d", rule.RuleID, nowMS),
			RuleID:          rule.RuleID,
			RuleName:        rule.Name,
			AppID:           rule.AppID,
			Platform:        rule.Platform,
			MetricID:        rule.MetricID,
			Status:          "firing",
			ObservedValue:   val,
			Threshold:       rule.Threshold,
			SampleSize:      n,
			TriggeredAtMS:   nowMS,
			LastEvaluatedMS: nowMS,
		}
		shouldNotify := true
		if existing != nil {
			inc = *existing
			inc.ObservedValue = val
			inc.SampleSize = n
			inc.LastEvaluatedMS = nowMS
			cooldownMS := rule.CooldownSec * 1000
			if inc.LastNotifiedMS > 0 && cooldownMS > 0 && (nowMS-inc.LastNotifiedMS) < cooldownMS {
				shouldNotify = false
				inc.DeliveryStatus = "skipped_cooldown"
			}
		}

		if shouldNotify {
			receipt := e.dispatcher.Send(ctx, rule, inc)
			inc.DeliveryStatus = receipt.Status
			inc.DeliveryHTTPStatus = receipt.HTTPStatus
			inc.DeliveryError = receipt.Error
			if receipt.Status == "delivered" {
				inc.LastNotifiedMS = nowMS
			}
		}

		if err := e.meta.SaveAlertIncident(ctx, inc); err != nil {
			return out, err
		}
		out.Incident = &inc
		return out, nil
	}

	// Not breached: if there was an active firing incident, resolve it and notify recovery!
	if existing != nil {
		inc := *existing
		inc.Status = "resolved"
		inc.ObservedValue = val
		inc.SampleSize = n
		inc.LastEvaluatedMS = nowMS
		inc.ResolvedAtMS = nowMS
		receipt := e.dispatcher.Send(ctx, rule, inc)
		inc.DeliveryStatus = receipt.Status
		inc.DeliveryHTTPStatus = receipt.HTTPStatus
		inc.DeliveryError = receipt.Error
		if receipt.Status == "delivered" {
			inc.LastNotifiedMS = nowMS
		}
		if err := e.meta.SaveAlertIncident(ctx, inc); err != nil {
			return out, err
		}
		out.State = "resolved"
		out.Incident = &inc
		return out, nil
	}

	out.State = "healthy"
	return out, nil
}

func (e *Engine) findActiveIncident(ctx context.Context, ruleID string) (*storage.AlertIncident, error) {
	list, err := e.meta.ListAlertIncidents(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Status == "firing" {
			return &list[i], nil
		}
	}
	return nil, nil
}

func (e *Engine) readMetric(ctx context.Context, rule storage.AlertRule) (*float64, int64, string, error) {
	f := storage.QueryFilter{
		AppID:      rule.AppID,
		Platform:   rule.Platform,
		Env:        rule.Env,
		AppVersion: rule.AppVersion,
		Scene:      rule.Scene,
	}

	switch rule.MetricID {
	case "startup_cold_p90_ms":
		rep, err := e.analytics.Startup(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		return rep.TotalP90.Value, rep.TotalP90.SampleSize, rep.TotalP90.MissingReason, nil

	case "startup_cold_p95_ms":
		rep, err := e.analytics.Startup(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		return rep.TotalP95.Value, rep.TotalP95.SampleSize, rep.TotalP95.MissingReason, nil

	case "startup_cold_p99_ms":
		rep, err := e.analytics.Startup(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		return rep.TotalP99.Value, rep.TotalP99.SampleSize, rep.TotalP99.MissingReason, nil

	case "startup_cold_p50_ms":
		rep, err := e.analytics.Startup(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		return rep.TotalP50.Value, rep.TotalP50.SampleSize, rep.TotalP50.MissingReason, nil

	case "fluency_jank_rate_pct":
		rows, err := e.analytics.Fluency(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		var validWin, totalFrames, jankFrames int64
		algos := map[string]struct{}{}
		for _, r := range rows {
			if r.ValidWindows > 0 {
				algos[r.Algorithm] = struct{}{}
				validWin += r.ValidWindows
				totalFrames += r.TotalFrames
				jankFrames += r.JankFrames
			}
		}
		if validWin == 0 || totalFrames == 0 {
			return nil, 0, contract.MissingNoObservations, nil
		}
		if len(algos) > 1 {
			return nil, validWin, contract.MissingIncompatibleAlgo, nil
		}
		pct := (float64(jankFrames) / float64(totalFrames)) * 100.0
		return &pct, validWin, "", nil

	case "resource_cpu_avg_pct":
		rows, err := e.analytics.Resources(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		var totalWin int64
		var weightedCPU float64
		for _, r := range rows {
			if r.AvgCPUPct != nil && r.ResourceWindows > 0 {
				weightedCPU += *r.AvgCPUPct * float64(r.ResourceWindows)
				totalWin += r.ResourceWindows
			}
		}
		if totalWin == 0 {
			return nil, 0, contract.MissingNoObservations, nil
		}
		avg := weightedCPU / float64(totalWin)
		return &avg, totalWin, "", nil

	case "resource_memory_peak_p90_mib", "resource_memory_peak_p95_mib", "resource_memory_peak_p99_mib":
		aggs, err := e.store.QueryResources(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		var hist contract.Histogram
		var sk *contract.DDSketch
		for _, r := range aggs {
			hist.Add(r.MemoryPeakHistMiB)
			contract.MergeDDSketch(&sk, r.MemoryPeakSketchMiB)
		}
		p := 0.90
		if rule.MetricID == "resource_memory_peak_p95_mib" {
			p = 0.95
		} else if rule.MetricID == "resource_memory_peak_p99_mib" {
			p = 0.99
		}
		q := contract.QuantileMemoryMiBSketch(sk, hist, p, rule.MinSamples)
		return q.Value, q.SampleSize, q.MissingReason, nil

	case "network_api_success_rate_pct":
		rep, err := e.analytics.Network(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		return rep.SuccessRatePct, rep.TotalCalls, rep.MissingReason, nil

	case "network_api_p90_ms":
		rep, err := e.analytics.Network(ctx, f)
		if err != nil {
			return nil, 0, "", err
		}
		return rep.DurationP90.Value, rep.DurationP90.SampleSize, rep.DurationP90.MissingReason, nil

	case "crash_event_count":
		issues, err := e.store.QueryIssues(ctx, f, 200)
		if err != nil {
			return nil, 0, "", err
		}
		var count int64
		for _, is := range issues {
			if is.Kind == "crash" {
				count += is.EventCount
			}
		}
		v := float64(count)
		return &v, count, "", nil

	default:
		return nil, 0, "unsupported_metric", nil
	}
}
