package storage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

type rollupKey struct {
	AppID       string
	Platform    string
	Env         string
	AppVersion  string
	BuildNumber string
	Adapter     string
	DayUTC      string
	Scene       string
	PolicyID    string
	Algorithm   string
	MemoryKind  string
}

type issueBucket struct {
	AppID       string
	Platform    string
	Env         string
	Fingerprint string
	Kind        string
	Title       string
	Fatal       bool
	ExcType     string
	ExitReason  string
	EventCount  int64
	FirstSeenMS int64
	LastSeenMS  int64
	Installs    map[string]struct{}
	Releases    map[string]struct{}
	Days        map[string]int64
	Frames      []protocol.StackFrame
	Breadcrumbs []protocol.Breadcrumb
}

type walEntry struct {
	Resource protocol.Resource `json:"r"`
	Event    protocol.Event    `json:"e"`
}

type persistedMeta struct {
	Releases       map[string]ReleaseRecord  `json:"releases"`
	Policies       map[string]SamplingPolicy `json:"policies"`
	Symbols        map[string]SymbolRecord   `json:"symbols"`
	AlertRules     map[string]AlertRule      `json:"alert_rules"`
	AlertIncidents map[string]AlertIncident  `json:"alert_incidents"`
}

// EmbeddedStore is a zero-CGO, WAL-durable, in-process columnar rollup store implementing
// both AnalyticsStore and MetaStore. Designed for <1s builds and sub-10ms 100k-event queries.
type EmbeddedStore struct {
	mu             sync.RWMutex
	dataDir        string
	walFile        *os.File
	walWriter      *bufio.Writer
	seenEvents     map[string]struct{}
	totalEvents    int64
	releases       map[string]ReleaseRecord
	policies       map[string]SamplingPolicy
	symbols        map[string]SymbolRecord
	symbolBytes    map[string][]byte // in-memory fallback when dataDir == ""
	alertRules     map[string]AlertRule
	alertIncidents map[string]AlertIncident
	startups       map[rollupKey]*StartupAggregate
	fluencies      map[rollupKey]*FluencyAggregate
	resources      map[rollupKey]*ResourceAggregate
	networks       map[rollupKey]*NetworkAggregate
	issues         map[string]*issueBucket // key: app|platform|env|fingerprint
}

// OpenEmbedded opens or initializes an embedded store in dataDir (empty dataDir = memory-only).
func OpenEmbedded(dataDir string) (*EmbeddedStore, error) {
	s := &EmbeddedStore{
		dataDir:        dataDir,
		seenEvents:     make(map[string]struct{}, 16384),
		releases:       make(map[string]ReleaseRecord),
		policies:       make(map[string]SamplingPolicy),
		symbols:        make(map[string]SymbolRecord),
		symbolBytes:    make(map[string][]byte),
		alertRules:     make(map[string]AlertRule),
		alertIncidents: make(map[string]AlertIncident),
		startups:       make(map[rollupKey]*StartupAggregate),
		fluencies:      make(map[rollupKey]*FluencyAggregate),
		resources:      make(map[rollupKey]*ResourceAggregate),
		networks:       make(map[rollupKey]*NetworkAggregate),
		issues:         make(map[string]*issueBucket),
	}

	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
		if err := s.loadMetaLocked(); err != nil {
			return nil, err
		}
		if err := s.replayWALLocked(); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(filepath.Join(dataDir, "events.wal.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open wal: %w", err)
		}
		s.walFile = f
		s.walWriter = bufio.NewWriterSize(f, 256*1024)
	}
	return s, nil
}

