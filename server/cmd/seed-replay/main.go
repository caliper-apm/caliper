package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
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
	startupMedMS  float64 // log-normal median cold start duration
	jankRate      float64 // baseline frame jank ratio
	cpuBasePct    float64 // mean process CPU %
	memBaseMiB    float64 // median peak resident memory MiB
	apiFailRate   float64 // logical API failure ratio
	apiMedMS      float64 // log-normal median API duration
	crashRate     float64 // per-slot probability of crash/ANR/abnormal_exit
}

type deviceTier struct {
	model        string
	osVersion    string
	weight       float64 // daily traffic share (sums to 1.0)
	startupScale float64
	jankScale    float64
	memScale     float64
	crashScale   float64
	demoUser     string
}

func devicePoolFor(platform string) []deviceTier {
	if platform == "ios" {
		return []deviceTier{
			{model: "iPhone 16 Pro", osVersion: "iOS 18.1", weight: 0.30, startupScale: 0.82, jankScale: 0.72, memScale: 0.90, crashScale: 0.65, demoUser: "1111222233334444"},
			{model: "iPhone 15 Pro", osVersion: "iOS 18.0", weight: 0.46, startupScale: 1.00, jankScale: 1.00, memScale: 1.00, crashScale: 1.00, demoUser: "0000111122223333"},
			{model: "iPhone 13", osVersion: "iOS 17.5", weight: 0.24, startupScale: 1.26, jankScale: 1.38, memScale: 1.14, crashScale: 1.45, demoUser: "2222333344445555"},
		}
	}
	return []deviceTier{
		{model: "Pixel 8", osVersion: "Android 14", weight: 0.28, startupScale: 0.80, jankScale: 0.74, memScale: 0.89, crashScale: 0.68, demoUser: "1111222233334444"},
		{model: "Galaxy A54", osVersion: "Android 13", weight: 0.48, startupScale: 1.00, jankScale: 1.00, memScale: 1.00, crashScale: 1.00, demoUser: "0000111122223333"},
		{model: "Redmi Note 13", osVersion: "Android 12", weight: 0.24, startupScale: 1.32, jankScale: 1.42, memScale: 1.16, crashScale: 1.50, demoUser: "2222333344445555"},
	}
}

func profilesFor(platform string) []releaseProfile {
	if platform == "ios" {
		return []releaseProfile{
			{version: "1.0.0", build: "100", gitSHA: "1000001abcdef", startupMedMS: 470, jankRate: 0.0110, cpuBasePct: 13.5, memBaseMiB: 185, apiFailRate: 0.0055, apiMedMS: 175, crashRate: 0.0048},
			{version: "1.1.0", build: "110", gitSHA: "1100002abcdef", startupMedMS: 640, jankRate: 0.0265, cpuBasePct: 19.8, memBaseMiB: 242, apiFailRate: 0.0195, apiMedMS: 285, crashRate: 0.0145},
			{version: "1.2.0", build: "120", gitSHA: "1200003abcdef", startupMedMS: 445, jankRate: 0.0102, cpuBasePct: 12.8, memBaseMiB: 178, apiFailRate: 0.0048, apiMedMS: 162, crashRate: 0.0040},
		}
	}
	return []releaseProfile{
		{version: "1.0.0", build: "100", gitSHA: "1000001abcdef", startupMedMS: 630, jankRate: 0.0165, cpuBasePct: 16.5, memBaseMiB: 212, apiFailRate: 0.0065, apiMedMS: 195, crashRate: 0.0058},
		{version: "1.1.0", build: "110", gitSHA: "1100002abcdef", startupMedMS: 835, jankRate: 0.0355, cpuBasePct: 23.5, memBaseMiB: 268, apiFailRate: 0.0215, apiMedMS: 310, crashRate: 0.0175},
		{version: "1.2.0", build: "120", gitSHA: "1200003abcdef", startupMedMS: 595, jankRate: 0.0150, cpuBasePct: 15.5, memBaseMiB: 204, apiFailRate: 0.0055, apiMedMS: 180, crashRate: 0.0046},
	}
}

