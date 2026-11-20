package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

type releaseProfile struct {
	version       string
	build         string
	gitSHA        string
	startupBaseMS float64
	jankRate      float64
	cpuBasePct    float64
	memBaseMiB    float64
	apiFailRate   float64
	apiBaseMS     float64
	crashRate     float64
}

func main() {
	target := flag.String("target", "http://127.0.0.1:8080", "Caliper server base URL")
	totalEvents := flag.Int("events", 100000, "Total telemetry events to replay via POST /v1/ingest")
	apiKey := flag.String("api-key", "", "Optional X-Caliper-Key")
	verify := flag.Bool("verify", true, "Verify cross-version comparison query latency and verdicts after replay")
	flag.Parse()

	profiles := []releaseProfile{
		{version: "1.0.0", build: "100", gitSHA: "1000001abcdef", startupBaseMS: 520, jankRate: 0.015, cpuBasePct: 16, memBaseMiB: 190, apiFailRate: 0.008, apiBaseMS: 180, crashRate: 0.002},
		{version: "1.1.0", build: "110", gitSHA: "1100002abcdef", startupBaseMS: 1680, jankRate: 0.095, cpuBasePct: 38, memBaseMiB: 360, apiFailRate: 0.065, apiBaseMS: 890, crashRate: 0.018},
		{version: "1.2.0", build: "120", gitSHA: "1200003abcdef", startupBaseMS: 490, jankRate: 0.014, cpuBasePct: 15, memBaseMiB: 185, apiFailRate: 0.007, apiBaseMS: 170, crashRate: 0.002},
	}

	rng := rand.New(rand.NewSource(20261119))
	client := &http.Client{Timeout: 30 * time.Second}
	baseTime := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	start := time.Now()
	sentEvents := 0
	batchesSent := 0
	seq := 0

	perGroup := *totalEvents / (len(profiles) * 2)
	if perGroup < 100 {
		perGroup = 100
	}

	type dayWindow struct {
		startDay int // offset from 2026-11-05
		numDays  int
	}
	releaseDays := map[string]dayWindow{
		"1.0.0": {startDay: 0, numDays: 5},  // Nov 05 - Nov 09: Healthy Baseline
		"1.1.0": {startDay: 5, numDays: 5},  // Nov 10 - Nov 14: Regressed Release
		"1.2.0": {startDay: 10, numDays: 4}, // Nov 15 - Nov 18: Hotfix Recovery
	}
	epochStart := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)

	for _, platform := range []string{"android", "ios"} {
		for _, prof := range profiles {
			dw := releaseDays[prof.version]
			var batchEvents []protocol.Event
			flush := func() {
				if len(batchEvents) == 0 {
					return
				}
				adapter := "native"
				if batchesSent%3 == 0 {
					adapter = "react-native"
				}
				installHash := fmt.Sprintf("%016x", rng.Uint64())
				userHash := ""
				if batchesSent%10 == 0 {
					// Deterministic non-PII sample user_hash for testing Audited Session Lookup out of the box.
					userHash = "0000111122223333"
				}
				devicePool := [][2]string{
					{"Pixel 8", "Android 14"},
					{"Galaxy A54", "Android 13"},
					{"Redmi Note 13", "Android 12"},
				}
				if platform == "ios" {
					devicePool = [][2]string{
						{"iPhone 16 Pro", "iOS 18.1"},
						{"iPhone 15 Pro", "iOS 18.0"},
						{"iPhone 13", "iOS 17.5"},
					}
				}
				devChoice := devicePool[batchesSent%len(devicePool)]
				env := protocol.BatchEnvelope{
					SchemaVersion: 1,
					SentAtMS:      baseTime.UnixMilli(),
					Resource: protocol.Resource{
						AppID:       "dev.caliper.demo",
						Platform:    platform,
						Env:         "prod",
						AppVersion:  prof.version,
						BuildNumber: prof.build,
						GitSHA:      prof.gitSHA,
						SymbolID:    fmt.Sprintf("sym-%s-%s-%s", platform, prof.version, prof.build),
						Adapter:     adapter,
						SDKName:     "caliper-" + platform,
						SDKVersion:  "1.1.0",
						InstallHash: installHash,
						UserHash:    userHash,
						DeviceModel: devChoice[0],
						OSVersion:   devChoice[1],
					},
					Events: batchEvents,
				}
				if err := postGzip(client, *target+"/v1/ingest", *apiKey, env); err != nil {
					log.Fatalf("ingest failed on batch %d: %v", batchesSent, err)
				}
				sentEvents += len(batchEvents)
				batchesSent++
				batchEvents = batchEvents[:0]
			}

			for i := 0; i < perGroup; i++ {
				seq++
				dayOffset := dw.startDay + ((i / 6) % dw.numDays)
				ts := epochStart.AddDate(0, 0, dayOffset).Add(time.Duration(rng.Intn(24*60)) * time.Minute).UnixMilli()
				ev := buildSyntheticEvent(rng, seq, platform, prof, ts)
				batchEvents = append(batchEvents, ev)
				if len(batchEvents) >= protocol.MaxBatchEvents {
					flush()
				}
			}
			flush()
			// Also emit real Native/Hermes CPU profiles and trimmed Heap snapshots for Deep Diagnostics.
			diagTS := epochStart.AddDate(0, 0, dw.startDay+dw.numDays-1).Add(12 * time.Hour).UnixMilli()
			batchEvents = append(batchEvents, buildDiagnosticEvents(platform, prof, diagTS)...)
			flush()
		}
	}

	elapsed := time.Since(start)
	log.Printf("Replayed %d events in %d gzipped HTTP batches in %v (%.0f events/sec)",
		sentEvents, batchesSent, elapsed.Round(time.Millisecond), float64(sentEvents)/elapsed.Seconds())

	if !*verify {
		return
	}

	// Verify cross-version regression detection & latency (< 200ms Week 1 gate).
	for _, pair := range [][3]string{
		{"android", "1.0.0", "1.1.0"},
		{"android", "1.1.0", "1.2.0"},
		{"ios", "1.0.0", "1.1.0"},
	} {
		reqBody, _ := json.Marshal(analytics.CompareRequest{
			AppID:            "dev.caliper.demo",
			Platform:         pair[0],
			Env:              "prod",
			BaselineVersion:  pair[1],
			CandidateVersion: pair[2],
		})
		qStart := time.Now()
		resp, err := client.Post(*target+"/v1/analytics/compare", "application/json", bytes.NewReader(reqBody))
		if err != nil {
			log.Fatalf("compare query failed: %v", err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		qDur := time.Since(qStart)
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("compare HTTP %d: %s", resp.StatusCode, string(raw))
		}
		var rep analytics.CompareReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			log.Fatalf("decode compare report: %v", err)
		}
		log.Printf("Compare %s %s -> %s: verdict=%s regressed=%d gated=%d query_latency=%v",
			pair[0], pair[1], pair[2], rep.Verdict, rep.RegressedCount, rep.GatedCount, qDur.Round(time.Microsecond))
	}
}