// Close flushes WAL and persists metadata atomically.
func (s *EmbeddedStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	if s.walWriter != nil {
		if err := s.walWriter.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.walFile != nil {
		if err := s.walFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.walFile = nil
		s.walWriter = nil
	}
	if s.dataDir != "" {
		if err := s.saveMetaLocked(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *EmbeddedStore) loadMetaLocked() error {
	metaPath := filepath.Join(s.dataDir, "meta.json")
	raw, err := os.ReadFile(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read meta.json: %w", err)
	}
	var pm persistedMeta
	if err := json.Unmarshal(raw, &pm); err != nil {
		return fmt.Errorf("decode meta.json: %w", err)
	}
	if pm.Releases != nil {
		s.releases = pm.Releases
	}
	if pm.Policies != nil {
		s.policies = pm.Policies
	}
	if pm.Symbols != nil {
		s.symbols = pm.Symbols
	}
	if pm.AlertRules != nil {
		s.alertRules = pm.AlertRules
	}
	if pm.AlertIncidents != nil {
		s.alertIncidents = pm.AlertIncidents
	}
	return nil
}

func (s *EmbeddedStore) saveMetaLocked() error {
	if s.dataDir == "" {
		return nil
	}
	raw, err := json.MarshalIndent(persistedMeta{
		Releases:       s.releases,
		Policies:       s.policies,
		Symbols:        s.symbols,
		AlertRules:     s.alertRules,
		AlertIncidents: s.alertIncidents,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dataDir, "meta.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dataDir, "meta.json"))
}

func (s *EmbeddedStore) replayWALLocked() error {
	walPath := filepath.Join(s.dataDir, "events.wal.jsonl")
	f, err := os.Open(walPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open wal for replay: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry walEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // ignore partial trailing line on crash recovery
		}
		parsed, err := protocol.ValidateEvent(&entry.Event)
		if err != nil {
			continue
		}
		if _, dup := s.seenEvents[entry.Event.EventID]; dup {
			continue
		}
		s.applyEventLocked(entry.Resource, entry.Event, parsed, true)
	}
	return scanner.Err()
}

// IngestBatch validates, deduplicates, persists to WAL, and updates columnar rollups.
func (s *EmbeddedStore) IngestBatch(_ context.Context, batch *protocol.BatchEnvelope) (IngestResult, error) {
	if err := batch.Validate(); err != nil {
		return IngestResult{Rejected: len(batch.Events)}, err
	}

	type validatedItem struct {
		ev     protocol.Event
		parsed any
	}
	valid := make([]validatedItem, 0, len(batch.Events))
	var res IngestResult
	for i := range batch.Events {
		parsed, err := protocol.ValidateEvent(&batch.Events[i])
		if err != nil {
			res.Rejected++
			continue
		}
		valid = append(valid, validatedItem{ev: batch.Events[i], parsed: parsed})
	}
	if len(valid) == 0 {
		return res, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	releaseMutated := false
	for _, item := range valid {
		if _, dup := s.seenEvents[item.ev.EventID]; dup {
			res.Duplicates++
			continue
		}
		if s.walWriter != nil {
			line, err := json.Marshal(walEntry{Resource: batch.Resource, Event: item.ev})
			if err != nil {
				res.Rejected++
				continue
			}
			if _, err := s.walWriter.Write(line); err != nil {
				return res, fmt.Errorf("write wal: %w", err)
			}
			if err := s.walWriter.WriteByte('\n'); err != nil {
				return res, fmt.Errorf("write wal newline: %w", err)
			}
		}
		s.applyEventLocked(batch.Resource, item.ev, item.parsed, false)
		res.Accepted++
		releaseMutated = true
	}

	if s.walWriter != nil && res.Accepted > 0 {
		if err := s.walWriter.Flush(); err != nil {
			return res, fmt.Errorf("flush wal: %w", err)
		}
	}
	if releaseMutated && s.dataDir != "" {
		_ = s.saveMetaLocked()
	}
	return res, nil
}

func (s *EmbeddedStore) applyEventLocked(r protocol.Resource, e protocol.Event, parsed any, isReplay bool) {
	s.seenEvents[e.EventID] = struct{}{}
	s.totalEvents++

	adapter := r.Adapter
	if adapter == "" {
		adapter = "native"
	}
	dayUTC := time.UnixMilli(e.OccurredAtMS).UTC().Format("2006-01-02")
	scene := e.Scene
	if scene == "" {
		scene = "unknown"
	}

	// Auto-discover release identity (zero manual registration friction).
	if !isReplay {
		rKey := releaseMapKey(r.AppID, r.Platform, r.Env, r.AppVersion, r.BuildNumber)
		rec, exists := s.releases[rKey]
		if !exists {
			rec = ReleaseRecord{
				AppID:       r.AppID,
				Platform:    r.Platform,
				Env:         r.Env,
				AppVersion:  r.AppVersion,
				BuildNumber: r.BuildNumber,
				GitSHA:      r.GitSHA,
				SymbolID:    r.SymbolID,
				Adapter:     adapter,
				FirstSeenMS: e.OccurredAtMS,
				LastSeenMS:  e.OccurredAtMS,
			}
		}
		if e.OccurredAtMS < rec.FirstSeenMS {
			rec.FirstSeenMS = e.OccurredAtMS
		}
		if e.OccurredAtMS > rec.LastSeenMS {
			rec.LastSeenMS = e.OccurredAtMS
		}
		if rec.GitSHA == "" && r.GitSHA != "" {
			rec.GitSHA = r.GitSHA
		}
		if rec.SymbolID == "" && r.SymbolID != "" {
			rec.SymbolID = r.SymbolID
		}
		rec.EventCount++
		s.releases[rKey] = rec
	}

	baseKey := rollupKey{
		AppID:       r.AppID,
		Platform:    r.Platform,
		Env:         r.Env,
		AppVersion:  r.AppVersion,
		BuildNumber: r.BuildNumber,
		Adapter:     adapter,
		DayUTC:      dayUTC,
		Scene:       scene,
		PolicyID:    e.PolicyID,
	}

	switch p := parsed.(type) {
	case protocol.StartupPayload:
		if !p.ColdStart {
			return
		}
		agg := s.startups[baseKey]
		if agg == nil {
			agg = &StartupAggregate{ContractVer: e.ContractVersion, PolicyIDs: map[string]struct{}{}}
			s.startups[baseKey] = agg
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}
		switch p.Result {
		case "success":
			agg.SuccessCount++
			agg.TotalHist[contract.DurationBucketIndex(p.TotalDurationMS)]++
			if p.NativeInitMS != nil && p.FirstFrameMS != nil {
				agg.StagedCount++
				agg.NativeInitHist[contract.DurationBucketIndex(*p.NativeInitMS)]++
				agg.FirstFrameHist[contract.DurationBucketIndex(*p.FirstFrameMS)]++
				rt := p.TotalDurationMS - *p.NativeInitMS - *p.FirstFrameMS
				if p.RuntimeInitMS != nil {
					rt = *p.RuntimeInitMS
				}
				if rt < 0 {
					rt = 0
				}
				agg.RuntimeHist[contract.DurationBucketIndex(rt)]++
			}
		case "aborted":
			agg.AbortedCount++
		case "timeout":
			agg.TimeoutCount++
		}

	case protocol.FluencyWindowPayload:
		k := baseKey
		k.Algorithm = p.Algorithm
		agg := s.fluencies[k]
		if agg == nil {
			agg = &FluencyAggregate{Scene: scene, Algorithm: p.Algorithm, PolicyIDs: map[string]struct{}{}}
			s.fluencies[k] = agg
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}
		agg.TotalWindows++
		if p.FrameCount == 0 {
			agg.ZeroFrameWindows++
		} else {
			agg.ValidWindows++
			agg.TotalFrames += p.FrameCount
			agg.JankFrames += p.JankFrameCount
			agg.SevereJankFrames += p.SevereJankFrameCount
		}
		if len(p.FrameDurationBuckets) == contract.HistogramBucketCount {
			_ = agg.FrameDurationHist.AddSlice(p.FrameDurationBuckets)
		}
		if len(p.StallStack) > 0 {
			agg.StallWindowCount++
			if len(agg.SampleStallStacks) < 5 {
				cp := append([]string(nil), p.StallStack...)
				agg.SampleStallStacks = append(agg.SampleStallStacks, cp)
			}
		}

	case protocol.ResourceWindowPayload:
		k := baseKey
		k.MemoryKind = p.MemoryKind
		agg := s.resources[k]
		if agg == nil {
			agg = &ResourceAggregate{Scene: scene, MemoryKind: p.MemoryKind, PolicyIDs: map[string]struct{}{}}
			s.resources[k] = agg
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}
		agg.ResourceWindows++
		agg.CPUPctSum += p.CPUPct
		agg.MemoryPeakHistMiB[contract.MemoryBucketIndex(p.MemoryPeakKiB/1024.0)]++

	case protocol.JSLongTaskPayload:
		k := baseKey
		k.MemoryKind = "js_only"
		agg := s.resources[k]
		if agg == nil {
			agg = &ResourceAggregate{Scene: scene, MemoryKind: "js_only", PolicyIDs: map[string]struct{}{}}
			s.resources[k] = agg
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}
		agg.JSLongTaskWindows++
		agg.JSLongTaskCount += p.LongTaskCount
		agg.JSBlockingSumMS += p.TotalBlockingMS
		if p.MaxTaskMS > agg.JSMaxTaskMS {
			agg.JSMaxTaskMS = p.MaxTaskMS
		}

	case protocol.NetworkSummaryPayload:
		agg := s.networks[baseKey]
		if agg == nil {
			agg = &NetworkAggregate{TransportSamples: map[string]*TransportOpAggregate{}, PolicyIDs: map[string]struct{}{}}
			s.networks[baseKey] = agg
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}
		agg.SummaryWindows++
		agg.TotalCalls += p.CallCount
		agg.FailureCounts.HTTP += p.FailureCounts.HTTP
		agg.FailureCounts.Business += p.FailureCounts.Business
		agg.FailureCounts.Parse += p.FailureCounts.Parse
		agg.FailureCounts.Network += p.FailureCounts.Network
		agg.FailureCounts.Timeout += p.FailureCounts.Timeout
		_ = agg.DurationHist.AddSlice(p.DurationBuckets)
		agg.DurationSumMS += p.DurationSumMS

	case protocol.NetworkTransportPayload:
		agg := s.networks[baseKey]
		if agg == nil {
			agg = &NetworkAggregate{TransportSamples: map[string]*TransportOpAggregate{}, PolicyIDs: map[string]struct{}{}}
			s.networks[baseKey] = agg
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}
		opKey := p.Method + ":" + p.Operation
		op := agg.TransportSamples[opKey]
		if op == nil {
			op = &TransportOpAggregate{Operation: p.Operation, Method: p.Method}
			agg.TransportSamples[opKey] = op
		}
		op.TotalCount++
		switch p.Result {
		case "success":
			op.SuccessCnt++
		case "failed":
			op.FailedCnt++
		case "timeout":
			op.TimeoutCnt++
		}
		if p.ConnectionReused {
			op.ReusedCnt++
		}
		if p.DNSMS != nil {
			op.DNSHist[contract.DurationBucketIndex(*p.DNSMS)]++
		}
		if p.TCPMS != nil {
			op.TCPHist[contract.DurationBucketIndex(*p.TCPMS)]++
		}
		if p.TLSMS != nil {
			op.TLSHist[contract.DurationBucketIndex(*p.TLSMS)]++
		}
		if p.TTFBMS != nil {
			op.TTFBHist[contract.DurationBucketIndex(*p.TTFBMS)]++
		}
		if p.BodyMS != nil {
			op.BodyHist[contract.DurationBucketIndex(*p.BodyMS)]++
		}
		op.TotalHist[contract.DurationBucketIndex(p.TotalMS)]++

	case protocol.StabilityPayload:
		iKey := issueMapKey(r.AppID, r.Platform, r.Env, p.Fingerprint)
		ib := s.issues[iKey]
		if ib == nil {
			ib = &issueBucket{
				AppID:       r.AppID,
				Platform:    r.Platform,
				Env:         r.Env,
				Fingerprint: p.Fingerprint,
				Kind:        e.Kind,
				Title:       p.Title,
				Fatal:       p.Fatal,
				ExcType:     p.ExceptionType,
				ExitReason:  p.ExitReason,
				FirstSeenMS: e.OccurredAtMS,
				LastSeenMS:  e.OccurredAtMS,
				Installs:    map[string]struct{}{},
				Releases:    map[string]struct{}{},
				Days:        map[string]int64{},
			}
			s.issues[iKey] = ib
		}
		ib.EventCount++
		if e.OccurredAtMS < ib.FirstSeenMS {
			ib.FirstSeenMS = e.OccurredAtMS
		}
		if e.OccurredAtMS > ib.LastSeenMS {
			ib.LastSeenMS = e.OccurredAtMS
		}
		ib.Installs[r.InstallHash] = struct{}{}
		ib.Releases[r.AppVersion+"("+r.BuildNumber+")"] = struct{}{}
		ib.Days[dayUTC]++
		if len(p.Frames) > 0 && len(ib.Frames) == 0 {
			ib.Frames = append([]protocol.StackFrame(nil), p.Frames...)
		}
		if len(p.Breadcrumbs) > 0 && len(ib.Breadcrumbs) == 0 {
			ib.Breadcrumbs = append([]protocol.Breadcrumb(nil), p.Breadcrumbs...)
		}
	}
}

func matchesFilter(k rollupKey, f QueryFilter) bool {
	if f.AppID != "" && k.AppID != f.AppID {
		return false
	}
	if f.Platform != "" && k.Platform != f.Platform {
		return false
	}
	if f.Env != "" && k.Env != f.Env {
		return false
	}
	if f.AppVersion != "" && k.AppVersion != f.AppVersion {
		return false
	}
	if f.BuildNumber != "" && k.BuildNumber != f.BuildNumber {
		return false
	}
	if f.Adapter != "" && k.Adapter != f.Adapter {
		return false
	}
	if f.Scene != "" && k.Scene != f.Scene {
		return false
	}
	if f.PolicyID != "" && k.PolicyID != f.PolicyID {
		return false
	}
	if f.Algorithm != "" && k.Algorithm != "" && k.Algorithm != f.Algorithm {
		return false
	}
	if f.StartDayUTC != "" && k.DayUTC < f.StartDayUTC {
		return false
	}
	if f.EndDayUTC != "" && k.DayUTC > f.EndDayUTC {
		return false
	}
	return true
}

func (s *EmbeddedStore) QueryStartup(_ context.Context, f QueryFilter) (StartupAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := StartupAggregate{PolicyIDs: map[string]struct{}{}}
	for k, agg := range s.startups {
		if !matchesFilter(k, f) {
			continue
		}
		out.TotalHist.Add(agg.TotalHist)
		out.NativeInitHist.Add(agg.NativeInitHist)
		out.RuntimeHist.Add(agg.RuntimeHist)
		out.FirstFrameHist.Add(agg.FirstFrameHist)
		out.SuccessCount += agg.SuccessCount
		out.AbortedCount += agg.AbortedCount
		out.TimeoutCount += agg.TimeoutCount
		out.StagedCount += agg.StagedCount
		if agg.ContractVer > out.ContractVer {
			out.ContractVer = agg.ContractVer
		}
		for pid := range agg.PolicyIDs {
			out.PolicyIDs[pid] = struct{}{}
		}
	}
	return out, nil
}

func (s *EmbeddedStore) QueryFluency(_ context.Context, f QueryFilter) ([]FluencyAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type groupKey struct{ scene, algo string }
	grouped := map[groupKey]*FluencyAggregate{}
	for k, agg := range s.fluencies {
		if !matchesFilter(k, f) {
			continue
		}
		gk := groupKey{scene: agg.Scene, algo: agg.Algorithm}
		dst := grouped[gk]
		if dst == nil {
			dst = &FluencyAggregate{Scene: agg.Scene, Algorithm: agg.Algorithm, PolicyIDs: map[string]struct{}{}}
			grouped[gk] = dst
		}
		dst.TotalWindows += agg.TotalWindows
		dst.ValidWindows += agg.ValidWindows
		dst.ZeroFrameWindows += agg.ZeroFrameWindows
		dst.TotalFrames += agg.TotalFrames
		dst.JankFrames += agg.JankFrames
		dst.SevereJankFrames += agg.SevereJankFrames
		dst.FrameDurationHist.Add(agg.FrameDurationHist)
		dst.StallWindowCount += agg.StallWindowCount
		for _, stack := range agg.SampleStallStacks {
			if len(dst.SampleStallStacks) < 5 {
				dst.SampleStallStacks = append(dst.SampleStallStacks, stack)
			}
		}
		for pid := range agg.PolicyIDs {
			dst.PolicyIDs[pid] = struct{}{}
		}
	}
	out := make([]FluencyAggregate, 0, len(grouped))
	for _, v := range grouped {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scene != out[j].Scene {
			return out[i].Scene < out[j].Scene
		}
		return out[i].Algorithm < out[j].Algorithm
	})
	return out, nil
}

func (s *EmbeddedStore) QueryResources(_ context.Context, f QueryFilter) ([]ResourceAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grouped := map[string]*ResourceAggregate{}
	for k, agg := range s.resources {
		if !matchesFilter(k, f) {
			continue
		}
		dst := grouped[agg.Scene]
		if dst == nil {
			dst = &ResourceAggregate{Scene: agg.Scene, PolicyIDs: map[string]struct{}{}}
			grouped[agg.Scene] = dst
		}
		if agg.MemoryKind != "js_only" && dst.MemoryKind == "" {
			dst.MemoryKind = agg.MemoryKind
		}
		dst.ResourceWindows += agg.ResourceWindows
		dst.CPUPctSum += agg.CPUPctSum
		dst.MemoryPeakHistMiB.Add(agg.MemoryPeakHistMiB)
		dst.JSLongTaskWindows += agg.JSLongTaskWindows
		dst.JSLongTaskCount += agg.JSLongTaskCount
		dst.JSBlockingSumMS += agg.JSBlockingSumMS
		if agg.JSMaxTaskMS > dst.JSMaxTaskMS {
			dst.JSMaxTaskMS = agg.JSMaxTaskMS
		}
		for pid := range agg.PolicyIDs {
			dst.PolicyIDs[pid] = struct{}{}
		}
	}
	out := make([]ResourceAggregate, 0, len(grouped))
	for _, v := range grouped {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scene < out[j].Scene })
	return out, nil
}

func (s *EmbeddedStore) QueryNetwork(_ context.Context, f QueryFilter) (NetworkAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := NetworkAggregate{TransportSamples: map[string]*TransportOpAggregate{}, PolicyIDs: map[string]struct{}{}}
	for k, agg := range s.networks {
		if !matchesFilter(k, f) {
			continue
		}
		out.SummaryWindows += agg.SummaryWindows
		out.TotalCalls += agg.TotalCalls
		out.FailureCounts.HTTP += agg.FailureCounts.HTTP
		out.FailureCounts.Business += agg.FailureCounts.Business
		out.FailureCounts.Parse += agg.FailureCounts.Parse
		out.FailureCounts.Network += agg.FailureCounts.Network
		out.FailureCounts.Timeout += agg.FailureCounts.Timeout
		out.DurationHist.Add(agg.DurationHist)
		out.DurationSumMS += agg.DurationSumMS
		for pid := range agg.PolicyIDs {
			out.PolicyIDs[pid] = struct{}{}
		}
		for opKey, src := range agg.TransportSamples {
			dst := out.TransportSamples[opKey]
			if dst == nil {
				dst = &TransportOpAggregate{Operation: src.Operation, Method: src.Method}
				out.TransportSamples[opKey] = dst
			}
			dst.TotalCount += src.TotalCount
			dst.SuccessCnt += src.SuccessCnt
			dst.FailedCnt += src.FailedCnt
			dst.TimeoutCnt += src.TimeoutCnt
			dst.ReusedCnt += src.ReusedCnt
			dst.DNSHist.Add(src.DNSHist)
			dst.TCPHist.Add(src.TCPHist)
			dst.TLSHist.Add(src.TLSHist)
			dst.TTFBHist.Add(src.TTFBHist)
			dst.BodyHist.Add(src.BodyHist)
			dst.TotalHist.Add(src.TotalHist)
		}
	}
	return out, nil
}

func (s *EmbeddedStore) QueryIssues(_ context.Context, f QueryFilter, limit int) ([]IssueSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := make([]IssueSummary, 0)
	for _, ib := range s.issues {
		if f.AppID != "" && ib.AppID != f.AppID {
			continue
		}
		if f.Platform != "" && ib.Platform != f.Platform {
			continue
		}
		if f.Env != "" && ib.Env != f.Env {
			continue
		}
		if f.AppVersion != "" {
			matched := false
			prefix := f.AppVersion + "("
			for rel := range ib.Releases {
				if len(rel) >= len(prefix) && rel[:len(prefix)] == prefix {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, toIssueSummary(ib))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EventCount != out[j].EventCount {
			return out[i].EventCount > out[j].EventCount
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *EmbeddedStore) GetIssue(_ context.Context, fingerprint string, f QueryFilter) (*IssueSummary, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ib := range s.issues {
		if ib.Fingerprint != fingerprint {
			continue
		}
		if f.AppID != "" && ib.AppID != f.AppID {
			continue
		}
		if f.Platform != "" && ib.Platform != f.Platform {
			continue
		}
		summary := toIssueSummary(ib)
		return &summary, true, nil
	}
	return nil, false, nil
}

func toIssueSummary(ib *issueBucket) IssueSummary {
	rels := make([]string, 0, len(ib.Releases))
	for rel := range ib.Releases {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	return IssueSummary{
		Fingerprint:      ib.Fingerprint,
		Kind:             ib.Kind,
		Title:            ib.Title,
		Fatal:            ib.Fatal,
		ExceptionType:    ib.ExcType,
		ExitReason:       ib.ExitReason,
		EventCount:       ib.EventCount,
		AffectedInstalls: int64(len(ib.Installs)),
		FirstSeenMS:      ib.FirstSeenMS,
		LastSeenMS:       ib.LastSeenMS,
		Releases:         rels,
		SampleFrames:     ib.Frames,
		SampleCrumbs:     ib.Breadcrumbs,
	}
}

func (s *EmbeddedStore) RegisterRelease(_ context.Context, r ReleaseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := releaseMapKey(r.AppID, r.Platform, r.Env, r.AppVersion, r.BuildNumber)
	existing, ok := s.releases[key]
	if ok {
		r.FirstSeenMS = existing.FirstSeenMS
		r.LastSeenMS = existing.LastSeenMS
		r.EventCount = existing.EventCount
	} else {
		now := time.Now().UnixMilli()
		if r.FirstSeenMS == 0 {
			r.FirstSeenMS = now
		}
		if r.LastSeenMS == 0 {
			r.LastSeenMS = now
		}
	}
	r.Registered = true
	s.releases[key] = r
	return s.saveMetaLocked()
}

func (s *EmbeddedStore) ListReleases(_ context.Context, appID, platform, env string) ([]ReleaseRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ReleaseRecord, 0, len(s.releases))
	for _, r := range s.releases {
		if appID != "" && r.AppID != appID {
			continue
		}
		if platform != "" && r.Platform != platform {
			continue
		}
		if env != "" && r.Env != env {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenMS != out[j].LastSeenMS {
			return out[i].LastSeenMS > out[j].LastSeenMS
		}
		return out[i].AppVersion > out[j].AppVersion
	})
	return out, nil
}

func (s *EmbeddedStore) GetPolicy(_ context.Context, appID, platform, env string) (SamplingPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := appID + "|" + platform + "|" + env
	if p, ok := s.policies[key]; ok {
		return p, nil
	}
	// Sensible, fail-bounded default policy (3% sampling, 24h lease, 50 events/24h cap).
	return SamplingPolicy{
		PolicyID:          "default_v1",
		AppID:             appID,
		Platform:          platform,
		Env:               env,
		RatePPM:           30_000,
		LeaseTTLMS:        24 * 60 * 60 * 1000,
		MaxEvents24h:      50,
		EnabledCollectors: []string{"startup", "crash", "anr", "abnormal_exit", "fluency", "resources", "network"},
		UpdatedAtMS:       time.Now().UnixMilli(),
	}, nil
}

func (s *EmbeddedStore) UpsertPolicy(_ context.Context, p SamplingPolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := p.AppID + "|" + p.Platform + "|" + p.Env
	p.UpdatedAtMS = time.Now().UnixMilli()
	s.policies[key] = p
	return s.saveMetaLocked()
}

func (s *EmbeddedStore) SaveSymbol(_ context.Context, sym SymbolRecord, content []byte) (SymbolRecord, error) {
	if sym.SymbolID == "" || sym.AppID == "" || sym.AppVersion == "" || sym.BuildNumber == "" {
		return SymbolRecord{}, errors.New("symbol_id, app_id, app_version, and build_number are required")
	}
	for _, r := range sym.SymbolID {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return SymbolRecord{}, errors.New("symbol_id contains invalid characters")
		}
	}
	if sym.Env == "" {
		sym.Env = "prod"
	}
	sym.SizeBytes = int64(len(content))
	if sym.UploadedAtMS == 0 {
		sym.UploadedAtMS = time.Now().UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dataDir != "" {
		symDir := filepath.Join(s.dataDir, "symbols")
		if err := os.MkdirAll(symDir, 0o755); err != nil {
			return SymbolRecord{}, err
		}
		target := filepath.Join(symDir, sym.SymbolID+".sym")
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return SymbolRecord{}, err
		}
		sym.StoragePath = target
	} else {
		cp := make([]byte, len(content))
		copy(cp, content)
		s.symbolBytes[sym.SymbolID] = cp
	}

	s.symbols[sym.SymbolID] = sym

	// Auto-bind symbol_id onto matching release record.
	rKey := releaseMapKey(sym.AppID, sym.Platform, sym.Env, sym.AppVersion, sym.BuildNumber)
	rec, exists := s.releases[rKey]
	if !exists {
		rec = ReleaseRecord{
			AppID:       sym.AppID,
			Platform:    sym.Platform,
			Env:         sym.Env,
			AppVersion:  sym.AppVersion,
			BuildNumber: sym.BuildNumber,
			GitSHA:      sym.GitSHA,
			Registered:  true,
			FirstSeenMS: sym.UploadedAtMS,
			LastSeenMS:  sym.UploadedAtMS,
		}
	}
	rec.SymbolID = sym.SymbolID
	if rec.GitSHA == "" && sym.GitSHA != "" {
		rec.GitSHA = sym.GitSHA
	}
	s.releases[rKey] = rec

	if err := s.saveMetaLocked(); err != nil {
		return SymbolRecord{}, err
	}
	return sym, nil
}

func (s *EmbeddedStore) ListSymbols(_ context.Context, appID, platform, appVersion, buildNumber string) ([]SymbolRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SymbolRecord, 0, len(s.symbols))
	for _, sym := range s.symbols {
		if appID != "" && sym.AppID != appID {
			continue
		}
		if platform != "" && sym.Platform != platform {
			continue
		}
		if appVersion != "" && sym.AppVersion != appVersion {
			continue
		}
		if buildNumber != "" && sym.BuildNumber != buildNumber {
			continue
		}
		out = append(out, sym)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UploadedAtMS != out[j].UploadedAtMS {
			return out[i].UploadedAtMS > out[j].UploadedAtMS
		}
		return out[i].SymbolID < out[j].SymbolID
	})
	return out, nil
}

func (s *EmbeddedStore) GetSymbolContent(_ context.Context, symbolID string) (SymbolRecord, []byte, bool, error) {
	s.mu.RLock()
	sym, ok := s.symbols[symbolID]
	memData := s.symbolBytes[symbolID]
	s.mu.RUnlock()
	if !ok {
		return SymbolRecord{}, nil, false, nil
	}
	if sym.StoragePath != "" {
		raw, err := os.ReadFile(sym.StoragePath)
		if err != nil {
			return sym, nil, false, err
		}
		return sym, raw, true, nil
	}
	return sym, memData, true, nil
}

func (s *EmbeddedStore) UpsertAlertRule(_ context.Context, rule AlertRule) (AlertRule, error) {
	if rule.RuleID == "" || rule.Name == "" || rule.MetricID == "" {
		return AlertRule{}, errors.New("rule_id, name, and metric_id are required")
	}
	if rule.Platform != "android" && rule.Platform != "ios" {
		return AlertRule{}, errors.New("platform must be android or ios")
	}
	if rule.Operator != "gt" && rule.Operator != "lt" {
		return AlertRule{}, errors.New("operator must be gt or lt")
	}
	if rule.Env == "" {
		rule.Env = "prod"
	}
	if rule.MinSamples <= 0 {
		rule.MinSamples = contract.DefaultDisplayMinSamples
	}
	if rule.WebhookFormat == "" {
		rule.WebhookFormat = "generic"
	}
	if rule.CooldownSec < 0 {
		rule.CooldownSec = 0
	}
	rule.UpdatedAtMS = time.Now().UnixMilli()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertRules[rule.RuleID] = rule
	if err := s.saveMetaLocked(); err != nil {
		return AlertRule{}, err
	}
	return rule, nil
}

func (s *EmbeddedStore) ListAlertRules(_ context.Context, appID, platform string) ([]AlertRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AlertRule, 0, len(s.alertRules))
	for _, r := range s.alertRules {
		if appID != "" && r.AppID != appID {
			continue
		}
		if platform != "" && r.Platform != platform {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RuleID < out[j].RuleID })
	return out, nil
}

func (s *EmbeddedStore) SaveAlertIncident(_ context.Context, inc AlertIncident) error {
	if inc.IncidentID == "" || inc.RuleID == "" {
		return errors.New("incident_id and rule_id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertIncidents[inc.IncidentID] = inc
	return s.saveMetaLocked()
}

func (s *EmbeddedStore) ListAlertIncidents(_ context.Context, ruleID string) ([]AlertIncident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]AlertIncident, 0, len(s.alertIncidents))
	for _, inc := range s.alertIncidents {
		if ruleID != "" && inc.RuleID != ruleID {
			continue
		}
		out = append(out, inc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastEvaluatedMS != out[j].LastEvaluatedMS {
			return out[i].LastEvaluatedMS > out[j].LastEvaluatedMS
		}
		return out[i].IncidentID < out[j].IncidentID
	})
	return out, nil
}

func (s *EmbeddedStore) Stats(_ context.Context) Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{
		Engine:       "embedded-wal-columnar",
		TotalEvents:  s.totalEvents,
		ReleaseCount: len(s.releases),
		IssueCount:   len(s.issues),
	}
}

func releaseMapKey(appID, platform, env, ver, build string) string {
	return appID + "|" + platform + "|" + env + "|" + ver + "|" + build
}

func issueMapKey(appID, platform, env, fingerprint string) string {
	return appID + "|" + platform + "|" + env + "|" + fingerprint
}