// Realistic 14-day canary rollout shares [v1.0.0, v1.1.0, v1.2.0] per day:
// Days 0..3:  v1.0.0 healthy steady baseline
// Days 4..8:  v1.1.0 canary ramp-up (16% -> 44% -> 72% -> 84% -> 80%) revealing statistically significant regression
// Days 9..13: v1.2.0 hotfix rollout (32% -> 62% -> 80% -> 88% -> 93%) driving smooth recovery
var canaryMix14d = [14][3]float64{
	{1.00, 0.00, 0.00}, // D0
	{1.00, 0.00, 0.00}, // D1
	{1.00, 0.00, 0.00}, // D2
	{1.00, 0.00, 0.00}, // D3
	{0.84, 0.16, 0.00}, // D4: v1.1.0 16% canary
	{0.56, 0.44, 0.00}, // D5: v1.1.0 44% rollout
	{0.28, 0.72, 0.00}, // D6: v1.1.0 becomes dominant (marker)
	{0.16, 0.84, 0.00}, // D7: v1.1.0 peak exposure
	{0.14, 0.80, 0.06}, // D8: v1.2.0 6% hotfix canary
	{0.10, 0.58, 0.32}, // D9: v1.2.0 32% ramp
	{0.06, 0.32, 0.62}, // D10: v1.2.0 becomes dominant (marker)
	{0.04, 0.16, 0.80}, // D11: v1.2.0 80%
	{0.03, 0.09, 0.88}, // D12: v1.2.0 88%
	{0.02, 0.05, 0.93}, // D13: v1.2.0 93% steady state
}

// Gentle natural daily drift (weekday/weekend traffic & network micro-variation, +-3.5%).
func dailyNaturalFactor(dayUTC time.Time, dayIdx int) float64 {
	wave := 1.0 + 0.022*math.Sin(float64(dayIdx)*1.15)
	if dayUTC.Weekday() == time.Saturday || dayUTC.Weekday() == time.Sunday {
		wave *= 1.028 // slight weekend peak traffic / mobile network overhead
	}
	return wave
}

// logNormal samples a right-skewed positive value typical of mobile latency & memory distributions.
func logNormal(rng *rand.Rand, median, sigma, minVal, maxVal float64) float64 {
	v := median * math.Exp(rng.NormFloat64()*sigma)
	if v < minVal {
		return minVal
	}
	if v > maxVal {
		return maxVal
	}
	return v
}

func randomHex(rng *rand.Rand, byteLen int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, byteLen*2)
	allZero := true
	for i := 0; i < byteLen; i++ {
		v := byte(rng.Intn(256))
		if v != 0 {
			allZero = false
		}
		b[i*2] = hexChars[v>>4]
		b[i*2+1] = hexChars[v&0x0F]
	}
	if allZero {
		b[len(b)-1] = '1'
	}
	return string(b)
}

