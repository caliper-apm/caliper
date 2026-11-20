package profiler

import (
	"context"
	"math"
	"sort"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/symbolicator"
)

// FlameNode represents one frame node in a hierarchical Native or Hermes CPU flamegraph.
type FlameNode struct {
	Name         string       `json:"name"`
	Depth        int          `json:"depth"`
	SelfSamples  int64        `json:"self_samples"`
	TotalSamples int64        `json:"total_samples"`
	SelfPct      float64      `json:"self_pct"`
	TotalPct     float64      `json:"total_pct"`
	Children     []*FlameNode `json:"children,omitempty"`
}

// FlamegraphReport is the symbolicated flamegraph tree returned to the Console & CLI.
type FlamegraphReport struct {
	ProfileID        string    `json:"profile_id"`
	Platform         string    `json:"platform"`
	AppVersion       string    `json:"app_version"`
	BuildNumber      string    `json:"build_number"`
	Scene            string    `json:"scene"`
	Runtime          string    `json:"runtime"` // native | hermes
	ThreadName       string    `json:"thread_name,omitempty"`
	SampleIntervalMS int       `json:"sample_interval_ms"`
	DurationMS       int64     `json:"duration_ms"`
	TotalSamples     int64     `json:"total_samples"`
	Symbolicated     bool      `json:"symbolicated"`
	Root             FlameNode `json:"root"`
}

// BuildFlamegraph resolves obfuscated frames via sym (if available) and merges folded stacks into a tree.
func BuildFlamegraph(ctx context.Context, rec storage.CPUProfileRecord, sym *symbolicator.Service) FlamegraphReport {
	scope := symbolicator.LookupScope{
		AppID:       rec.AppID,
		Platform:    rec.Platform,
		AppVersion:  rec.AppVersion,
		BuildNumber: rec.BuildNumber,
	}

	type mutableNode struct {
		name         string
		depth        int
		selfSamples  int64
		totalSamples int64
		children     map[string]*mutableNode
	}
	root := &mutableNode{name: "root (" + rec.Runtime + ")", depth: 0, children: map[string]*mutableNode{}}
	var totalSamples int64
	symbolicatedAny := false

	for _, sample := range rec.Stacks {
		frames := sample.Frames
		if sym != nil && len(frames) > 0 {
			if resolved, resolvedCnt := sym.ResolveLines(ctx, scope, frames); resolvedCnt > 0 {
				frames = resolved
				symbolicatedAny = true
			}
		}
		root.totalSamples += sample.Samples
		totalSamples += sample.Samples
		curr := root
		for i, frameName := range frames {
			child, ok := curr.children[frameName]
			if !ok {
				child = &mutableNode{
					name:     frameName,
					depth:    i + 1,
					children: map[string]*mutableNode{},
				}
				curr.children[frameName] = child
			}
			child.totalSamples += sample.Samples
			if i == len(frames)-1 {
				child.selfSamples += sample.Samples
			}
			curr = child
		}
	}
	if totalSamples == 0 {
		totalSamples = rec.TotalSamples
	}

	var freeze func(m *mutableNode) FlameNode
	freeze = func(m *mutableNode) FlameNode {
		node := FlameNode{
			Name:         m.name,
			Depth:        m.depth,
			SelfSamples:  m.selfSamples,
			TotalSamples: m.totalSamples,
		}
		if totalSamples > 0 {
			node.SelfPct = round2((float64(m.selfSamples) / float64(totalSamples)) * 100.0)
			node.TotalPct = round2((float64(m.totalSamples) / float64(totalSamples)) * 100.0)
		}
		if len(m.children) > 0 {
			kids := make([]*FlameNode, 0, len(m.children))
			for _, ch := range m.children {
				frozen := freeze(ch)
				kids = append(kids, &frozen)
			}
			sort.Slice(kids, func(i, j int) bool {
				if kids[i].TotalSamples != kids[j].TotalSamples {
					return kids[i].TotalSamples > kids[j].TotalSamples
				}
				return kids[i].Name < kids[j].Name
			})
			node.Children = kids
		}
		return node
	}

	return FlamegraphReport{
		ProfileID:        rec.ProfileID,
		Platform:         rec.Platform,
		AppVersion:       rec.AppVersion,
		BuildNumber:      rec.BuildNumber,
		Scene:            rec.Scene,
		Runtime:          rec.Runtime,
		ThreadName:       rec.ThreadName,
		SampleIntervalMS: rec.SampleIntervalMS,
		DurationMS:       rec.DurationMS,
		TotalSamples:     totalSamples,
		Symbolicated:     symbolicatedAny,
		Root:             freeze(root),
	}
}

