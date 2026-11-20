package profiler

import (
	"context"
	"strings"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/symbolicator"
)

func TestBuildFlamegraphWithAutoSymbolication(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	r8Map := `com.caliper.demo.feed.FeedImageDecoder -> a.b.c:
    90:95:android.graphics.Bitmap decodeSync(byte[]) -> a
`
	_, err = store.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID: "sym-r8-1", AppID: "dev.caliper.demo", Platform: "android",
		Env: "prod", AppVersion: "1.1.0", BuildNumber: "110", Kind: "r8_mapping", SHA256: "deadbeef",
	}, []byte(r8Map))
	if err != nil {
		t.Fatalf("save symbol: %v", err)
	}

	symSvc := symbolicator.NewService(store)
	rec := storage.CPUProfileRecord{
		ProfileID:        "prof-1",
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		AppVersion:       "1.1.0",
		BuildNumber:      "110",
		Scene:            "home_feed",
		Runtime:          "native",
		SampleIntervalMS: 10,
		DurationMS:       5000,
		TotalSamples:     100,
		Stacks: []protocol.ProfileStackSample{
			{Frames: []string{"android.os.Looper.loop", "a.b.c.a(SourceFile:92)"}, Samples: 75},
			{Frames: []string{"android.os.Looper.loop", "android.view.Choreographer.doFrame"}, Samples: 25},
		},
	}

	fg := BuildFlamegraph(ctx, rec, symSvc)
	if !fg.Symbolicated || fg.TotalSamples != 100 || len(fg.Root.Children) != 1 {
		t.Fatalf("unexpected flamegraph: %+v", fg)
	}
	looper := fg.Root.Children[0]
	if looper.TotalPct != 100.0 || len(looper.Children) != 2 {
		t.Fatalf("unexpected looper node: %+v", looper)
	}
	hot := looper.Children[0]
	if !strings.Contains(hot.Name, "FeedImageDecoder.decodeSync") || hot.SelfPct != 75.0 {
		t.Fatalf("expected symbolicated FeedImageDecoder.decodeSync at 75%%, got %+v", hot)
	}
}

func TestCompareHeapSnapshotsFlagsLeakSuspects(t *testing.T) {
	base := []storage.HeapSnapshotRecord{{
		Platform: "android", AppVersion: "1.0.0", TotalRetainedKiB: 160 * 1024,
		Dominators: []protocol.HeapDominator{
			{ClassName: "BitmapImageCache", InstanceCount: 120, RetainedKiB: 40 * 1024},
		},
	}}
	cand := []storage.HeapSnapshotRecord{{
		Platform: "android", AppVersion: "1.1.0", TotalRetainedKiB: 360 * 1024,
		Dominators: []protocol.HeapDominator{
			{ClassName: "BitmapImageCache", InstanceCount: 980, RetainedKiB: 220 * 1024},
		},
	}}

	diff := CompareHeapSnapshots(context.Background(), "android", "1.0.0", "1.1.0", base, cand, nil)
	if diff.DeltaRetainedMiB != 200.0 || len(diff.DominatorDiffs) != 1 {
		t.Fatalf("unexpected heap diff: %+v", diff)
	}
	if !diff.DominatorDiffs[0].Suspect || diff.DominatorDiffs[0].DeltaMiB != 180.0 {
		t.Fatalf("expected BitmapImageCache flagged as leak suspect (+180 MiB), got %+v", diff.DominatorDiffs[0])
	}
}