func main() {
	target := flag.String("target", "http://127.0.0.1:8080", "Caliper server base URL")
	totalEvents := flag.Int("events", 30000, "Total telemetry events to replay via POST /v1/ingest")
	apiKey := flag.String("api-key", "", "Optional X-Caliper-Key")
	verify := flag.Bool("verify", true, "Verify cross-version comparison query latency and verdicts after replay")
	flag.Parse()

	rng := rand.New(rand.NewSource(20261124))
	client := &http.Client{Timeout: 30 * time.Second}

	// Dynamically anchor the 14-day window to end on today UTC (zero artificial date gap).
	nowUTC := time.Now().UTC()
	todayUTC := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	epochStart := todayUTC.AddDate(0, 0, -(len(canaryMix14d) - 1))

	start := time.Now()
	sentEvents := 0
	batchesSent := 0
	seq := 0

	perPlatformDay := *totalEvents / (2 * len(canaryMix14d))
	if perPlatformDay < 120 {
		perPlatformDay = 120
	}

	// Replay chronologically day-by-day across both platforms so recent network samples &
	// timelines naturally contain interleaved Android and iOS traffic just like production.
	for dayIdx := 0; dayIdx < len(canaryMix14d); dayIdx++ {
		dayDate := epochStart.AddDate(0, 0, dayIdx)
		dayFactor := dailyNaturalFactor(dayDate, dayIdx)
		mix := canaryMix14d[dayIdx]

		for _, platform := range []string{"android", "ios"} {
			pool := devicePoolFor(platform)
			profiles := profilesFor(platform)

			for profIdx, prof := range profiles {
				share := mix[profIdx]
				if share <= 0 {
					continue
				}
				profEvents := int(math.Round(float64(perPlatformDay) * share))
				if profEvents < 6 {
					continue
				}

				for tierIdx, tier := range pool {
					tierEvents := int(math.Round(float64(profEvents) * tier.weight))
					if tierEvents < 3 {
						continue
					}

					// Split ~72% Native / ~28% React Native consistently on every day & tier.
					nativeCount := int(math.Round(float64(tierEvents) * 0.72))
					rnCount := tierEvents - nativeCount

					for _, adapterSpec := range []struct {
						name  string
						count int
					}{
						{"native", nativeCount},
						{"react-native", rnCount},
					} {
						if adapterSpec.count <= 0 {
							continue
						}
						var batchEvents []protocol.Event
						flush := func() {
							if len(batchEvents) == 0 {
								return
							}
							installHash := fmt.Sprintf("%016x", rng.Uint64())
							userHash := ""
							if batchesSent%4 == 0 {
								userHash = tier.demoUser
							}
							env := protocol.BatchEnvelope{
								SchemaVersion: 1,
								SentAtMS:      nowUTC.UnixMilli(),
								Resource: protocol.Resource{
									AppID:       "dev.caliper.demo",
									Platform:    platform,
									Env:         "prod",
									AppVersion:  prof.version,
									BuildNumber: prof.build,
									GitSHA:      prof.gitSHA,
									SymbolID:    fmt.Sprintf("sym-%s-%s-%s", platform, prof.version, prof.build),
									Adapter:     adapterSpec.name,
									SDKName:     "caliper-" + platform,
									SDKVersion:  "1.9.0",
									InstallHash: installHash,
									UserHash:    userHash,
									DeviceModel: tier.model,
									OSVersion:   tier.osVersion,
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

						for i := 0; i < adapterSpec.count; i++ {
							seq++
							// Spread smoothly across 24 hours.
							minuteOfDay := rng.Intn(24 * 60)
							ts := dayDate.Add(time.Duration(minuteOfDay)*time.Minute + time.Duration(rng.Intn(60))*time.Second).UnixMilli()
							if ts > nowUTC.UnixMilli() {
								ts = nowUTC.UnixMilli() - int64(rng.Intn(600_000))
							}
							ev := buildSyntheticEvent(rng, seq, platform, prof, tier, dayFactor, ts)
							batchEvents = append(batchEvents, ev)
							if len(batchEvents) >= protocol.MaxBatchEvents {
								flush()
							}
						}
						flush()
						_ = tierIdx
					}
				}
			}
		}
	}

	// Emit Native/Hermes CPU profiles & trimmed Heap snapshots once per release for Deep Diagnostics.
	for _, platform := range []string{"android", "ios"} {
		pool := devicePoolFor(platform)
		profiles := profilesFor(platform)
		for idx, prof := range profiles {
			diagDay := epochStart.AddDate(0, 0, 3+idx*4)
			diagTS := diagDay.Add(12 * time.Hour).UnixMilli()
			env := protocol.BatchEnvelope{
				SchemaVersion: 1,
				SentAtMS:      nowUTC.UnixMilli(),
				Resource: protocol.Resource{
					AppID:       "dev.caliper.demo",
					Platform:    platform,
					Env:         "prod",
					AppVersion:  prof.version,
					BuildNumber: prof.build,
					GitSHA:      prof.gitSHA,
					SymbolID:    fmt.Sprintf("sym-%s-%s-%s", platform, prof.version, prof.build),
					Adapter:     "native",
					SDKName:     "caliper-" + platform,
					SDKVersion:  "1.9.0",
					InstallHash: fmt.Sprintf("%016x", rng.Uint64()),
					DeviceModel: pool[1].model,
					OSVersion:   pool[1].osVersion,
				},
				Events: buildDiagnosticEvents(platform, prof, diagTS),
			}
			if err := postGzip(client, *target+"/v1/ingest", *apiKey, env); err != nil {
				log.Fatalf("diagnostic ingest failed: %v", err)
			}
			sentEvents += len(env.Events)
			batchesSent++
		}
	}

	elapsed := time.Since(start)
	log.Printf("Replayed %d realistic canary events (%s..%s) in %d gzipped batches in %v (%.0f events/sec)",
		sentEvents, epochStart.Format("2006-01-02"), todayUTC.Format("2006-01-02"),
		batchesSent, elapsed.Round(time.Millisecond), float64(sentEvents)/elapsed.Seconds())

	if !*verify {
		return
	}

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

func buildSyntheticEvent(rng *rand.Rand, seq int, platform string, p releaseProfile, tier deviceTier, dayFactor float64, ts int64) protocol.Event {
	id := fmt.Sprintf("replay-%s-%s-%07d", platform, p.version, seq)
	scenes := []string{"home_feed", "streaming_reader", "media_gallery"}
	scene := scenes[seq%len(scenes)]

	switch seq % 6 {
	case 0: // startup (log-normal right-skewed realistic mobile cold start)
		median := p.startupMedMS * tier.startupScale * dayFactor
		total := logNormal(rng, median, 0.30, 180, 4800)
		native := total * (0.28 + rng.Float64()*0.06)
		firstFrame := total * (0.24 + rng.Float64()*0.06)
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
		frames := int64(950 + rng.Intn(550))
		effRate := p.jankRate * tier.jankScale * dayFactor * (0.85 + rng.Float64()*0.30)
		jank := int64(math.Round(float64(frames) * effRate))
		if jank > frames {
			jank = frames
		}
		severe := jank / 4
		if seq%50 == 0 {
			frames, jank, severe = 0, 0, 0 // verify zero-frame windows remain isolated & never inflate smoothness
		}
		var buckets [contract.HistogramBucketCount]int64
		if frames > 0 {
			smooth := frames - jank
			buckets[0] = int64(float64(smooth) * 0.88)
			buckets[1] = smooth - buckets[0]
			buckets[2] = jank - severe
			buckets[3] = severe
		}
		var stall []string
		if p.jankRate > 0.025 && frames > 0 && rng.Float64() < 0.25 {
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
			SevereJankFrameCount: severe,
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
		cpu := logNormal(rng, p.cpuBasePct*tier.memScale*dayFactor, 0.16, 4.0, 88.0)
		memMiB := logNormal(rng, p.memBaseMiB*tier.memScale*dayFactor, 0.14, 72.0, 960.0)
		memKiB := memMiB * 1024.0
		payload, _ := json.Marshal(protocol.ResourceWindowPayload{
			WindowDurationMS: 30000,
			CPUPct:           cpu,
			MemoryKind:       memKind,
			MemoryPeakKiB:    memKiB,
			MemoryAvgKiB:     memKiB * 0.88,
			SampleCount:      6,
		})
		return protocol.Event{EventID: id, Kind: protocol.KindResourceWindow, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}

	case 3: // network_summary
		calls := int64(25 + rng.Intn(25))
		// Binomial-like failure count so daily success rate moves smoothly with realistic variance.
		var fails int64
		effFailRate := p.apiFailRate * dayFactor
		for c := int64(0); c < calls; c++ {
			if rng.Float64() < effFailRate {
				fails++
			}
		}
		var buckets [contract.HistogramBucketCount]int64
		var sumMS float64
		for c := int64(0); c < calls; c++ {
			d := logNormal(rng, p.apiMedMS*dayFactor, 0.34, 28, 8000)
			buckets[contract.DurationBucketIndex(d)]++
			sumMS += d
		}
		var fc protocol.NetworkFailureCounts
		for fIdx := int64(0); fIdx < fails; fIdx++ {
			r := rng.Float64()
			switch {
			case r < 0.44:
				fc.HTTP++
			case r < 0.68:
				fc.Timeout++
			case r < 0.84:
				fc.Network++
			case r < 0.93:
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

	case 4: // network_transport (with authentic W3C trace_id & span_id for OTLP end-to-end correlation)
		ops := []struct {
			op     string
			method string
			scale  float64
		}{
			{"/api/v1/feed", "GET", 1.0},
			{"/api/v1/gallery", "GET", 1.25},
			{"/api/v1/stream/article", "POST", 1.45},
			{"/api/v1/user/profile", "GET", 0.62},
		}
		chosen := ops[(seq/6)%len(ops)]
		reused := rng.Float64() < 0.68
		dns := 0.0
		tcp := 0.0
		tls := 0.0
		if !reused {
			dns = logNormal(rng, 14.0, 0.30, 3, 180)
			tcp = logNormal(rng, 22.0, 0.28, 6, 260)
			tls = logNormal(rng, 38.0, 0.28, 10, 420)
			if p.version == "1.1.0" {
				tls += 45.0 * chosen.scale // mild connection-pool churn regression
			}
		}
		ttfb := logNormal(rng, p.apiMedMS*0.58*chosen.scale*dayFactor, 0.32, 18, 4500)
		body := logNormal(rng, p.apiMedMS*0.24*chosen.scale*dayFactor, 0.30, 6, 1800)
		total := dns + tcp + tls + ttfb + body
		res := "success"
		status := 200
		if rng.Float64() < 0.08 {
			status = 304
		}
		if rng.Float64() < p.apiFailRate*1.35*dayFactor {
			fr := rng.Float64()
			switch {
			case fr < 0.45:
				res = "failed"
				status = 502
			case fr < 0.66:
				res = "failed"
				status = 504
			case fr < 0.82:
				res = "failed"
				status = 429
			case fr < 0.90:
				res = "failed"
				status = 401
			default:
				res = "timeout"
				status = 0
				total += 1600
			}
		}
		proto := "h2"
		if rng.Float64() < 0.30 {
			proto = "h3"
		}
		netTypes := []string{"wifi", "5g", "4g"}
		netType := netTypes[rng.Intn(len(netTypes))]
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
			ConnectionReused: reused,
			Protocol:         proto,
			NetworkType:      netType,
			TraceID:          randomHex(rng, 16),
			SpanID:           randomHex(rng, 8),
		})
		return protocol.Event{EventID: id, Kind: protocol.KindNetworkStage, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}

	default: // js_longtask, custom_scene, or crash/ANR/abnormal_exit
		if rng.Float64() < p.crashRate*3.0*tier.crashScale*dayFactor {
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
						{TimestampMS: ts - 1200, Category: "network", Message: "GET /api/v1/gallery 200 (480ms)"},
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
						{TimestampMS: ts - 15000, Category: "resource", Message: "memory peak crossed 288 MiB"},
					},
				})
				return protocol.Event{EventID: id, Kind: protocol.KindAbnormalExit, OccurredAtMS: ts, ContractVersion: 1, Scene: "media_gallery", PolicyID: "default_v1", Payload: payload}
			}
		}
		if (seq/6)%2 == 0 {
			type journeySpec struct {
				name   string
				scale  float64
				stages []string
			}
			journeys := []journeySpec{
				{"article_open_to_readable", 1.0, []string{"cache_lookup", "api_fetch", "markdown_parse", "layout_render"}},
				{"feed_refresh_pipeline", 0.85, []string{"network_fetch", "json_decode", "image_prefetch", "list_diff"}},
				{"media_decode_pipeline", 1.20, []string{"disk_read", "codec_decode", "gpu_upload"}},
			}
			j := journeys[(seq/12)%len(journeys)]
			totalDur := logNormal(rng, p.apiMedMS*1.40*j.scale*tier.startupScale*dayFactor, 0.28, 60, 4500)
			jStatus := "success"
			errCode := ""
			if rng.Float64() < p.apiFailRate*1.1*dayFactor {
				if rng.Float64() < 0.65 {
					jStatus = "failure"
					errCode = "ERR_DECODE_OR_UPSTREAM"
				} else {
					jStatus = "timeout"
					errCode = "ERR_STAGE_TIMEOUT"
					totalDur += 900
				}
			}
			weights := []float64{0.15, 0.42, 0.25, 0.18}
			stages := make([]protocol.SceneStage, 0, len(j.stages))
			for idx, stName := range j.stages {
				w := weights[idx%len(weights)]
				stages = append(stages, protocol.SceneStage{
					Name:       stName,
					DurationMS: totalDur * w,
				})
			}
			payload, _ := json.Marshal(protocol.CustomScenePayload{
				JourneyName: j.name,
				Status:      jStatus,
				DurationMS:  &totalDur,
				ErrorCode:   errCode,
				Stages:      stages,
			})
			return protocol.Event{EventID: id, Kind: protocol.KindCustomScene, OccurredAtMS: ts, ContractVersion: 1, Scene: scene, PolicyID: "default_v1", Payload: payload}
		}
		cnt := int64(1 + rng.Intn(3))
		blk := float64(cnt) * logNormal(rng, 58.0+p.jankRate*750.0, 0.22, 45.0, 900.0)
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

	hotSamples := int64(22)
	if regressed {
		hotSamples = 58
	}
	nativePayload, _ := json.Marshal(protocol.CPUProfilePayload{
		Runtime:          "native",
		ThreadName:       "main",
		SampleIntervalMS: 10,
		DurationMS:       5000,
		TotalSamples:     100,
		Stacks: []protocol.ProfileStackSample{
			{Frames: []string{"MainRunLoop", "HomeFeedController.renderVisibleCells", hotFrame}, Samples: hotSamples},
			{Frames: []string{"MainRunLoop", "HomeFeedController.renderVisibleCells", "LayoutEngine.measure"}, Samples: 100 - hotSamples - 12},
			{Frames: []string{"MainRunLoop", "NetworkClient.dispatch"}, Samples: 12},
		},
	})

	hermesHot := int64(22)
	if regressed {
		hermesHot = 54
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

	cacheKiB := 44.0 * 1024
	totalRetKiB := 188.0 * 1024
	cacheInst := int64(145)
	trigger := "manual"
	if regressed {
		cacheKiB = 112.0 * 1024
		totalRetKiB = 264.0 * 1024
		cacheInst = 420
		trigger = "high_watermark"
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
