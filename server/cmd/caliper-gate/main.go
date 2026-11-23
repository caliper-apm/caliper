package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
)

func main() {
	server := flag.String("server", "http://127.0.0.1:8080", "Caliper APM server base URL")
	appID := flag.String("app-id", "", "Optional app_id filter")
	platform := flag.String("platform", "", "Target platform: android | ios (required)")
	env := flag.String("env", "prod", "Target environment")
	baseline := flag.String("baseline", "", "Baseline release version (required)")
	candidate := flag.String("candidate", "", "Candidate release version (required)")
	minSamples := flag.Int64("min-samples", 0, "Override minimum sample gate (0 uses server Zero-Lie default)")
	maxRegressed := flag.Int("max-regressed", 0, "Maximum allowed regressed metrics before blocking")
	requireSymbols := flag.Bool("require-symbols", false, "Block release if candidate lacks bound symbol archive")
	allowGated := flag.Bool("allow-gated", false, "Exit 0 instead of 3 when verdict is gated (insufficient samples)")
	format := flag.String("format", "markdown", "Output format: markdown | json | text")
	apiKey := flag.String("api-key", os.Getenv("CALIPER_API_KEY"), "Optional X-Caliper-Key / Bearer token")
	timeout := flag.Duration("timeout", 20*time.Second, "HTTP request timeout")
	flag.Parse()

	if (*platform != "android" && *platform != "ios") || *baseline == "" || *candidate == "" {
		fmt.Fprintln(os.Stderr, "usage: caliper-gate --server URL --platform android|ios --baseline X.Y.Z --candidate A.B.C")
		os.Exit(1)
	}

	reqPayload := analytics.ReleaseGateRequest{
		AppID:               *appID,
		Platform:            *platform,
		Env:                 *env,
		BaselineVersion:     *baseline,
		CandidateVersion:    *candidate,
		MinSamples:          *minSamples,
		MaxRegressedMetrics: *maxRegressed,
		RequireSymbolBound:  *requireSymbols,
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal request: %v\n", err)
		os.Exit(1)
	}

	url := strings.TrimRight(*server, "/") + "/v1/analytics/release-gate"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "create request: %v\n", err)
		os.Exit(1)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if *apiKey != "" {
		httpReq.Header.Set("X-Caliper-Key", *apiKey)
	}

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		fmt.Fprintf(os.Stderr, "read response: %v\n", err)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "server returned HTTP %d: %s\n", resp.StatusCode, strings.TrimSpace(string(raw)))
		os.Exit(1)
	}

	var rep analytics.ReleaseGateReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
		os.Exit(1)
	}

	switch *format {
	case "json":
		pretty, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(pretty))
	case "text":
		fmt.Printf("verdict=%s score=%d/100 platform=%s baseline=v%s candidate=v%s regressed=%d gated=%d symbols_bound=%v\n",
			rep.Verdict, rep.HealthScore, rep.Platform, rep.BaselineVersion, rep.CandidateVersion,
			rep.RegressedCount, rep.GatedCount, rep.CandidateSymbolBound)
		for _, r := range rep.BlockingReasons {
			fmt.Printf("  BLOCK: %s\n", r)
		}
		for _, w := range rep.Warnings {
			fmt.Printf("  WARN:  %s\n", w)
		}
	default:
		fmt.Print(rep.MarkdownSummary)
	}

	exitCode := rep.ExitCode
	if exitCode == 3 && *allowGated {
		exitCode = 0
	}
	os.Exit(exitCode)
}
