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
	"strings"
	"sync"
	"time"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage/objectstore"
)

type rollupKey struct {
	AppID       string
	Platform    string
	Env         string
	AppVersion  string
	BuildNumber string
	Adapter     string
	DeviceModel string
	OSVersion   string
	DayUTC      string
	Scene       string
	PolicyID    string
	Algorithm   string
	MemoryKind  string
}

type issueBucket struct {
	AppID             string
	Platform          string
	Env               string
	Fingerprint       string
	Kind              string
	Title             string
	Fatal             bool
	ExcType           string
	ExitReason        string
	EventCount        int64
	FirstSeenMS       int64
	LastSeenMS        int64
	Installs          map[string]struct{}
	Releases          map[string]struct{}
	Devices           map[string]int64
	OSVersions        map[string]int64
	ReleaseCounts     map[string]int64
	SampleUserHash    string
	SampleInstallHash string
	Days              map[string]int64
	Frames            []protocol.StackFrame
	Breadcrumbs       []protocol.Breadcrumb
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
	RemoteLogTasks map[string]RemoteLogTask  `json:"remote_log_tasks"`
	IssueStates    map[string]IssueState     `json:"issue_states,omitempty"`
}

// EmbeddedStore is a zero-CGO, WAL-durable, in-process columnar rollup store implementing
// both AnalyticsStore and MetaStore. Designed for <1s builds and sub-10ms 100k-event queries.
type EmbeddedStore struct {
	mu               sync.RWMutex
	dataDir          string
	objectStore      objectstore.ObjectStore
	walFile          *os.File
	walWriter        *bufio.Writer
	seenEvents       map[string]struct{}
	totalEvents      int64
	releases         map[string]ReleaseRecord
	policies         map[string]SamplingPolicy
	symbols          map[string]SymbolRecord
	symbolBytes      map[string][]byte // in-memory fallback when dataDir == ""
	alertRules       map[string]AlertRule
	alertIncidents   map[string]AlertIncident
	remoteLogTasks   map[string]RemoteLogTask
	remoteLogBytes   map[string][]byte
	issueStates      map[string]IssueState
	auditLogs        []AuditLogEntry
	installTimelines map[string][]SessionTimelineEvent
	userTimelines    map[string][]SessionTimelineEvent
	cpuProfiles      []CPUProfileRecord
	heapSnapshots    []HeapSnapshotRecord
	networkSamples   []NetworkTransportSample
	startups         map[rollupKey]*StartupAggregate
	fluencies        map[rollupKey]*FluencyAggregate
	resources        map[rollupKey]*ResourceAggregate
	networks         map[rollupKey]*NetworkAggregate
	customScenes     map[rollupKey]*CustomSceneAggregate
	issues           map[string]*issueBucket // key: app|platform|env|fingerprint
}

// SetObjectStore configures an external S3/MinIO-compatible object store for symbols and remote logs.
func (s *EmbeddedStore) SetObjectStore(store objectstore.ObjectStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objectStore = store
}

const (
	maxTimelineEventsPerHash = 200
	maxTrackedTimelineHashes = 10000
	maxStoredDiagnostics     = 500
	maxNetworkSamples        = 1000
)