// HeapDominatorDiff summarizes retained-memory growth per class between baseline and candidate builds.
type HeapDominatorDiff struct {
	ClassName          string  `json:"class_name"`
	BaselineMiB        float64 `json:"baseline_mib"`
	CandidateMiB       float64 `json:"candidate_mib"`
	DeltaMiB           float64 `json:"delta_mib"`
	BaselineInstances  int64   `json:"baseline_instances"`
	CandidateInstances int64   `json:"candidate_instances"`
	Suspect            bool    `json:"suspect"`
}

// HeapCompareReport compares trimmed heap snapshots across two releases to isolate OOM leak suspects.
type HeapCompareReport struct {
	Platform          string              `json:"platform"`
	BaselineVersion   string              `json:"baseline_version"`
	CandidateVersion  string              `json:"candidate_version"`
	BaselineRetained  float64             `json:"baseline_retained_mib"`
	CandidateRetained float64             `json:"candidate_retained_mib"`
	DeltaRetainedMiB  float64             `json:"delta_retained_mib"`
	DominatorDiffs    []HeapDominatorDiff `json:"dominator_diffs"`
}

// CompareHeapSnapshots computes class-level retained memory deltas and flags leak suspects (>= +16 MiB).
func CompareHeapSnapshots(
	ctx context.Context,
	platform, baseVer, candVer string,
	baseSnaps, candSnaps []storage.HeapSnapshotRecord,
	sym *symbolicator.Service,
) HeapCompareReport {
	baseAvgRet, baseClasses := aggregateDominators(ctx, baseSnaps, sym)
	candAvgRet, candClasses := aggregateDominators(ctx, candSnaps, sym)

	allClasses := map[string]struct{}{}
	for cls := range baseClasses {
		allClasses[cls] = struct{}{}
	}
	for cls := range candClasses {
		allClasses[cls] = struct{}{}
	}

	diffs := make([]HeapDominatorDiff, 0, len(allClasses))
	for cls := range allClasses {
		b := baseClasses[cls]
		c := candClasses[cls]
		delta := round2(c.mib - b.mib)
		diffs = append(diffs, HeapDominatorDiff{
			ClassName:          cls,
			BaselineMiB:        round2(b.mib),
			CandidateMiB:       round2(c.mib),
			DeltaMiB:           delta,
			BaselineInstances:  b.instances,
			CandidateInstances: c.instances,
			Suspect:            delta >= 16.0,
		})
	}
	sort.Slice(diffs, func(i, j int) bool {
		if diffs[i].DeltaMiB != diffs[j].DeltaMiB {
			return diffs[i].DeltaMiB > diffs[j].DeltaMiB
		}
		return diffs[i].ClassName < diffs[j].ClassName
	})

	return HeapCompareReport{
		Platform:          platform,
		BaselineVersion:   baseVer,
		CandidateVersion:  candVer,
		BaselineRetained:  round2(baseAvgRet),
		CandidateRetained: round2(candAvgRet),
		DeltaRetainedMiB:  round2(candAvgRet - baseAvgRet),
		DominatorDiffs:    diffs,
	}
}

type classRetention struct {
	mib       float64
	instances int64
}

func aggregateDominators(ctx context.Context, snaps []storage.HeapSnapshotRecord, sym *symbolicator.Service) (float64, map[string]classRetention) {
	if len(snaps) == 0 {
		return 0, map[string]classRetention{}
	}
	var totalRetainedKiB float64
	sums := map[string]struct {
		kib float64
		cnt int64
	}{}
	for _, s := range snaps {
		totalRetainedKiB += s.TotalRetainedKiB
		scope := symbolicator.LookupScope{
			AppID:       s.AppID,
			Platform:    s.Platform,
			AppVersion:  s.AppVersion,
			BuildNumber: s.BuildNumber,
		}
		for _, d := range s.Dominators {
			cls := d.ClassName
			if sym != nil {
				res := sym.ResolveFrames(ctx, scope, []protocol.StackFrame{{Symbol: cls + ".<init>"}})
				if res.ResolvedCount > 0 && len(res.Frames) == 1 {
					symName := res.Frames[0].Symbol
					if idx := len(symName) - len(".<init>"); idx > 0 && symName[idx:] == ".<init>" {
						cls = symName[:idx]
					}
				}
			}
			cur := sums[cls]
			cur.kib += d.RetainedKiB
			cur.cnt += d.InstanceCount
			sums[cls] = cur
		}
	}
	n := float64(len(snaps))
	out := make(map[string]classRetention, len(sums))
	for cls, v := range sums {
		out[cls] = classRetention{
			mib:       (v.kib / n) / 1024.0,
			instances: int64(math.Round(float64(v.cnt) / n)),
		}
	}
	return (totalRetainedKiB / n) / 1024.0, out
}

func round2(v float64) float64 {
	return math.Round(v*100.0) / 100.0
}