func buildSyntheticEvent(rng *rand.Rand, seq int, platform string, p releaseProfile, ts int64) protocol.Event {
	id := fmt.Sprintf("replay-%s-%s-%07d", platform, p.version, seq)
	scenes := []string{"home_feed", "streaming_reader", "media_gallery"}
	scene := scenes[seq%len(scenes)]

	switch seq % 6 {
	case 0: // startup
		total := p.startupBaseMS * (0.75 + rng.Float64()*0.6)
		native := total * 0.30
		firstFrame := total * 0.25
		anchor := "application_on_create"
		if platform == "ios" {
			anchor = "process_main"
		}
		payload, _ := json.Marshal(protocol.StartupPayload{
			TotalDurationMS: total,
			NativeInitMS:    &native,
			FirstFrameMS:    &firstFrame,
			ColdStart:       true,
			StartAnchor:     anchor,
			EndAnchor:       "first_content_frame",
			Result:          "success",
		})
		return protocol.Event{EventID: id, Kind: protocol.KindStartup, OccurredAtMS: ts, ContractVersion: 1, PolicyID: "default_v1", Payload: payload}

	case 1: // fluency_window
		algo := "android_frame_metrics_deadline"
		if platform == "ios" {
			algo = "ios_display_link"
		}
		frames := int64(900 + rng.Intn(600))
		jank := int64(float64(frames) * p.jankRate * (0.7 + rng.Float64()*0.6))
		if seq%50 == 0 {
			frames, jank = 0, 0 // verify zero-frame windows remain isolated & never inflate smoothness
		}
		var buckets [contract.HistogramBucketCount]int64
		if frames > 0 {
			buckets[0] = frames - jank
			buckets[2] = jank
		}
		var stall []string
		if p.jankRate > 0.05 && frames > 0 && rng.Float64() < 0.25 {
			stall = []string{
				"com.caliper.demo.feed.FeedImageDecoder.decodeSync(FeedImageDecoder.kt:92)",
				"com.caliper.demo.feed.FeedAdapter.onBindViewHolder(FeedAdapter.kt:148)",
			}
		}
		payload, _ := json.Marshal(protocol.FluencyWindowPayload{
			Algorithm:            algo,
			WindowDurationMS:     30000,
			FrameCount:           frames,
			JankFrameCount:       jank,
			RefreshRateHz:        60,
			FrameDurationBuckets: buckets[:],
			StallStack:           stall,
		})
		return protocol.Event{EventID: id, Kind: protocol.KindFluencyWindow, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}

	case 2: // resource_window
		memKind := "android_pss_kib"
		if platform == "ios" {
			memKind = "ios_phys_footprint_kib"
		}
		cpu := p.cpuBasePct * (0.8 + rng.Float64()*0.5)
		memKiB := p.memBaseMiB * 1024 * (0.85 + rng.Float64()*0.35)
		payload, _ := json.Marshal(protocol.ResourceWindowPayload{
			WindowDurationMS: 30000,
			CPUPct:           cpu,
			MemoryKind:       memKind,
			MemoryPeakKiB:    memKiB,
			MemoryAvgKiB:     memKiB * 0.9,
			SampleCount:      6,
		})
		return protocol.Event{EventID: id, Kind: protocol.KindResourceWindow, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}

	case 3: // network_summary
		calls := int64(20 + rng.Intn(30))
		fails := int64(float64(calls) * p.apiFailRate)
		var buckets [contract.HistogramBucketCount]int64
		var sumMS float64
		for c := int64(0); c < calls; c++ {
			d := p.apiBaseMS * (0.6 + rng.Float64()*0.9)
			buckets[contract.DurationBucketIndex(d)]++
			sumMS += d
		}
		var fc protocol.NetworkFailureCounts
		for fIdx := int64(0); fIdx < fails; fIdx++ {
			r := rng.Float64()
			switch {
			case r < 0.42:
				fc.HTTP++
			case r < 0.66:
				fc.Timeout++
			case r < 0.82:
				fc.Network++
			case r < 0.92:
				fc.Business++
			default:
				fc.Parse++
			}
		}
		payload, _ := json.Marshal(protocol.NetworkSummaryPayload{
			WindowDurationMS: 30000,
			CallCount:        calls,
			FailureCounts:    fc,
			DurationBuckets:  buckets[:],
			DurationSumMS:    sumMS,
		})
		return protocol.Event{EventID: id, Kind: protocol.KindNetworkSummary, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}

	case 4: // network_transport
		ops := []struct {
			op     string
			method string
			scale  float64
		}{
			{"/api/v1/feed", "GET", 1.0},
			{"/api/v1/gallery", "GET", 1.35},
			{"/api/v1/stream/article", "POST", 1.65},
			{"/api/v1/user/profile", "GET", 0.55},
		}
		chosen := ops[(seq/6)%len(ops)]
		dns := 16.0 + rng.Float64()*14.0
		tcp := 22.0 + rng.Float64()*18.0
		tls := 40.0 + rng.Float64()*28.0
		if p.apiBaseMS > 500 {
			tls += 210.0 * chosen.scale
		}
		ttfb := p.apiBaseMS * 0.55 * chosen.scale
		body := p.apiBaseMS * 0.25 * chosen.scale
		total := dns + tcp + tls + ttfb + body
		res := "success"
		status := 200
		if rng.Float64() < p.apiFailRate {
			res = "failed"
			status = 502
		}
		payload, _ := json.Marshal(protocol.NetworkTransportPayload{
			Operation:        chosen.op,
			Method:           chosen.method,
			StatusCode:       status,
			Result:           res,
			DNSMS:            &dns,
			TCPMS:            &tcp,
			TLSMS:            &tls,
			TTFBMS:           &ttfb,
			BodyMS:           &body,
			TotalMS:          total,
			ConnectionReused: rng.Float64() < 0.65,
			Protocol:         "h2",
			NetworkType:      "wifi",
		})
		return protocol.Event{EventID: id, Kind: protocol.KindNetworkStage, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}

	default: // js_longtask or crash/ANR/abnormal_exit
		if rng.Float64() < p.crashRate*3.0 {
			switch (seq / 6) % 3 {
			case 0: // Crash with build-bound obfuscated frames on 1.1.0 for live symbolication!
				frames := []protocol.StackFrame{
					{Module: "AtlasDemo", Symbol: "a.b.c.a", File: "SourceFile", Line: 92, InApp: true},
					{Module: "AtlasDemo", Symbol: "com.caliper.demo.feed.FeedAdapter.onBindViewHolder", File: "FeedAdapter.kt", Line: 148, InApp: true},
				}
				title := "OutOfMemoryError in FeedImageDecoder.decodeSync"
				if platform == "ios" {
					frames = []protocol.StackFrame{
						{Module: "AtlasDemo", Symbol: "0x10004220", Address: "0x10004220", InApp: true},
						{Module: "AtlasDemo", Symbol: "0x10008010", Address: "0x10008010", InApp: true},
					}
					title = "EXC_BAD_ACCESS in ImageDecoder.decodeFrame"
				}
				payload, _ := json.Marshal(protocol.StabilityPayload{
					Fingerprint:   "fp-decoder-crash-" + platform,
					Title:         title,
					Fatal:         true,
					ExceptionType: "OutOfMemoryError",
					ThreadName:    "main",
					Frames:        frames,
					Breadcrumbs: []protocol.Breadcrumb{
						{TimestampMS: ts - 3500, Category: "navigation", Message: "enter home_feed"},
						{TimestampMS: ts - 1200, Category: "network", Message: "GET /api/v1/gallery 200 (1840ms)"},
						{TimestampMS: ts - 250, Category: "memory", Message: "low memory trim level=RUNNING_CRITICAL"},
					},
				})
				return protocol.Event{EventID: id, Kind: protocol.KindCrash, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}
			case 1: // ANR / Watchdog
				payload, _ := json.Marshal(protocol.StabilityPayload{
					Fingerprint:   "fp-main-thread-anr-" + platform,
					Title:         "Main Thread Blocked > 5000ms (StreamingReader JSON parse)",
					Fatal:         false,
					ExceptionType: "ANR",
					ThreadName:    "main",
					Frames: []protocol.StackFrame{
						{Module: "HermesBundle", Symbol: "index.bundle:1:12", InApp: true},
						{Module: "ReactNative", Symbol: "JSIExecutor.callFunctionReturnFlushedQueue", File: "JSIExecutor.cpp", Line: 214, InApp: false},
					},
					Breadcrumbs: []protocol.Breadcrumb{
						{TimestampMS: ts - 6000, Category: "navigation", Message: "open streaming_reader"},
						{TimestampMS: ts - 5100, Category: "js_longtask", Message: "renderTokenChunk blocked 1480ms"},
					},
				})
				return protocol.Event{EventID: id, Kind: protocol.KindANR, OccurredAtMS: ts, ContractVersion: 1, Scene: "streaming_reader", PolicyID: "default_v1", Payload: payload}
			default: // Abnormal Exit (OOM / LMK)
				payload, _ := json.Marshal(protocol.StabilityPayload{
					Fingerprint:       "fp-abnormal-lmk-" + platform,
					Title:             "Abnormal Background/Foreground OOM Termination (LMK)",
					Fatal:             true,
					ExitReason:        "REASON_LOW_MEMORY",
					ProcessImportance: "IMPORTANCE_FOREGROUND",
					Breadcrumbs: []protocol.Breadcrumb{
						{TimestampMS: ts - 15000, Category: "resource", Message: "memory peak crossed 384 MiB"},
					},
				})
				return protocol.Event{EventID: id, Kind: protocol.KindAbnormalExit, OccurredAtMS: ts, ContractVersion: 1, Scene: "media_gallery", PolicyID: "default_v1", Payload: payload}
			}
		}
		cnt := int64(1 + rng.Intn(4))
		blk := float64(cnt) * (65.0 + p.jankRate*900.0)
		payload, _ := json.Marshal(protocol.JSLongTaskPayload{
			WindowDurationMS: 30000,
			LongTaskCount:    cnt,
			TotalBlockingMS:  blk,
			MaxTaskMS:        blk * 0.6,
		})
		return protocol.Event{EventID: id, Kind: protocol.KindJSLongTask, OccurredAtMS: ts, ContractVersion: 1, Scene: "streaming_reader", PolicyID: "default_v1", Payload: payload}
	}
}

