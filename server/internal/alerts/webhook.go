package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/caliper-apm/caliper/server/internal/storage"
)

// DeliveryReceipt records the real HTTP outcome of a webhook dispatch.
type DeliveryReceipt struct {
	Status     string `json:"delivery_status"` // delivered | failed | none
	HTTPStatus int    `json:"delivery_http_status,omitempty"`
	Error      string `json:"delivery_error,omitempty"`
}

// WebhookDispatcher formats and delivers alert notifications over HTTP(S).
type WebhookDispatcher struct {
	client *http.Client
}

// NewWebhookDispatcher creates a bounded-timeout webhook sender.
func NewWebhookDispatcher(timeout time.Duration) *WebhookDispatcher {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &WebhookDispatcher{
		client: &http.Client{Timeout: timeout},
	}
}

// Send formats inc according to rule.WebhookFormat (generic|feishu|slack|discord) and posts it.
func (d *WebhookDispatcher) Send(ctx context.Context, rule storage.AlertRule, inc storage.AlertIncident) DeliveryReceipt {
	if strings.TrimSpace(rule.WebhookURL) == "" {
		return DeliveryReceipt{Status: "none"}
	}
	parsedURL, err := url.Parse(rule.WebhookURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return DeliveryReceipt{Status: "failed", Error: "invalid webhook_url scheme or host"}
	}

	payload, err := FormatWebhookPayload(rule, inc)
	if err != nil {
		return DeliveryReceipt{Status: "failed", Error: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rule.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return DeliveryReceipt{Status: "failed", Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Caliper-APM-Alerts/0.1")

	resp, err := d.client.Do(req)
	if err != nil {
		return DeliveryReceipt{Status: "failed", Error: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return DeliveryReceipt{Status: "delivered", HTTPStatus: resp.StatusCode}
	}
	return DeliveryReceipt{
		Status:     "failed",
		HTTPStatus: resp.StatusCode,
		Error:      fmt.Sprintf("webhook returned HTTP %d", resp.StatusCode),
	}
}

// FormatWebhookPayload produces native webhook JSON payloads for generic, feishu, slack, and discord.
func FormatWebhookPayload(rule storage.AlertRule, inc storage.AlertIncident) ([]byte, error) {
	obsStr := "unknown"
	if inc.ObservedValue != nil {
		obsStr = fmt.Sprintf("%.2f", *inc.ObservedValue)
	}
	stateLabel := "FIRING"
	colorHex := 0xE53935
	feishuTemplate := "red"
	if inc.Status == "resolved" {
		stateLabel = "RESOLVED"
		colorHex = 0x2E7D32
		feishuTemplate = "green"
	}
	summary := fmt.Sprintf("[%s] %s (%s/%s): %s = %s (threshold %s %.2f, n=%d)",
		stateLabel, rule.Name, rule.Platform, rule.AppVersion, rule.MetricID, obsStr, rule.Operator, rule.Threshold, inc.SampleSize)

	switch strings.ToLower(rule.WebhookFormat) {
	case "", "generic":
		return json.Marshal(map[string]any{
			"source":   "caliper-apm",
			"status":   inc.Status,
			"summary":  summary,
			"rule":     rule,
			"incident": inc,
		})

	case "feishu":
		return json.Marshal(map[string]any{
			"msg_type": "interactive",
			"card": map[string]any{
				"header": map[string]any{
					"template": feishuTemplate,
					"title": map[string]string{
						"tag":     "plain_text",
						"content": fmt.Sprintf("[Caliper APM · %s] %s", stateLabel, rule.Name),
					},
				},
				"elements": []map[string]any{
					{
						"tag": "markdown",
						"content": fmt.Sprintf(
							"**App**: `%s` (%s / %s)\n**Metric**: `%s`\n**Observed**: `%s` (n=%d)\n**Threshold**: `%s %.2f`\n**Incident**: `%s`",
							rule.AppID, rule.Platform, rule.AppVersion, rule.MetricID, obsStr, inc.SampleSize, rule.Operator, rule.Threshold, inc.IncidentID,
						),
					},
				},
			},
		})

	case "slack":
		return json.Marshal(map[string]any{
			"text": summary,
			"blocks": []map[string]any{
				{
					"type": "header",
					"text": map[string]string{
						"type": "plain_text",
						"text": fmt.Sprintf("Caliper APM %s: %s", stateLabel, rule.Name),
					},
				},
				{
					"type": "section",
					"text": map[string]string{
						"type": "mrkdwn",
						"text": fmt.Sprintf("*App:* `%s` (`%s` / `%s`)\n*Metric:* `%s` = *%s* (threshold `%s %.2f`, samples: `%d`)",
							rule.AppID, rule.Platform, rule.AppVersion, rule.MetricID, obsStr, rule.Operator, rule.Threshold, inc.SampleSize),
					},
				},
			},
		})

	case "discord":
		return json.Marshal(map[string]any{
			"content": summary,
			"embeds": []map[string]any{
				{
					"title":       fmt.Sprintf("Caliper APM %s: %s", stateLabel, rule.Name),
					"description": summary,
					"color":       colorHex,
				},
			},
		})

	default:
		return nil, errors.New("unsupported webhook_format: " + rule.WebhookFormat)
	}
}
