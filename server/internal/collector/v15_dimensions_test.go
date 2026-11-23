package collector

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestV15DimensionsAndDeviceScopedIssues(t *testing.T) {
	st, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer st.Close()

	h := NewHandler(st, st, Config{})
	srv := httptest.NewServer(h)
	defer srv.Close()

	now := time.Now().UTC().UnixMilli()

	postBatch := func(device, osVer, scene, installHash, eventID string) {
		t.Helper()
		crashPayload, _ := json.Marshal(protocol.StabilityPayload{
			Fingerprint:   "fp-v15-cache-npe",
			Title:         "NullPointerException: article cache null",
			ExceptionType: "NullPointerException",
			Fatal:         true,
			Frames: []protocol.StackFrame{
				{Symbol: "com.atlas.feed.FeedController.render", File: "FeedController.kt", Line: 88, InApp: true},
			},
		})
		startupPayload, _ := json.Marshal(protocol.StartupPayload{
			TotalDurationMS: 420.0,
			ColdStart:       true,
			StartAnchor:     "process_start",
			EndAnchor:       "first_frame",
			Result:          "success",
		})
		batch := protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      now,
			Resource: protocol.Resource{
				AppID:       "dev.caliper.demo",
				Platform:    "android",
				Env:         "prod",
				AppVersion:  "1.1.0",
				BuildNumber: "110",
				SDKName:     "caliper-android",
				SDKVersion:  "1.5.0",
				DeviceModel: device,
				OSVersion:   osVer,
				InstallHash: installHash,
			},
			Events: []protocol.Event{
				{
					EventID:         eventID + "-start",
					Kind:            protocol.KindStartup,
					OccurredAtMS:    now,
					ContractVersion: 1,
					Scene:           scene,
					PolicyID:        "default_v1",
					Payload:         startupPayload,
				},
				{
					EventID:         eventID + "-crash",
					Kind:            protocol.KindCrash,
					OccurredAtMS:    now + 500,
					ContractVersion: 1,
					Scene:           scene,
					PolicyID:        "default_v1",
					Payload:         crashPayload,
				},
			},
		}
		raw, _ := json.Marshal(batch)
		resp, err := http.Post(srv.URL+"/v1/ingest", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("ingest %s: %v", eventID, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("ingest status %d", resp.StatusCode)
		}
	}

	postBatch("V2343A", "14", "home_feed", "aaaa1111bbbb2222", "ev-v15-0001")
	postBatch("V2343A", "14", "article_detail", "aaaa1111bbbb3333", "ev-v15-0002")
	postBatch("Pixel 8", "15", "home_feed", "cccc4444dddd5555", "ev-v15-0003")

	// 1. Verify GET /v1/ops/dimensions returns sorted unique values.
	resp, err := http.Get(srv.URL + "/v1/ops/dimensions?platform=android&env=prod")
	if err != nil {
		t.Fatalf("GET /v1/ops/dimensions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dimensions status %d", resp.StatusCode)
	}
	var dims struct {
		Devices    []string `json:"devices"`
		OSVersions []string `json:"os_versions"`
		Scenes     []string `json:"scenes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&dims); err != nil {
		t.Fatalf("decode dimensions: %v", err)
	}
	if !reflect.DeepEqual(dims.Devices, []string{"Pixel 8", "V2343A"}) {
		t.Fatalf("unexpected devices: %#v", dims.Devices)
	}
	if !reflect.DeepEqual(dims.OSVersions, []string{"14", "15"}) {
		t.Fatalf("unexpected os_versions: %#v", dims.OSVersions)
	}
	if !reflect.DeepEqual(dims.Scenes, []string{"article_detail", "home_feed"}) {
		t.Fatalf("unexpected scenes: %#v", dims.Scenes)
	}

	// 2. Verify GET /v1/stability/issues scopes event_count honestly by device_model & os_version.
	queryIssues := func(qs string) []storage.IssueSummary {
		t.Helper()
		r, err := http.Get(srv.URL + "/v1/stability/issues?" + qs)
		if err != nil {
			t.Fatalf("GET issues %s: %v", qs, err)
		}
		defer r.Body.Close()
		var out struct {
			Issues []storage.IssueSummary `json:"issues"`
		}
		if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
			t.Fatalf("decode issues: %v", err)
		}
		return out.Issues
	}

	all := queryIssues("platform=android&env=prod")
	if len(all) != 1 || all[0].EventCount != 3 {
		t.Fatalf("expected 1 issue with event_count=3 overall, got %+v", all)
	}
	vivo := queryIssues("platform=android&env=prod&device_model=V2343A")
	if len(vivo) != 1 || vivo[0].EventCount != 2 {
		t.Fatalf("expected event_count=2 for V2343A, got %+v", vivo)
	}
	pixel := queryIssues("platform=android&env=prod&device_model=Pixel+8&os_version=15")
	if len(pixel) != 1 || pixel[0].EventCount != 1 {
		t.Fatalf("expected event_count=1 for Pixel 8 / OS 15, got %+v", pixel)
	}
	none := queryIssues("platform=android&env=prod&device_model=UnknownPhone")
	if len(none) != 0 {
		t.Fatalf("expected 0 issues for UnknownPhone, got %+v", none)
	}
}
