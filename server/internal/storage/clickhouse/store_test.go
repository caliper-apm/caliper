package clickhouse

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestClickHouseMigrateIngestAndQueries(t *testing.T) {
	var (
		mu       sync.Mutex
		ddls     []string
		inserts  = map[string][]map[string]any{}
		lastSQL  string
		sawAuth  bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-ClickHouse-User") == "caliper" && r.Header.Get("X-ClickHouse-Key") == "ch-pass" {
			sawAuth = true
		}

		queryParam := r.URL.Query().Get("query")
		if strings.HasPrefix(queryParam, "INSERT INTO ") {
			var reader io.Reader = r.Body
			if r.Header.Get("Content-Encoding") == "gzip" {
				gz, err := gzip.NewReader(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				defer gz.Close()
				reader = gz
			}
			raw, _ := io.ReadAll(reader)
			parts := strings.Fields(queryParam)
			table := parts[2]
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line == "" {
					continue
				}
				var row map[string]any
				if err := json.Unmarshal([]byte(line), &row); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				inserts[table] = append(inserts[table], row)
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		body, _ := io.ReadAll(r.Body)
		sqlText := strings.TrimSpace(string(body))
		lastSQL = sqlText

		if strings.HasPrefix(sqlText, "CREATE ") {
			ddls = append(ddls, sqlText)
			w.WriteHeader(http.StatusOK)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(sqlText, "caliper_startup_events"):
			_, _ = w.Write([]byte(`{"data":[{"success_count":"150","aborted_count":"2","timeout_count":"1","staged_count":"140","total_buckets":["0","0","0","0","0","100","50","0","0","0","0","0","0","0","0","0","0","0","0"],"native_buckets":["0","0","0","150","0","0","0","0","0","0","0","0","0","0","0","0","0","0","0"],"runtime_buckets":["0","0","0","150","0","0","0","0","0","0","0","0","0","0","0","0","0","0","0"],"first_frame_buckets":["0","0","0","0","150","0","0","0","0","0","0","0","0","0","0","0","0","0","0"],"policy_ids":["pol-prod"]}]}`))
		case strings.Contains(sqlText, "caliper_fluency_events"):
			_, _ = w.Write([]byte(`{"data":[{"scene":"home_feed","algorithm":"android_frame_metrics_deadline","total_windows":"120","valid_windows":"118","zero_frame_windows":"2","total_frames":"7080","jank_frames":"212","severe_jank_frames":"18","stall_window_count":"5","frame_buckets":["6800","200","80","0","0","0","0","0","0","0","0","0","0","0","0","0","0","0","0"],"sample_stall_stacks":[["FeedImageDecoder.decodeSync"]],"policy_ids":["pol-prod"]}]}`))
		case strings.Contains(sqlText, "caliper_session_timeline"):
			_, _ = w.Write([]byte(`{"data":[{"event_id":"evt-000001","occurred_at_ms":"1763540000000","app_id":"dev.caliper.demo","platform":"android","env":"prod","app_version":"1.1.0","build_number":"110","adapter":"native","install_hash":"0123456789abcdef","user_hash":"fedcba9876543210","kind":"startup","scene":"home_feed","summary":"cold_start=640ms result=success"}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	store, err := New(Config{
		Endpoint: srv.URL,
		Database: "caliper_prod",
		Username: "caliper",
		Password: "ch-pass",
	})
	if err != nil {
		t.Fatalf("new clickhouse store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mu.Lock()
	ddlCount := len(ddls)
	mu.Unlock()
	if ddlCount != 10 { // 1 CREATE DATABASE + 9 CREATE TABLE
		t.Fatalf("expected 10 DDL statements, got %d", ddlCount)
	}

	batch := &protocol.BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      1763540000000,
		Resource: protocol.Resource{
			AppID:       "dev.caliper.demo",
			Platform:    "android",
			Env:         "prod",
			AppVersion:  "1.1.0",
			BuildNumber: "110",
			Adapter:     "native",
			SDKName:     "caliper-android",
			SDKVersion:  "0.5.0",
			InstallHash: "0123456789abcdef",
			UserHash:    "fedcba9876543210",
		},
		Events: []protocol.Event{
			{
				EventID:         "evt-ch-startup-1",
				Kind:            protocol.KindStartup,
				OccurredAtMS:    1763540000000,
				ContractVersion: 1,
				Scene:           "home_feed",
				Payload:         json.RawMessage(`{"total_duration_ms":640,"native_init_ms":120,"first_frame_ms":220,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`),
			},
			{
				EventID:         "evt-ch-fluency-1",
				Kind:            protocol.KindFluencyWindow,
				OccurredAtMS:    1763540001000,
				ContractVersion: 1,
				Scene:           "home_feed",
				Payload:         json.RawMessage(`{"algorithm":"android_frame_metrics_deadline","window_duration_ms":10000,"frame_count":600,"jank_frame_count":24}`),
			},
		},
	}

	res, err := store.IngestBatch(ctx, batch)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if res.Accepted != 2 || res.Rejected != 0 {
		t.Fatalf("unexpected ingest result: %+v", res)
	}
	mu.Lock()
	if !sawAuth {
		t.Fatalf("expected clickhouse auth headers")
	}
	if len(inserts["caliper_prod.caliper_startup_events"]) != 1 || len(inserts["caliper_prod.caliper_session_timeline"]) != 2 {
		t.Fatalf("unexpected inserted tables: %+v", inserts)
	}
	mu.Unlock()

	st, err := store.QueryStartup(ctx, storage.QueryFilter{Platform: "android", AppVersion: "1.1.0"})
	if err != nil {
		t.Fatalf("query startup: %v", err)
	}
	if st.SuccessCount != 150 || st.TotalHist[5] != 100 || st.TotalHist[6] != 50 {
		t.Fatalf("unexpected startup aggregate: %+v", st)
	}

	fl, err := store.QueryFluency(ctx, storage.QueryFilter{Platform: "android", Scene: "home' OR '1'='1"})
	if err != nil {
		t.Fatalf("query fluency: %v", err)
	}
	if len(fl) != 1 || fl[0].ValidWindows != 118 || fl[0].FrameDurationHist[0] != 6800 {
		t.Fatalf("unexpected fluency aggregate: %+v", fl)
	}
	mu.Lock()
	if !strings.Contains(lastSQL, `scene = 'home\' OR \'1\'=\'1'`) {
		t.Fatalf("SQL literal was not safely escaped: %s", lastSQL)
	}
	mu.Unlock()

	tl, err := store.QuerySessionTimeline(ctx, storage.SessionLookupFilter{UserHash: "fedcba9876543210"})
	if err != nil {
		t.Fatalf("query session timeline: %v", err)
	}
	if len(tl) != 1 || tl[0].UserHash != "fedcba9876543210" {
		t.Fatalf("unexpected timeline: %+v", tl)
	}
}

func TestClickHouseDeviceOSFiltersAndDimensions(t *testing.T) {
	where := buildWhereClause(storage.QueryFilter{
		Platform:    "android",
		DeviceModel: "Redmi Note 13",
		OSVersion:   "Android 12",
	}, false, false)
	if !strings.Contains(where, "device_model = 'Redmi Note 13'") || !strings.Contains(where, "os_version = 'Android 12'") {
		t.Fatalf("expected device_model and os_version in WHERE, got: %s", where)
	}

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotQuery = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"val":"Galaxy A54"},{"val":"Pixel 8"},{"val":"Redmi Note 13"}]}`))
	}))
	defer srv.Close()

	store, err := New(Config{Endpoint: srv.URL, Database: "caliper"})
	if err != nil {
		t.Fatalf("new clickhouse store: %v", err)
	}
	vals := store.ListDimensionValues(context.Background(), storage.QueryFilter{Platform: "android"}, "device")
	if len(vals) != 3 || vals[0] != "Galaxy A54" || vals[2] != "Redmi Note 13" {
		t.Fatalf("unexpected dimension values: %+v", vals)
	}
	if !strings.Contains(gotQuery, "SELECT DISTINCT device_model AS val") {
		t.Fatalf("unexpected dimension query: %s", gotQuery)
	}
}
