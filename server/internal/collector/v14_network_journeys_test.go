package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestV14NetworkSamplesStatusCodesAndCustomJourneys(t *testing.T) {
	st, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer st.Close()

	h := NewHandler(st, st, Config{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	now := time.Now().UTC().UnixMilli()
	evs := make([]protocol.Event, 0, 120)

	// 40 network transport events (mix of 200 fast, 502 error, timeout slow)
	for i := 0; i < 40; i++ {
		dns, tcp, tls, ttfb, body := 15.0, 20.0, 35.0, 140.0, 40.0
		total := 250.0
		res := "success"
		code := 200
		if i%5 == 0 {
			res = "failed"
			code = 502
			total = 1350.0
			ttfb = 1100.0
		} else if i%7 == 0 {
			res = "timeout"
			code = 0
			total = 3200.0
		}
		p, _ := json.Marshal(protocol.NetworkTransportPayload{
			Operation:        "/api/v1/stream/article",
			Method:           "POST",
			StatusCode:       code,
			Result:           res,
			DNSMS:            &dns,
			TCPMS:            &tcp,
			TLSMS:            &tls,
			TTFBMS:           &ttfb,
			BodyMS:           &body,
			TotalMS:          total,
			ConnectionReused: i%2 == 0,
			Protocol:         "h2",
			NetworkType:      "5g",
		})
		evs = append(evs, protocol.Event{
			EventID:         fmt.Sprintf("net-sample-%04d", i),
			Kind:            protocol.KindNetworkStage,
			OccurredAtMS:    now + int64(i*1000),
			ContractVersion: 1,
			Scene:           "streaming_reader",
			PolicyID:        "default_v1",
			Payload:         p,
		})
	}

	// 30 custom_scene business journey traces with ordered stages
	for i := 0; i < 30; i++ {
		dur := 420.0
		status := "success"
		errCode := ""
		if i%10 == 0 {
			status = "failure"
			errCode = "ERR_PARSE"
			dur = 980.0
		}
		p, _ := json.Marshal(protocol.CustomScenePayload{
			JourneyName: "article_open_to_readable",
			Status:      status,
			DurationMS:  &dur,
			ErrorCode:   errCode,
			Stages: []protocol.SceneStage{
				{Name: "cache_lookup", DurationMS: 30},
				{Name: "api_fetch", DurationMS: 220},
				{Name: "layout_render", DurationMS: 170},
			},
		})
		evs = append(evs, protocol.Event{
			EventID:         fmt.Sprintf("jrn-trace-%04d", i),
			Kind:            protocol.KindCustomScene,
			OccurredAtMS:    now + int64(i*1000),
			ContractVersion: 1,
			Scene:           "streaming_reader",
			PolicyID:        "default_v1",
			Payload:         p,
		})
	}

	rawEnv, _ := json.Marshal(protocol.BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      now,
		Resource: protocol.Resource{
			AppID:       "dev.caliper.demo",
			Platform:    "android",
			Env:         "prod",
			AppVersion:  "1.1.0",
			BuildNumber: "110",
			SDKName:     "caliper-android",
			SDKVersion:  "1.4.0",
			InstallHash: "1122334455667788",
			UserHash:    "0000111122223333",
			DeviceModel: "Redmi Note 13",
			OSVersion:   "Android 12",
		},
		Events: evs,
	})
	resp, err := http.Post(srv.URL+"/v1/ingest", "application/json", bytes.NewReader(rawEnv))
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest failed: err=%v status=%v", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 1. Network report includes HTTP status_codes distribution
	netResp, err := http.Get(srv.URL + "/v1/analytics/network?platform=android")
	if err != nil || netResp.StatusCode != http.StatusOK {
		t.Fatalf("network report failed: %v", err)
	}
	var netRep analytics.NetworkReport
	_ = json.NewDecoder(netResp.Body).Decode(&netRep)
	netResp.Body.Close()
	if netRep.StatusCodes["2xx"] == 0 || netRep.StatusCodes["5xx"] == 0 || netRep.StatusCodes["timeout"] == 0 {
		t.Fatalf("expected 2xx, 5xx, and timeout status codes, got %+v", netRep.StatusCodes)
	}

	// 2. Network slow/error samples endpoint filters properly
	smpResp, err := http.Get(srv.URL + "/v1/analytics/network/samples?platform=android&operation=/api/v1/stream/article&only_issues=true")
	if err != nil || smpResp.StatusCode != http.StatusOK {
		t.Fatalf("network samples failed: %v", err)
	}
	var smpBody struct {
		Samples []storage.NetworkTransportSample `json:"samples"`
	}
	_ = json.NewDecoder(smpResp.Body).Decode(&smpBody)
	smpResp.Body.Close()
	if len(smpBody.Samples) == 0 {
		t.Fatalf("expected non-empty slow/failed network samples")
	}
	for _, s := range smpBody.Samples {
		if s.Result == "success" && s.StatusCode < 400 && s.TotalMS < 800 {
			t.Fatalf("only_issues=true leaked healthy fast sample: %+v", s)
		}
		if s.UserHash != "0000111122223333" || s.DeviceModel != "Redmi Note 13" {
			t.Fatalf("missing user/device attribution on sample: %+v", s)
		}
	}

	// 3. Custom business journey traces endpoint returns stage waterfall & success rate
	jResp, err := http.Get(srv.URL + "/v1/analytics/journeys?platform=android")
	if err != nil || jResp.StatusCode != http.StatusOK {
		t.Fatalf("journeys failed: %v", err)
	}
	var jBody struct {
		Journeys []analytics.CustomJourneyReport `json:"journeys"`
	}
	_ = json.NewDecoder(jResp.Body).Decode(&jBody)
	jResp.Body.Close()
	if len(jBody.Journeys) != 1 || jBody.Journeys[0].JourneyName != "article_open_to_readable" {
		t.Fatalf("unexpected journeys: %+v", jBody.Journeys)
	}
	j := jBody.Journeys[0]
	if j.TotalCount != 30 || j.FailureCount != 3 || len(j.Stages) != 3 || j.ErrorCodes["ERR_PARSE"] != 3 {
		t.Fatalf("unexpected journey details: %+v", j)
	}
	if j.DurationP90.Value == nil || j.Stages[1].Name != "api_fetch" || j.Stages[1].P90MS.Value == nil {
		t.Fatalf("expected valid P90 quantiles for journey and stages: %+v", j)
	}
}
