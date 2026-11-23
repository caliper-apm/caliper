package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/storage/objectstore"
)

func TestV13OpsPolicyIssueTriageGateAlertAndS3(t *testing.T) {
	// 1. Mock S3 server verifying SigV4 PUT/GET for symbols & remote logs
	var s3Mu sync.Mutex
	s3Objects := map[string][]byte{}
	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/caliper-blobs/")
		s3Mu.Lock()
		defer s3Mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			s3Objects[key] = raw
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := s3Objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer s3Srv.Close()

	s3Store, err := objectstore.NewS3(objectstore.S3Config{
		Endpoint:  s3Srv.URL,
		Bucket:    "caliper-blobs",
		Region:    "us-east-1",
		AccessKey: "test-ak",
		SecretKey: "test-sk",
	})
	if err != nil {
		t.Fatalf("new s3 store: %v", err)
	}

	st, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer st.Close()
	st.SetObjectStore(s3Store)

	// Verify symbol round-trip via S3
	ctx := context.Background()
	_, err = st.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID:    "sym-s3-100",
		AppID:       "dev.caliper.demo",
		Platform:    "android",
		Env:         "prod",
		AppVersion:  "1.0.0",
		BuildNumber: "100",
		Kind:        "r8_mapping",
		SHA256:      "abc123",
	}, []byte("com.caliper.Demo -> a.b:\n"))
	if err != nil {
		t.Fatalf("save symbol to s3: %v", err)
	}
	_, symBytes, found, err := st.GetSymbolContent(ctx, "sym-s3-100")
	if err != nil || !found || !strings.Contains(string(symBytes), "com.caliper.Demo") {
		t.Fatalf("read symbol from s3 failed: found=%v err=%v body=%q", found, err, string(symBytes))
	}

	h := NewHandler(st, st, Config{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// 2. PUT /v1/policy + GET /v1/policy
	polBody, _ := json.Marshal(storage.SamplingPolicy{
		PolicyID:          "pol-canary-v2",
		AppID:             "dev.caliper.demo",
		Platform:          "android",
		Env:               "prod",
		RatePPM:           250000,
		LeaseTTLMS:        1800000,
		MaxEvents24h:      400,
		EnabledCollectors: []string{"startup", "fluency", "network", "stability", "cpu_profile"},
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/policy", bytes.NewReader(polBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /v1/policy failed: err=%v status=%v", err, resp.StatusCode)
	}
	resp.Body.Close()

	getPol, err := http.Get(srv.URL + "/v1/policy?app_id=dev.caliper.demo&platform=android&env=prod")
	if err != nil || getPol.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/policy failed: %v", err)
	}
	var gotPol storage.SamplingPolicy
	_ = json.NewDecoder(getPol.Body).Decode(&gotPol)
	getPol.Body.Close()
	if gotPol.RatePPM != 250000 || gotPol.PolicyID != "pol-canary-v2" {
		t.Fatalf("unexpected policy: %+v", gotPol)
	}

	// 3. Ingest crash events across devices/OS + startup events for release gate
	now := time.Now().UTC().UnixMilli()
	ingestRelease := func(ver, build, device, osVer string, startupMS float64, withCrash bool) {
		evs := make([]protocol.Event, 0, 125)
		for i := 0; i < 110; i++ {
			sp, _ := json.Marshal(protocol.StartupPayload{
				TotalDurationMS: startupMS,
				ColdStart:       true,
				StartAnchor:     "application_on_create",
				EndAnchor:       "first_content_frame",
				Result:          "success",
			})
			evs = append(evs, protocol.Event{
				EventID:         fmt.Sprintf("ev-%s-%s-%d", ver, device, i),
				Kind:            protocol.KindStartup,
				OccurredAtMS:    now,
				ContractVersion: 1,
				PolicyID:        "default_v1",
				Payload:         sp,
			})
		}
		if withCrash {
			cp, _ := json.Marshal(protocol.StabilityPayload{
				Fingerprint:   "fp-oom-decoder",
				Title:         "OutOfMemoryError in Decoder",
				Fatal:         true,
				ExceptionType: "OutOfMemoryError",
			})
			evs = append(evs, protocol.Event{
				EventID:         fmt.Sprintf("cr-%s-%s", ver, device),
				Kind:            protocol.KindCrash,
				OccurredAtMS:    now,
				ContractVersion: 1,
				PolicyID:        "default_v1",
				Payload:         cp,
			})
		}
		envBody, _ := json.Marshal(protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      now,
			Resource: protocol.Resource{
				AppID:       "dev.caliper.demo",
				Platform:    "android",
				Env:         "prod",
				AppVersion:  ver,
				BuildNumber: build,
				SDKName:     "caliper-android",
				SDKVersion:  "1.3.0",
				InstallHash: "1234567890abcdef",
				UserHash:    "0000111122223333",
				DeviceModel: device,
				OSVersion:   osVer,
			},
			Events: evs,
		})
		r, err := http.Post(srv.URL+"/v1/ingest", "application/json", bytes.NewReader(envBody))
		if err != nil || r.StatusCode != http.StatusAccepted {
			t.Fatalf("ingest %s failed: err=%v", ver, err)
		}
		r.Body.Close()
	}

	ingestRelease("1.0.0", "100", "Pixel 8", "Android 14", 480, false)
	ingestRelease("1.1.0", "110", "Redmi Note 13", "Android 12", 2400, true)
	ingestRelease("1.1.0", "110", "Galaxy A54", "Android 13", 1900, true)

	// 4. PATCH /v1/stability/issues/{fp} + verify device/os breakdown & triage overlay
	patchBody := `{"status":"in_progress","assignee":"alice","note":"Investigating bitmap cache","app_id":"dev.caliper.demo","platform":"android"}`
	patchReq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/v1/stability/issues/fp-oom-decoder", strings.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil || patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH issue state failed: err=%v status=%v", err, patchResp.StatusCode)
	}
	patchResp.Body.Close()

	detResp, err := http.Get(srv.URL + "/v1/stability/issues/fp-oom-decoder?platform=android")
	if err != nil || detResp.StatusCode != http.StatusOK {
		t.Fatalf("GET issue detail failed: %v", err)
	}
	var det struct {
		Status          string           `json:"status"`
		Assignee        string           `json:"assignee"`
		SampleUserHash  string           `json:"sample_user_hash"`
		DeviceBreakdown map[string]int64 `json:"device_breakdown"`
		OSBreakdown     map[string]int64 `json:"os_breakdown"`
	}
	_ = json.NewDecoder(detResp.Body).Decode(&det)
	detResp.Body.Close()
	if det.Status != "in_progress" || det.Assignee != "alice" || det.SampleUserHash != "0000111122223333" {
		t.Fatalf("unexpected issue detail: %+v", det)
	}
	if det.DeviceBreakdown["Redmi Note 13"] != 1 || det.OSBreakdown["Android 12"] != 1 {
		t.Fatalf("unexpected device/os breakdown: dev=%+v os=%+v", det.DeviceBreakdown, det.OSBreakdown)
	}

	// 5. POST /v1/analytics/release-gate (expect BLOCK exit 2 on 1.0.0 -> 1.1.0)
	gateReq, _ := json.Marshal(analytics.ReleaseGateRequest{
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		BaselineVersion:  "1.0.0",
		CandidateVersion: "1.1.0",
	})
	gResp, err := http.Post(srv.URL+"/v1/analytics/release-gate", "application/json", bytes.NewReader(gateReq))
	if err != nil || gResp.StatusCode != http.StatusOK {
		t.Fatalf("release-gate failed: err=%v", err)
	}
	var gRep analytics.ReleaseGateReport
	_ = json.NewDecoder(gResp.Body).Decode(&gRep)
	gResp.Body.Close()
	if gRep.Verdict != "block" || gRep.ExitCode != 2 || len(gRep.BlockingReasons) == 0 {
		t.Fatalf("expected BLOCK verdict, got %+v", gRep)
	}

	// 6. Alert rule create -> test webhook -> delete
	var hookHits int
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hookHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer hookSrv.Close()

	ruleBody, _ := json.Marshal(storage.AlertRule{
		RuleID:        "rule-v13-test",
		Name:          "Test Feishu Rule",
		AppID:         "dev.caliper.demo",
		Platform:      "android",
		Env:           "prod",
		MetricID:      "startup_cold_p90_ms",
		Operator:      "gt",
		Threshold:     1000,
		MinSamples:    20,
		WebhookURL:    hookSrv.URL,
		WebhookFormat: "feishu",
		Enabled:       true,
	})
	rResp, err := http.Post(srv.URL+"/v1/alerts/rules", "application/json", bytes.NewReader(ruleBody))
	if err != nil || rResp.StatusCode != http.StatusOK {
		t.Fatalf("upsert rule failed: err=%v", err)
	}
	rResp.Body.Close()

	testHookResp, err := http.Post(srv.URL+"/v1/alerts/rules/rule-v13-test/test", "application/json", nil)
	if err != nil || testHookResp.StatusCode != http.StatusOK {
		t.Fatalf("test webhook failed: err=%v status=%v", err, testHookResp.StatusCode)
	}
	testHookResp.Body.Close()
	if hookHits != 1 {
		t.Fatalf("expected 1 webhook delivery hit, got %d", hookHits)
	}

	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/alerts/rules/rule-v13-test", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete alert rule failed: err=%v status=%v", err, delResp.StatusCode)
	}
	delResp.Body.Close()
}
