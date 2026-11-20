package collector

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/profiler"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestV1DiagnosticsFlamegraphHeapRemoteLogAndOTLP(t *testing.T) {
	store, err := storage.OpenEmbedded(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	h := NewHandler(store, store, Config{})
	ts := httptest.NewServer(h)
	defer ts.Close()

	// 1. Ingest CPU profile + Heap snapshots for v1.0.0 and v1.1.0
	ingestJSON := func(ver string, retainedKiB float64, profID string) {
		payload := `{
		  "schema_version": 1,
		  "sent_at_ms": 1763540000000,
		  "resource": {
		    "app_id": "dev.caliper.demo", "platform": "android", "env": "prod",
		    "app_version": "` + ver + `", "build_number": "110", "adapter": "react-native",
		    "sdk_name": "caliper-android", "sdk_version": "1.0.0",
		    "install_hash": "1111222233334444", "user_hash": "0000111122223333"
		  },
		  "events": [
		    {
		      "event_id": "` + profID + `",
		      "kind": "cpu_profile",
		      "occurred_at_ms": 1763540000000,
		      "contract_version": 1,
		      "scene": "streaming_reader",
		      "payload": {
		        "runtime": "hermes",
		        "thread_name": "mqt_js",
		        "sample_interval_ms": 10,
		        "duration_ms": 5000,
		        "total_samples": 100,
		        "stacks": [
		          {"frames": ["(root)", "renderTokenChunk", "JSON.parse"], "samples": 80},
		          {"frames": ["(root)", "flushLayout"], "samples": 20}
		        ]
		      }
		    },
		    {
		      "event_id": "heap-` + ver + `-0001",
		      "kind": "heap_snapshot",
		      "occurred_at_ms": 1763540001000,
		      "contract_version": 1,
		      "scene": "home_feed",
		      "payload": {
		        "trigger_reason": "high_watermark",
		        "memory_kind": "android_pss_kib",
		        "total_allocated_kib": 409600,
		        "total_retained_kib": ` + jsonNumber(retainedKiB) + `,
		        "memory_limit_kib": 524288,
		        "dominators": [
		          {"class_name": "BitmapImageCache", "instance_count": 400, "retained_kib": ` + jsonNumber(retainedKiB*0.6) + `}
		        ]
		      }
		    }
		  ]
		}`
		resp, err := http.Post(ts.URL+"/v1/ingest", "application/json", strings.NewReader(payload))
		if err != nil || resp.StatusCode != http.StatusAccepted {
			t.Fatalf("ingest %s failed: status=%v err=%v", ver, resp.StatusCode, err)
		}
		resp.Body.Close()
	}
	ingestJSON("1.0.0", 160*1024, "prof-v100-001")
	ingestJSON("1.1.0", 360*1024, "prof-v110-001")

	// 2. Query CPU Flamegraph tree
	respFG, err := http.Get(ts.URL + "/v1/diagnostics/profiles/prof-v110-001/flamegraph")
	if err != nil || respFG.StatusCode != http.StatusOK {
		t.Fatalf("get flamegraph failed: status=%v err=%v", respFG.StatusCode, err)
	}
	var fg profiler.FlamegraphReport
	if err := json.NewDecoder(respFG.Body).Decode(&fg); err != nil {
		t.Fatalf("decode flamegraph: %v", err)
	}
	respFG.Body.Close()
	if fg.TotalSamples != 100 || fg.Runtime != "hermes" || len(fg.Root.Children) != 1 {
		t.Fatalf("unexpected flamegraph: %+v", fg)
	}

	// 3. Compare Trimmed Heap Snapshots (detects +120 MiB BitmapImageCache leak suspect)
	respHeap, err := http.Post(ts.URL+"/v1/diagnostics/heap/compare", "application/json",
		strings.NewReader(`{"platform":"android","baseline_version":"1.0.0","candidate_version":"1.1.0"}`))
	if err != nil || respHeap.StatusCode != http.StatusOK {
		t.Fatalf("heap compare failed: status=%v err=%v", respHeap.StatusCode, err)
	}
	var heapDiff profiler.HeapCompareReport
	if err := json.NewDecoder(respHeap.Body).Decode(&heapDiff); err != nil {
		t.Fatalf("decode heap diff: %v", err)
	}
	respHeap.Body.Close()
	if len(heapDiff.DominatorDiffs) != 1 || !heapDiff.DominatorDiffs[0].Suspect {
		t.Fatalf("expected BitmapImageCache suspect in heap diff: %+v", heapDiff)
	}

	// 4. Remote Log Retrieval full workflow: create (audited) -> SDK poll -> gzip upload -> audited read
	respTask, err := http.Post(ts.URL+"/v1/diagnostics/log-tasks", "application/json",
		strings.NewReader(`{"app_id":"dev.caliper.demo","platform":"android","target_type":"user_hash","target_hash":"0000111122223333","reason":"INC-999-remote-diag"}`))
	if err != nil || respTask.StatusCode != http.StatusCreated {
		t.Fatalf("create remote log task failed: status=%v err=%v", respTask.StatusCode, err)
	}
	var created storage.RemoteLogTask
	_ = json.NewDecoder(respTask.Body).Decode(&created)
	respTask.Body.Close()

	// SDK polls pending tasks
	respPoll, err := http.Get(ts.URL + "/v1/diagnostics/log-tasks/pending?platform=android&user_hash=0000111122223333")
	if err != nil || respPoll.StatusCode != http.StatusOK {
		t.Fatalf("poll pending failed: %v", err)
	}
	var pollOut struct {
		Tasks []storage.RemoteLogTask `json:"tasks"`
	}
	_ = json.NewDecoder(respPoll.Body).Decode(&pollOut)
	respPoll.Body.Close()
	if len(pollOut.Tasks) != 1 || pollOut.Tasks[0].TaskID != created.TaskID {
		t.Fatalf("expected 1 pending task matching %s, got %+v", created.TaskID, pollOut.Tasks)
	}

	// SDK uploads gzipped diagnostic ring log
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write([]byte("19:30:01 INFO scene=home_feed decode_ms=142\n19:30:02 WARN memory_high_watermark pss=368MiB\n"))
	_ = gw.Close()
	reqUp, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/diagnostics/log-tasks/"+created.TaskID+"/upload", &gz)
	reqUp.Header.Set("Content-Encoding", "gzip")
	respUp, err := http.DefaultClient.Do(reqUp)
	if err != nil || respUp.StatusCode != http.StatusOK {
		t.Fatalf("upload remote log failed: status=%v err=%v", respUp.StatusCode, err)
	}
	respUp.Body.Close()

	// Operator reads content with audit reason
	respContent, err := http.Get(ts.URL + "/v1/diagnostics/log-tasks/" + created.TaskID + "/content?reason=INC-999-read")
	if err != nil || respContent.StatusCode != http.StatusOK {
		t.Fatalf("read remote log failed: status=%v err=%v", respContent.StatusCode, err)
	}
	var contentOut struct {
		Content string `json:"content"`
	}
	_ = json.NewDecoder(respContent.Body).Decode(&contentOut)
	respContent.Body.Close()
	if !strings.Contains(contentOut.Content, "memory_high_watermark") {
		t.Fatalf("unexpected remote log content: %q", contentOut.Content)
	}

	// 5. OTLP/HTTP JSON traces ingest + OTLP resourceMetrics export
	otlpBody := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"dev.caliper.demo"}},{"key":"os.name","value":{"stringValue":"ios"}},{"key":"service.version","value":{"stringValue":"1.1.0"}}]},"scopeSpans":[{"spans":[{"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7","name":"app.startup.cold","startTimeUnixNano":"1763540000000000000","endTimeUnixNano":"1763540000800000000"}]}]}]}`
	respOTLP, err := http.Post(ts.URL+"/v1/otlp/v1/traces", "application/json", strings.NewReader(otlpBody))
	if err != nil || respOTLP.StatusCode != http.StatusOK {
		t.Fatalf("otlp traces ingest failed: status=%v err=%v", respOTLP.StatusCode, err)
	}
	respOTLP.Body.Close()

	respExp, err := http.Get(ts.URL + "/v1/otlp/export?platform=ios&app_version=1.1.0")
	if err != nil || respExp.StatusCode != http.StatusOK {
		t.Fatalf("otlp export failed: status=%v err=%v", respExp.StatusCode, err)
	}
	respExp.Body.Close()
}

func jsonNumber(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