// OpenEmbedded opens or initializes an embedded store in dataDir (empty dataDir = memory-only).
func OpenEmbedded(dataDir string) (*EmbeddedStore, error) {
	s := &EmbeddedStore{
		dataDir:          dataDir,
		seenEvents:       make(map[string]struct{}, 16384),
		releases:         make(map[string]ReleaseRecord),
		policies:         make(map[string]SamplingPolicy),
		symbols:          make(map[string]SymbolRecord),
		symbolBytes:      make(map[string][]byte),
		alertRules:       make(map[string]AlertRule),
		alertIncidents:   make(map[string]AlertIncident),
		remoteLogTasks:   make(map[string]RemoteLogTask),
		remoteLogBytes:   make(map[string][]byte),
		issueStates:      make(map[string]IssueState),
		installTimelines: make(map[string][]SessionTimelineEvent),
		userTimelines:    make(map[string][]SessionTimelineEvent),
		startups:         make(map[rollupKey]*StartupAggregate),
		fluencies:        make(map[rollupKey]*FluencyAggregate),
		resources:        make(map[rollupKey]*ResourceAggregate),
		networks:         make(map[rollupKey]*NetworkAggregate),
		customScenes:     make(map[rollupKey]*CustomSceneAggregate),
		issues:           make(map[string]*issueBucket),
	}

	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
		if err := s.loadMetaLocked(); err != nil {
			return nil, err
		}
		if err := s.loadAuditLogsLocked(); err != nil {
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
	if pm.RemoteLogTasks != nil {
		s.remoteLogTasks = pm.RemoteLogTasks
	}
	if pm.IssueStates != nil {
		s.issueStates = pm.IssueStates
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
		RemoteLogTasks: s.remoteLogTasks,
		IssueStates:    s.issueStates,
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

	s.indexTimelineLocked(r, e, adapter, scene, parsed)

	baseKey := rollupKey{
		AppID:       r.AppID,
		Platform:    r.Platform,
		Env:         r.Env,
		AppVersion:  r.AppVersion,
		BuildNumber: r.BuildNumber,
		Adapter:     adapter,
		DeviceModel: r.DeviceModel,
		OSVersion:   r.OSVersion,
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
			agg = &NetworkAggregate{
				StatusCodes:      map[string]int64{},
				TransportSamples: map[string]*TransportOpAggregate{},
				PolicyIDs:        map[string]struct{}{},
			}
			s.networks[baseKey] = agg
		}
		if agg.StatusCodes == nil {
			agg.StatusCodes = map[string]int64{}
		}
		agg.StatusCodes[statusCodeBucket(p.StatusCode, p.Result)]++
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

		isIssueSample := p.Result != "success" || p.StatusCode >= 400 || p.TotalMS >= 800
		if isIssueSample || len(s.networkSamples) < 120 || s.totalEvents%7 == 0 {
			s.networkSamples = append(s.networkSamples, NetworkTransportSample{
				EventID:          e.EventID,
				OccurredAtMS:     e.OccurredAtMS,
				AppID:            r.AppID,
				Platform:         r.Platform,
				Env:              r.Env,
				AppVersion:       r.AppVersion,
				BuildNumber:      r.BuildNumber,
				Adapter:          baseKey.Adapter,
				Scene:            baseKey.Scene,
				DeviceModel:      r.DeviceModel,
				OSVersion:        r.OSVersion,
				InstallHash:      r.InstallHash,
				UserHash:         r.UserHash,
				Operation:        p.Operation,
				Method:           p.Method,
				StatusCode:       p.StatusCode,
				Result:           p.Result,
				DNSMS:            p.DNSMS,
				TCPMS:            p.TCPMS,
				TLSMS:            p.TLSMS,
				TTFBMS:           p.TTFBMS,
				BodyMS:           p.BodyMS,
				TotalMS:          p.TotalMS,
				ConnectionReused: p.ConnectionReused,
				Protocol:         p.Protocol,
				NetworkType:      p.NetworkType,
			})
			if len(s.networkSamples) > maxNetworkSamples {
				s.networkSamples = s.networkSamples[len(s.networkSamples)-maxNetworkSamples:]
			}
		}

	case protocol.CustomScenePayload:
		ck := baseKey
		ck.Scene = p.JourneyName
		agg := s.customScenes[ck]
		if agg == nil {
			agg = &CustomSceneAggregate{
				JourneyName: p.JourneyName,
				StageHists:  map[string]contract.Histogram{},
				ErrorCodes:  map[string]int64{},
				PolicyIDs:   map[string]struct{}{},
			}
			s.customScenes[ck] = agg
		}
		agg.TotalCount++
		switch p.Status {
		case "success":
			agg.SuccessCount++
		case "failure":
			agg.FailureCount++
		case "timeout":
			agg.TimeoutCount++
		case "cancelled":
			agg.CancelledCount++
		}
		if p.ErrorCode != "" {
			agg.ErrorCodes[p.ErrorCode]++
		}
		if p.DurationMS != nil {
			agg.DurationHist[contract.DurationBucketIndex(*p.DurationMS)]++
		}
		for _, st := range p.Stages {
			h, exists := agg.StageHists[st.Name]
			if !exists {
				agg.StageOrder = append(agg.StageOrder, st.Name)
			}
			h[contract.DurationBucketIndex(st.DurationMS)]++
			agg.StageHists[st.Name] = h
		}
		if e.PolicyID != "" {
			agg.PolicyIDs[e.PolicyID] = struct{}{}
		}

	case protocol.StabilityPayload:
		iKey := issueMapKey(r.AppID, r.Platform, r.Env, p.Fingerprint)
		ib := s.issues[iKey]
		if ib == nil {
			ib = &issueBucket{
				AppID:         r.AppID,
				Platform:      r.Platform,
				Env:           r.Env,
				Fingerprint:   p.Fingerprint,
				Kind:          e.Kind,
				Title:         p.Title,
				Fatal:         p.Fatal,
				ExcType:       p.ExceptionType,
				ExitReason:    p.ExitReason,
				FirstSeenMS:   e.OccurredAtMS,
				LastSeenMS:    e.OccurredAtMS,
				Installs:      map[string]struct{}{},
				Releases:      map[string]struct{}{},
				Devices:       map[string]int64{},
				OSVersions:    map[string]int64{},
				ReleaseCounts: map[string]int64{},
				Days:          map[string]int64{},
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
		relTag := r.AppVersion + "(" + r.BuildNumber + ")"
		ib.Releases[relTag] = struct{}{}
		ib.ReleaseCounts[relTag]++
		if r.DeviceModel != "" {
			ib.Devices[r.DeviceModel]++
		}
		if r.OSVersion != "" {
			ib.OSVersions[r.OSVersion]++
		}
		if r.UserHash != "" {
			ib.SampleUserHash = r.UserHash
		}
		if r.InstallHash != "" && ib.SampleInstallHash == "" {
			ib.SampleInstallHash = r.InstallHash
		}
		ib.Days[dayUTC]++
		if len(p.Frames) > 0 && len(ib.Frames) == 0 {
			ib.Frames = append([]protocol.StackFrame(nil), p.Frames...)
		}
		if len(p.Breadcrumbs) > 0 && len(ib.Breadcrumbs) == 0 {
			ib.Breadcrumbs = append([]protocol.Breadcrumb(nil), p.Breadcrumbs...)
		}

	case protocol.CPUProfilePayload:
		rec := CPUProfileRecord{
			ProfileID:        e.EventID,
			OccurredAtMS:     e.OccurredAtMS,
			AppID:            r.AppID,
			Platform:         r.Platform,
			Env:              r.Env,
			AppVersion:       r.AppVersion,
			BuildNumber:      r.BuildNumber,
			Scene:            scene,
			Runtime:          p.Runtime,
			ThreadName:       p.ThreadName,
			SampleIntervalMS: p.SampleIntervalMS,
			DurationMS:       p.DurationMS,
			TotalSamples:     p.TotalSamples,
			Stacks:           append([]protocol.ProfileStackSample(nil), p.Stacks...),
		}
		s.cpuProfiles = append(s.cpuProfiles, rec)
		if len(s.cpuProfiles) > maxStoredDiagnostics {
			s.cpuProfiles = s.cpuProfiles[len(s.cpuProfiles)-maxStoredDiagnostics:]
		}

	case protocol.HeapSnapshotPayload:
		rec := HeapSnapshotRecord{
			SnapshotID:        e.EventID,
			OccurredAtMS:      e.OccurredAtMS,
			AppID:             r.AppID,
			Platform:          r.Platform,
			Env:               r.Env,
			AppVersion:        r.AppVersion,
			BuildNumber:       r.BuildNumber,
			Scene:             scene,
			TriggerReason:     p.TriggerReason,
			MemoryKind:        p.MemoryKind,
			TotalAllocatedKiB: p.TotalAllocatedKiB,
			TotalRetainedKiB:  p.TotalRetainedKiB,
			MemoryLimitKiB:    p.MemoryLimitKiB,
			Dominators:        append([]protocol.HeapDominator(nil), p.Dominators...),
		}
		s.heapSnapshots = append(s.heapSnapshots, rec)
		if len(s.heapSnapshots) > maxStoredDiagnostics {
			s.heapSnapshots = s.heapSnapshots[len(s.heapSnapshots)-maxStoredDiagnostics:]
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
	if f.DeviceModel != "" && k.DeviceModel != f.DeviceModel {
		return false
	}
	if f.OSVersion != "" && k.OSVersion != f.OSVersion {
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

func statusCodeBucket(code int, result string) string {
	if result == "timeout" {
		return "timeout"
	}
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		if result == "success" {
			return "2xx"
		}
		return "other"
	}
}

func (s *EmbeddedStore) QueryNetwork(_ context.Context, f QueryFilter) (NetworkAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := NetworkAggregate{
		StatusCodes:      map[string]int64{},
		TransportSamples: map[string]*TransportOpAggregate{},
		PolicyIDs:        map[string]struct{}{},
	}
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
		for code, cnt := range agg.StatusCodes {
			out.StatusCodes[code] += cnt
		}
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

func (s *EmbeddedStore) QueryNetworkSamples(_ context.Context, f QueryFilter, operation string, onlyIssues bool, limit int) ([]NetworkTransportSample, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := make([]NetworkTransportSample, 0, limit)
	for i := len(s.networkSamples) - 1; i >= 0; i-- {
		smp := s.networkSamples[i]
		if f.AppID != "" && smp.AppID != f.AppID {
			continue
		}
		if f.Platform != "" && smp.Platform != f.Platform {
			continue
		}
		if f.Env != "" && smp.Env != f.Env {
			continue
		}
		if f.AppVersion != "" && smp.AppVersion != f.AppVersion {
			continue
		}
		if f.BuildNumber != "" && smp.BuildNumber != f.BuildNumber {
			continue
		}
		if f.Adapter != "" && smp.Adapter != f.Adapter {
			continue
		}
		if f.Scene != "" && smp.Scene != f.Scene {
			continue
		}
		if f.DeviceModel != "" && smp.DeviceModel != f.DeviceModel {
			continue
		}
		if f.OSVersion != "" && smp.OSVersion != f.OSVersion {
			continue
		}
		if operation != "" && smp.Operation != operation {
			continue
		}
		dayUTC := time.UnixMilli(smp.OccurredAtMS).UTC().Format("2006-01-02")
		if f.StartDayUTC != "" && dayUTC < f.StartDayUTC {
			continue
		}
		if f.EndDayUTC != "" && dayUTC > f.EndDayUTC {
			continue
		}
		if onlyIssues && smp.Result == "success" && smp.StatusCode < 400 && smp.TotalMS < 800 {
			continue
		}
		out = append(out, smp)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *EmbeddedStore) QueryCustomScenes(_ context.Context, f QueryFilter) ([]CustomSceneAggregate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grouped := map[string]*CustomSceneAggregate{}
	for k, agg := range s.customScenes {
		sub := f
		sub.Scene = ""
		if !matchesFilter(k, sub) {
			continue
		}
		dst := grouped[agg.JourneyName]
		if dst == nil {
			dst = &CustomSceneAggregate{
				JourneyName: agg.JourneyName,
				StageHists:  map[string]contract.Histogram{},
				ErrorCodes:  map[string]int64{},
				PolicyIDs:   map[string]struct{}{},
			}
			grouped[agg.JourneyName] = dst
		}
		dst.TotalCount += agg.TotalCount
		dst.SuccessCount += agg.SuccessCount
		dst.FailureCount += agg.FailureCount
		dst.TimeoutCount += agg.TimeoutCount
		dst.CancelledCount += agg.CancelledCount
		dst.DurationHist.Add(agg.DurationHist)
		for _, stName := range agg.StageOrder {
			h, exists := dst.StageHists[stName]
			if !exists {
				dst.StageOrder = append(dst.StageOrder, stName)
			}
			h.Add(agg.StageHists[stName])
			dst.StageHists[stName] = h
		}
		for ec, cnt := range agg.ErrorCodes {
			dst.ErrorCodes[ec] += cnt
		}
		for pid := range agg.PolicyIDs {
			dst.PolicyIDs[pid] = struct{}{}
		}
	}
	out := make([]CustomSceneAggregate, 0, len(grouped))
	for _, v := range grouped {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalCount != out[j].TotalCount {
			return out[i].TotalCount > out[j].TotalCount
		}
		return out[i].JourneyName < out[j].JourneyName
	})
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
		out = append(out, s.toIssueSummaryLocked(ib))
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
		summary := s.toIssueSummaryLocked(ib)
		return &summary, true, nil
	}
	return nil, false, nil
}

func cloneCounts(src map[string]int64) map[string]int64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (s *EmbeddedStore) toIssueSummaryLocked(ib *issueBucket) IssueSummary {
	rels := make([]string, 0, len(ib.Releases))
	for rel := range ib.Releases {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	sum := IssueSummary{
		Fingerprint:       ib.Fingerprint,
		Kind:              ib.Kind,
		Title:             ib.Title,
		Fatal:             ib.Fatal,
		ExceptionType:     ib.ExcType,
		ExitReason:        ib.ExitReason,
		EventCount:        ib.EventCount,
		AffectedInstalls:  int64(len(ib.Installs)),
		FirstSeenMS:       ib.FirstSeenMS,
		LastSeenMS:        ib.LastSeenMS,
		Releases:          rels,
		DeviceBreakdown:   cloneCounts(ib.Devices),
		OSBreakdown:       cloneCounts(ib.OSVersions),
		ReleaseBreakdown:  cloneCounts(ib.ReleaseCounts),
		SampleUserHash:    ib.SampleUserHash,
		SampleInstallHash: ib.SampleInstallHash,
		Status:            "open",
		SampleFrames:      ib.Frames,
		SampleCrumbs:      ib.Breadcrumbs,
	}
	if st, ok := s.issueStates[ib.Fingerprint]; ok {
		if st.Status != "" {
			sum.Status = st.Status
		}
		sum.Assignee = st.Assignee
		sum.Note = st.Note
		sum.ResolvedInVersion = st.ResolvedInVersion
		sum.StateUpdatedAtMS = st.UpdatedAtMS
	}
	return sum
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

func (s *EmbeddedStore) SaveSymbol(ctx context.Context, sym SymbolRecord, content []byte) (SymbolRecord, error) {
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

	s.mu.RLock()
	objStore := s.objectStore
	s.mu.RUnlock()

	if objStore != nil {
		objKey := "symbols/" + sym.SymbolID + ".sym"
		if err := objStore.PutObject(ctx, objKey, content, "application/octet-stream"); err != nil {
			return SymbolRecord{}, fmt.Errorf("object store put symbol: %w", err)
		}
		sym.StoragePath = "obj://" + objKey
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if objStore == nil {
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

func (s *EmbeddedStore) GetSymbolContent(ctx context.Context, symbolID string) (SymbolRecord, []byte, bool, error) {
	s.mu.RLock()
	sym, ok := s.symbols[symbolID]
	memData := s.symbolBytes[symbolID]
	objStore := s.objectStore
	s.mu.RUnlock()
	if !ok {
		return SymbolRecord{}, nil, false, nil
	}
	if strings.HasPrefix(sym.StoragePath, "obj://") {
		if objStore == nil {
			return sym, nil, false, errors.New("symbol stored in external object store but none is configured")
		}
		raw, found, err := objStore.GetObject(ctx, strings.TrimPrefix(sym.StoragePath, "obj://"))
		return sym, raw, found, err
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

func (s *EmbeddedStore) DeleteAlertRule(_ context.Context, ruleID string) error {
	if ruleID == "" {
		return errors.New("rule_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.alertRules[ruleID]; !ok {
		return errors.New("alert rule not found")
	}
	delete(s.alertRules, ruleID)
	return s.saveMetaLocked()
}

func (s *EmbeddedStore) UpsertIssueState(_ context.Context, st IssueState) (IssueState, error) {
	if st.Fingerprint == "" {
		return IssueState{}, errors.New("fingerprint is required")
	}
	switch st.Status {
	case "open", "in_progress", "resolved", "ignored":
	default:
		return IssueState{}, fmt.Errorf("invalid issue status %q (expected open, in_progress, resolved, or ignored)", st.Status)
	}
	st.UpdatedAtMS = time.Now().UTC().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issueStates[st.Fingerprint] = st
	if err := s.saveMetaLocked(); err != nil {
		return IssueState{}, err
	}
	return st, nil
}

func (s *EmbeddedStore) GetIssueState(_ context.Context, fingerprint string) (IssueState, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.issueStates[fingerprint]
	return st, ok, nil
}

func (s *EmbeddedStore) ListIssueStates(_ context.Context) ([]IssueState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]IssueState, 0, len(s.issueStates))
	for _, st := range s.issueStates {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out, nil
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

func (s *EmbeddedStore) ListDimensionValues(_ context.Context, f QueryFilter, dimension string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]struct{}{}
	collect := func(k rollupKey) {
		if !matchesFilter(k, f) {
			return
		}
		var v string
		switch dimension {
		case "device":
			v = k.DeviceModel
		case "os":
			v = k.OSVersion
		}
		if v != "" {
			seen[v] = struct{}{}
		}
	}
	for k := range s.startups {
		collect(k)
	}
	for k := range s.fluencies {
		collect(k)
	}
	for k := range s.resources {
		collect(k)
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
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

// SummarizeEvent creates a concise, non-PII diagnostic summary for session timeline views.
func SummarizeEvent(kind string, parsed any) string {
	switch p := parsed.(type) {
	case protocol.StartupPayload:
		return fmt.Sprintf("cold_start=%.0fms result=%s", p.TotalDurationMS, p.Result)
	case protocol.StabilityPayload:
		return fmt.Sprintf("%s fatal=%v title=%s", kind, p.Fatal, p.Title)
	case protocol.FluencyWindowPayload:
		return fmt.Sprintf("frames=%d jank=%d algo=%s", p.FrameCount, p.JankFrameCount, p.Algorithm)
	case protocol.ResourceWindowPayload:
		return fmt.Sprintf("cpu=%.1f%% mem_peak=%.1fMiB (%s)", p.CPUPct, p.MemoryPeakKiB/1024.0, p.MemoryKind)
	case protocol.JSLongTaskPayload:
		return fmt.Sprintf("longtasks=%d blocking=%.0fms max=%.0fms", p.LongTaskCount, p.TotalBlockingMS, p.MaxTaskMS)
	case protocol.NetworkSummaryPayload:
		return fmt.Sprintf("calls=%d failed=%d", p.CallCount, p.FailureCounts.Total())
	case protocol.NetworkTransportPayload:
		return fmt.Sprintf("%s %s status=%d total=%.0fms result=%s", p.Method, p.Operation, p.StatusCode, p.TotalMS, p.Result)
	case protocol.CustomScenePayload:
		return fmt.Sprintf("journey=%s status=%s", p.JourneyName, p.Status)
	case protocol.CPUProfilePayload:
		return fmt.Sprintf("cpu_profile runtime=%s samples=%d duration=%dms", p.Runtime, p.TotalSamples, p.DurationMS)
	case protocol.HeapSnapshotPayload:
		return fmt.Sprintf("heap_snapshot trigger=%s retained=%.1fMiB dominators=%d", p.TriggerReason, p.TotalRetainedKiB/1024.0, len(p.Dominators))
	default:
		return kind
	}
}

func (s *EmbeddedStore) indexTimelineLocked(r protocol.Resource, e protocol.Event, adapter, scene string, parsed any) {
	item := SessionTimelineEvent{
		EventID:      e.EventID,
		OccurredAtMS: e.OccurredAtMS,
		AppID:        r.AppID,
		Platform:     r.Platform,
		Env:          r.Env,
		AppVersion:   r.AppVersion,
		BuildNumber:  r.BuildNumber,
		Adapter:      adapter,
		InstallHash:  r.InstallHash,
		UserHash:     r.UserHash,
		Kind:         e.Kind,
		Scene:        scene,
		Summary:      SummarizeEvent(e.Kind, parsed),
	}
	appendBoundedTimeline(s.installTimelines, r.Platform+"|"+r.InstallHash, item)
	if r.UserHash != "" {
		appendBoundedTimeline(s.userTimelines, r.Platform+"|"+r.UserHash, item)
	}
}

func appendBoundedTimeline(index map[string][]SessionTimelineEvent, key string, item SessionTimelineEvent) {
	if key == "" {
		return
	}
	list, exists := index[key]
	if !exists && len(index) >= maxTrackedTimelineHashes {
		return
	}
	list = append(list, item)
	if len(list) > maxTimelineEventsPerHash {
		list = list[len(list)-maxTimelineEventsPerHash:]
	}
	index[key] = list
}

func (s *EmbeddedStore) QuerySessionTimeline(_ context.Context, f SessionLookupFilter) ([]SessionTimelineEvent, error) {
	if f.InstallHash == "" && f.UserHash == "" {
		return nil, errors.New("install_hash or user_hash is required")
	}
	limit := f.Limit
	if limit <= 0 || limit > maxTimelineEventsPerHash {
		limit = 100
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	platforms := []string{"android", "ios"}
	if f.Platform != "" {
		platforms = []string{f.Platform}
	}

	var source []SessionTimelineEvent
	for _, plat := range platforms {
		if f.UserHash != "" {
			source = append(source, s.userTimelines[plat+"|"+f.UserHash]...)
		} else {
			source = append(source, s.installTimelines[plat+"|"+f.InstallHash]...)
		}
	}

	out := make([]SessionTimelineEvent, 0, len(source))
	for _, ev := range source {
		if f.AppID != "" && ev.AppID != f.AppID {
			continue
		}
		if f.Platform != "" && ev.Platform != f.Platform {
			continue
		}
		if f.InstallHash != "" && ev.InstallHash != f.InstallHash {
			continue
		}
		if f.UserHash != "" && ev.UserHash != f.UserHash {
			continue
		}
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OccurredAtMS != out[j].OccurredAtMS {
			return out[i].OccurredAtMS > out[j].OccurredAtMS
		}
		return out[i].EventID < out[j].EventID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *EmbeddedStore) loadAuditLogsLocked() error {
	path := filepath.Join(s.dataDir, "audit.wal.jsonl")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry AuditLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		s.auditLogs = append(s.auditLogs, entry)
	}
	return scanner.Err()
}

func (s *EmbeddedStore) AppendAuditLog(_ context.Context, entry AuditLogEntry) error {
	if entry.Action == "" || entry.Actor == "" || entry.Reason == "" {
		return errors.New("audit log requires action, actor, and reason")
	}
	if entry.TimestampMS == 0 {
		entry.TimestampMS = time.Now().UTC().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.AuditID == "" {
		entry.AuditID = fmt.Sprintf("aud-%d-%d", entry.TimestampMS, len(s.auditLogs)+1)
	}
	s.auditLogs = append(s.auditLogs, entry)
	if s.dataDir != "" {
		f, err := os.OpenFile(filepath.Join(s.dataDir, "audit.wal.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("append audit log: %w", err)
		}
		defer f.Close()
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(raw, '\n')); err != nil {
			return fmt.Errorf("write audit entry: %w", err)
		}
	}
	return nil
}

func (s *EmbeddedStore) ListAuditLogs(_ context.Context, limit int) ([]AuditLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := len(s.auditLogs)
	if n == 0 {
		return []AuditLogEntry{}, nil
	}
	out := make([]AuditLogEntry, 0, min(n, limit))
	for i := n - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.auditLogs[i])
	}
	return out, nil
}

func releaseMapKey(appID, platform, env, ver, build string) string {
	return appID + "|" + platform + "|" + env + "|" + ver + "|" + build
}

func issueMapKey(appID, platform, env, fingerprint string) string {
	return appID + "|" + platform + "|" + env + "|" + fingerprint
}

func (s *EmbeddedStore) QueryDailyTrends(_ context.Context, f QueryFilter) ([]DailyTrendPoint, error) {
	type dayAcc struct {
		verCount   map[string]int64
		startHist  contract.Histogram
		startN     int64
		flValidWin int64
		flFrames   int64
		flJank     int64
		flAlgos    map[string]struct{}
		resWin     int64
		cpuSum     float64
		memHist    contract.Histogram
		netCalls   int64
		netFails   int64
		netHist    contract.Histogram
		crashes    int64
		anrs       int64
		abnormals  int64
	}
	days := map[string]*dayAcc{}
	getDay := func(d string) *dayAcc {
		acc := days[d]
		if acc == nil {
			acc = &dayAcc{verCount: map[string]int64{}, flAlgos: map[string]struct{}{}}
			days[d] = acc
		}
		return acc
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for k, agg := range s.startups {
		if !matchesFilter(k, f) {
			continue
		}
		acc := getDay(k.DayUTC)
		acc.startHist.Add(agg.TotalHist)
		acc.startN += agg.SuccessCount
		acc.verCount[k.AppVersion] += agg.SuccessCount
	}
	for k, agg := range s.fluencies {
		if !matchesFilter(k, f) {
			continue
		}
		acc := getDay(k.DayUTC)
		if agg.ValidWindows > 0 {
			acc.flValidWin += agg.ValidWindows
			acc.flFrames += agg.TotalFrames
			acc.flJank += agg.JankFrames
			acc.flAlgos[agg.Algorithm] = struct{}{}
			acc.verCount[k.AppVersion] += agg.ValidWindows
		}
	}
	for k, agg := range s.resources {
		if !matchesFilter(k, f) {
			continue
		}
		acc := getDay(k.DayUTC)
		acc.resWin += agg.ResourceWindows
		acc.cpuSum += agg.CPUPctSum
		acc.memHist.Add(agg.MemoryPeakHistMiB)
		acc.verCount[k.AppVersion] += agg.ResourceWindows
	}
	for k, agg := range s.networks {
		if !matchesFilter(k, f) {
			continue
		}
		acc := getDay(k.DayUTC)
		acc.netCalls += agg.TotalCalls
		acc.netFails += agg.FailureCounts.Total()
		acc.netHist.Add(agg.DurationHist)
		acc.verCount[k.AppVersion] += agg.SummaryWindows
	}
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
			for rel := range ib.Releases {
				if len(rel) >= len(f.AppVersion) && rel[:len(f.AppVersion)] == f.AppVersion {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		for dayUTC, cnt := range ib.Days {
			if f.StartDayUTC != "" && dayUTC < f.StartDayUTC {
				continue
			}
			if f.EndDayUTC != "" && dayUTC > f.EndDayUTC {
				continue
			}
			acc := getDay(dayUTC)
			switch ib.Kind {
			case protocol.KindCrash:
				acc.crashes += cnt
			case protocol.KindANR:
				acc.anrs += cnt
			case protocol.KindAbnormalExit:
				acc.abnormals += cnt
			}
		}
	}

	dayKeys := make([]string, 0, len(days))
	for d := range days {
		dayKeys = append(dayKeys, d)
	}
	sort.Strings(dayKeys)

	out := make([]DailyTrendPoint, 0, len(dayKeys))
	for _, d := range dayKeys {
		acc := days[d]
		pt := DailyTrendPoint{
			DayUTC:          d,
			StartupSamples:  acc.startN,
			FluencyWindows:  acc.flValidWin,
			APICalls:        acc.netCalls,
			ResourceWindows: acc.resWin,
			CrashEvents:     acc.crashes,
			ANREvents:       acc.anrs,
			AbnormalExits:   acc.abnormals,
		}
		var maxVer string
		var maxCnt int64
		for v, c := range acc.verCount {
			if c > maxCnt || (c == maxCnt && v > maxVer) {
				maxVer = v
				maxCnt = c
			}
		}
		pt.DominantVersion = maxVer

		if q50 := contract.QuantileDuration(acc.startHist, 0.50, contract.DefaultDisplayMinSamples); q50.Value != nil {
			pt.StartupP50MS = q50.Value
		}
		if q90 := contract.QuantileDuration(acc.startHist, 0.90, contract.DefaultDisplayMinSamples); q90.Value != nil {
			pt.StartupP90MS = q90.Value
		}
		if len(acc.flAlgos) == 1 && acc.flFrames > 0 && acc.flValidWin >= contract.DefaultDisplayMinSamples {
			v := (float64(acc.flJank) / float64(acc.flFrames)) * 100.0
			pt.JankRatePct = &v
		}
		if acc.netCalls >= contract.DefaultDisplayMinSamples {
			sr := (float64(acc.netCalls-acc.netFails) / float64(acc.netCalls)) * 100.0
			pt.APISuccessPct = &sr
		}
		if n90 := contract.QuantileDuration(acc.netHist, 0.90, contract.DefaultDisplayMinSamples); n90.Value != nil {
			pt.APIP90MS = n90.Value
		}
		if acc.resWin >= contract.DefaultDisplayMinSamples {
			cpu := acc.cpuSum / float64(acc.resWin)
			pt.AvgCPUPct = &cpu
		}
		if m90 := contract.QuantileMemoryMiB(acc.memHist, 0.90, contract.DefaultDisplayMinSamples); m90.Value != nil {
			pt.MemoryP90MiB = m90.Value
		}
		out = append(out, pt)
	}
	return out, nil
}

func (s *EmbeddedStore) QueryCPUProfiles(_ context.Context, f QueryFilter, runtime string, limit int) ([]CPUProfileRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CPUProfileRecord, 0, min(len(s.cpuProfiles), limit))
	for i := len(s.cpuProfiles) - 1; i >= 0; i-- {
		p := s.cpuProfiles[i]
		if f.AppID != "" && p.AppID != f.AppID {
			continue
		}
		if f.Platform != "" && p.Platform != f.Platform {
			continue
		}
		if f.Env != "" && p.Env != f.Env {
			continue
		}
		if f.AppVersion != "" && p.AppVersion != f.AppVersion {
			continue
		}
		if f.Scene != "" && p.Scene != f.Scene {
			continue
		}
		if runtime != "" && p.Runtime != runtime {
			continue
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *EmbeddedStore) GetCPUProfile(_ context.Context, profileID string) (*CPUProfileRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.cpuProfiles) - 1; i >= 0; i-- {
		if s.cpuProfiles[i].ProfileID == profileID {
			cp := s.cpuProfiles[i]
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

func (s *EmbeddedStore) QueryHeapSnapshots(_ context.Context, f QueryFilter, limit int) ([]HeapSnapshotRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HeapSnapshotRecord, 0, min(len(s.heapSnapshots), limit))
	for i := len(s.heapSnapshots) - 1; i >= 0; i-- {
		h := s.heapSnapshots[i]
		if f.AppID != "" && h.AppID != f.AppID {
			continue
		}
		if f.Platform != "" && h.Platform != f.Platform {
			continue
		}
		if f.Env != "" && h.Env != f.Env {
			continue
		}
		if f.AppVersion != "" && h.AppVersion != f.AppVersion {
			continue
		}
		if f.Scene != "" && h.Scene != f.Scene {
			continue
		}
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *EmbeddedStore) CreateRemoteLogTask(_ context.Context, task RemoteLogTask) (RemoteLogTask, error) {
	if task.TargetHash == "" || task.Reason == "" || task.RequestedBy == "" {
		return RemoteLogTask{}, errors.New("target_hash, reason, and requested_by are required")
	}
	if task.TargetType != "install_hash" && task.TargetType != "user_hash" {
		return RemoteLogTask{}, errors.New("target_type must be install_hash or user_hash")
	}
	if task.Platform != "android" && task.Platform != "ios" {
		return RemoteLogTask{}, errors.New("platform must be android or ios")
	}
	if task.MaxBytes <= 0 || task.MaxBytes > protocol.MaxPayloadBytes {
		task.MaxBytes = 512 * 1024 // 512 KiB default bounded cap
	}
	now := time.Now().UTC().UnixMilli()
	if task.CreatedAtMS == 0 {
		task.CreatedAtMS = now
	}
	if task.ExpiresAtMS <= task.CreatedAtMS {
		task.ExpiresAtMS = task.CreatedAtMS + 3600_000 // 1 hour default TTL
	}
	task.Status = "pending"

	s.mu.Lock()
	defer s.mu.Unlock()
	if task.TaskID == "" {
		task.TaskID = fmt.Sprintf("rlog-%d-%d", task.CreatedAtMS, len(s.remoteLogTasks)+1)
	}
	s.remoteLogTasks[task.TaskID] = task
	if err := s.saveMetaLocked(); err != nil {
		return RemoteLogTask{}, err
	}
	return task, nil
}

func (s *EmbeddedStore) ListRemoteLogTasks(_ context.Context, appID, platform string) ([]RemoteLogTask, error) {
	now := time.Now().UTC().UnixMilli()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RemoteLogTask, 0, len(s.remoteLogTasks))
	for _, t := range s.remoteLogTasks {
		if appID != "" && t.AppID != appID {
			continue
		}
		if platform != "" && t.Platform != platform {
			continue
		}
		if t.Status == "pending" && now > t.ExpiresAtMS {
			t.Status = "expired"
		}
		t.StoragePath = ""
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMS != out[j].CreatedAtMS {
			return out[i].CreatedAtMS > out[j].CreatedAtMS
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out, nil
}

func (s *EmbeddedStore) PollPendingLogTasks(_ context.Context, appID, platform, installHash, userHash string, nowMS int64) ([]RemoteLogTask, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UTC().UnixMilli()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RemoteLogTask, 0)
	for _, t := range s.remoteLogTasks {
		if t.Status != "pending" || nowMS > t.ExpiresAtMS {
			continue
		}
		if appID != "" && t.AppID != "" && t.AppID != appID {
			continue
		}
		if platform != "" && t.Platform != platform {
			continue
		}
		if t.TargetType == "install_hash" && installHash != "" && t.TargetHash == installHash {
			t.StoragePath = ""
			out = append(out, t)
		} else if t.TargetType == "user_hash" && userHash != "" && t.TargetHash == userHash {
			t.StoragePath = ""
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *EmbeddedStore) CompleteRemoteLogTask(ctx context.Context, taskID string, content []byte, sha256Hex string, nowMS int64) (RemoteLogTask, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UTC().UnixMilli()
	}
	s.mu.RLock()
	objStore := s.objectStore
	taskSnapshot, ok := s.remoteLogTasks[taskID]
	s.mu.RUnlock()
	if !ok {
		return RemoteLogTask{}, errors.New("remote log task not found")
	}
	if taskSnapshot.Status != "pending" {
		return RemoteLogTask{}, fmt.Errorf("remote log task %s already %s", taskID, taskSnapshot.Status)
	}
	if nowMS <= taskSnapshot.ExpiresAtMS && int64(len(content)) <= taskSnapshot.MaxBytes && objStore != nil {
		objKey := "remotelog/" + taskID + ".log"
		if err := objStore.PutObject(ctx, objKey, content, "text/plain; charset=utf-8"); err != nil {
			return RemoteLogTask{}, fmt.Errorf("object store put remotelog: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.remoteLogTasks[taskID]
	if !ok {
		return RemoteLogTask{}, errors.New("remote log task not found")
	}
	if task.Status != "pending" {
		return RemoteLogTask{}, fmt.Errorf("remote log task %s already %s", taskID, task.Status)
	}
	if nowMS > task.ExpiresAtMS {
		task.Status = "expired"
		s.remoteLogTasks[taskID] = task
		_ = s.saveMetaLocked()
		return RemoteLogTask{}, errors.New("remote log task has expired")
	}
	if int64(len(content)) > task.MaxBytes {
		return RemoteLogTask{}, fmt.Errorf("log payload %d bytes exceeds task max_bytes %d", len(content), task.MaxBytes)
	}

	if objStore != nil {
		task.StoragePath = "obj://remotelog/" + taskID + ".log"
	} else if s.dataDir != "" {
		dir := filepath.Join(s.dataDir, "remotelog")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return RemoteLogTask{}, fmt.Errorf("mkdir remotelog: %w", err)
		}
		path := filepath.Join(dir, taskID+".log")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return RemoteLogTask{}, fmt.Errorf("write remotelog: %w", err)
		}
		task.StoragePath = path
	} else {
		s.remoteLogBytes[taskID] = append([]byte(nil), content...)
	}

	task.Status = "completed"
	task.CompletedAtMS = nowMS
	task.UploadedBytes = int64(len(content))
	task.SHA256 = sha256Hex
	s.remoteLogTasks[taskID] = task
	if err := s.saveMetaLocked(); err != nil {
		return RemoteLogTask{}, err
	}
	task.StoragePath = ""
	return task, nil
}

func (s *EmbeddedStore) GetRemoteLogContent(ctx context.Context, taskID string) (RemoteLogTask, []byte, bool, error) {
	s.mu.RLock()
	task, ok := s.remoteLogTasks[taskID]
	memData := s.remoteLogBytes[taskID]
	objStore := s.objectStore
	s.mu.RUnlock()
	if !ok {
		return RemoteLogTask{}, nil, false, nil
	}
	if task.Status != "completed" {
		task.StoragePath = ""
		return task, nil, true, nil
	}
	if strings.HasPrefix(task.StoragePath, "obj://") {
		if objStore == nil {
			return task, nil, false, errors.New("remote log stored in external object store but none is configured")
		}
		raw, found, err := objStore.GetObject(ctx, strings.TrimPrefix(task.StoragePath, "obj://"))
		task.StoragePath = ""
		return task, raw, found, err
	}
	if task.StoragePath != "" {
		raw, err := os.ReadFile(task.StoragePath)
		if err != nil {
			return task, nil, false, err
		}
		task.StoragePath = ""
		return task, raw, true, nil
	}
	task.StoragePath = ""
	return task, memData, true, nil
}