func postGzip(client *http.Client, url, apiKey string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	if apiKey != "" {
		req.Header.Set("X-Caliper-Key", apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(msg))
	}
	return nil
}

func buildDiagnosticEvents(platform string, prof releaseProfile, tsMS int64) []protocol.Event {
	regressed := prof.version == "1.1.0"
	memKind := "android_pss_kib"
	hotFrame := "a.b.c.a(SourceFile:92)" // Retraces via atlas-android-1.1.0-110.r8.txt -> FeedImageDecoder.decodeSync
	cacheClass := "BitmapImageCache"
	if platform == "ios" {
		memKind = "ios_phys_footprint_kib"
		hotFrame = "0x10004220" // Resolves via atlas-ios-1.1.0-110.dsym.json -> ImageDecoder.decodeFrame(_:)
		cacheClass = "UIImageDiskCache"
	}

	hotSamples := int64(18)
	if regressed {
		hotSamples = 72
	}
	nativePayload, _ := json.Marshal(protocol.CPUProfilePayload{
		Runtime:          "native",
		ThreadName:       "main",
		SampleIntervalMS: 10,
		DurationMS:       5000,
		TotalSamples:     100,
		Stacks: []protocol.ProfileStackSample{
			{Frames: []string{"MainRunLoop", "HomeFeedController.renderVisibleCells", hotFrame}, Samples: hotSamples},
			{Frames: []string{"MainRunLoop", "HomeFeedController.renderVisibleCells", "LayoutEngine.measure"}, Samples: 100 - hotSamples - 10},
			{Frames: []string{"MainRunLoop", "NetworkClient.dispatch"}, Samples: 10},
		},
	})

	hermesHot := int64(20)
	if regressed {
		hermesHot = 68
	}
	hermesPayload, _ := json.Marshal(protocol.CPUProfilePayload{
		Runtime:          "hermes",
		ThreadName:       "mqt_js",
		SampleIntervalMS: 10,
		DurationMS:       5000,
		TotalSamples:     100,
		Stacks: []protocol.ProfileStackSample{
			{Frames: []string{"(root)", "index.bundle:1:12", "MarkdownParser.parseSync"}, Samples: hermesHot},
			{Frames: []string{"(root)", "ReactReconciler.commitMutationEffects"}, Samples: 100 - hermesHot},
		},
	})

	cacheKiB := 42.0 * 1024
	totalRetKiB := 175.0 * 1024
	cacheInst := int64(140)
	trigger := "manual"
	if regressed {
		cacheKiB = 218.0 * 1024
		totalRetKiB = 365.0 * 1024
		cacheInst = 920
		trigger = "oom_warning"
	}
	heapPayload, _ := json.Marshal(protocol.HeapSnapshotPayload{
		TriggerReason:     trigger,
		MemoryKind:        memKind,
		TotalAllocatedKiB: totalRetKiB * 1.15,
		TotalRetainedKiB:  totalRetKiB,
		MemoryLimitKiB:    512 * 1024,
		Dominators: []protocol.HeapDominator{
			{ClassName: cacheClass, InstanceCount: cacheInst, RetainedKiB: cacheKiB, RetainerPath: []string{"AtlasApplication", "FeedImagePipeline", cacheClass}},
			{ClassName: "StreamingTokenBuffer", InstanceCount: 240, RetainedKiB: 28 * 1024, RetainerPath: []string{"StreamingReaderController", "StreamingTokenBuffer"}},
			{ClassName: "SQLitePageCache", InstanceCount: 64, RetainedKiB: 16 * 1024, RetainerPath: []string{"LocalArticleStore", "SQLitePageCache"}},
		},
	})

	return []protocol.Event{
		{
			EventID:         fmt.Sprintf("prof-nat-%s-%s", platform, prof.version),
			Kind:            protocol.KindCPUProfile,
			OccurredAtMS:    tsMS,
			ContractVersion: 1,
			Scene:           "home_feed",
			Payload:         nativePayload,
		},
		{
			EventID:         fmt.Sprintf("prof-js-%s-%s", platform, prof.version),
			Kind:            protocol.KindCPUProfile,
			OccurredAtMS:    tsMS + 1000,
			ContractVersion: 1,
			Scene:           "streaming_reader",
			Payload:         hermesPayload,
		},
		{
			EventID:         fmt.Sprintf("heap-snap-%s-%s", platform, prof.version),
			Kind:            protocol.KindHeapSnapshot,
			OccurredAtMS:    tsMS + 2000,
			ContractVersion: 1,
			Scene:           "home_feed",
			Payload:         heapPayload,
		},
	}
}
