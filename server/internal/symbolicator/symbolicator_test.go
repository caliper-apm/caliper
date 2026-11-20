package symbolicator

import (
	"context"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestR8RetraceExactLineMapping(t *testing.T) {
	mapping := []byte(`# {"id":"com.android.tools.r8.mapping","version":"2.2"}
com.caliper.demo.feed.FeedImageDecoder -> a.b.c:
    # {"id":"sourceFile","fileName":"FeedImageDecoder.kt"}
    10:15:void decodeSync(byte[]):90:95 -> a
    20:20:void clearCache():140 -> b
`)
	m := ParseR8Mapping(mapping)

	sym, file, line, ok := m.ResolveFrame("a.b.c.a", 12)
	if !ok {
		t.Fatal("expected R8 frame match")
	}
	if sym != "com.caliper.demo.feed.FeedImageDecoder.decodeSync" || file != "FeedImageDecoder.kt" || line != 92 {
		t.Fatalf("unexpected R8 resolution: sym=%q file=%q line=%d", sym, file, line)
	}

	resolvedLine, ok := m.ResolveLine("a.b.c.b(SourceFile:20)")
	if !ok || resolvedLine != "com.caliper.demo.feed.FeedImageDecoder.clearCache(FeedImageDecoder.kt:140)" {
		t.Fatalf("unexpected resolved line: %q (ok=%v)", resolvedLine, ok)
	}
}

func TestSourceMapV3VLQDecoding(t *testing.T) {
	// Generated line 1:
	//   col 0 -> src 0, origLine 10 (0-based 9), origCol 4, name 0 ("renderTokenChunk")
	//   VLQ(0)=A, VLQ(0)=A, VLQ(9)=S, VLQ(4)=I, VLQ(0)=A => "AASIA"
	//   col 24 (+24 => wB), src +0 (A), origLine +5 (K), origCol -2 (F), name +1 (C) => "wBAKFC"
	smJSON := []byte(`{
		"version": 3,
		"sources": ["src/screens/StreamingReaderScreen.tsx"],
		"names": ["renderTokenChunk", "parseMarkdownBlock"],
		"mappings": "AASIA,wBAKFC"
	}`)

	sm, err := ParseSourceMapV3(smJSON)
	if err != nil {
		t.Fatalf("parse sourcemap: %v", err)
	}

	file, line, col, sym, ok := sm.Lookup(1, 0)
	if !ok || file != "src/screens/StreamingReaderScreen.tsx" || line != 10 || col != 4 || sym != "renderTokenChunk" {
		t.Fatalf("unexpected first segment: file=%q line=%d col=%d sym=%q ok=%v", file, line, col, sym, ok)
	}

	file2, line2, col2, sym2, ok2 := sm.Lookup(1, 28)
	if !ok2 || file2 != "src/screens/StreamingReaderScreen.tsx" || line2 != 15 || col2 != 2 || sym2 != "parseMarkdownBlock" {
		t.Fatalf("unexpected second segment: file=%q line=%d col=%d sym=%q ok=%v", file2, line2, col2, sym2, ok2)
	}
}

func TestNativeBreakpadAndJSONSymbolTables(t *testing.T) {
	breakpad := []byte(`MODULE mac arm64 DEADBEEF0 DemoApp
FILE 0 Sources/Media/ImageDecoder.swift
FUNC 10004200 100 0 ImageDecoder.decodeFrame(_:)
10004220 20 84 0
10004240 30 89 0
`)
	tbl := ParseNativeSymbols(breakpad)
	entry, ok := tbl.ResolveAddress("0x10004228")
	if !ok || entry.Symbol != "ImageDecoder.decodeFrame(_:)" || entry.File != "Sources/Media/ImageDecoder.swift" || entry.Line != 84 {
		t.Fatalf("unexpected breakpad entry: %+v ok=%v", entry, ok)
	}

	jsonSym := []byte(`[{"address":"0x10008000","size":64,"symbol":"FeedViewController.scrollViewDidScroll","file":"FeedViewController.swift","line":112}]`)
	tbl2 := ParseNativeSymbols(jsonSym)
	entry2, ok2 := tbl2.ResolveAddress("0x10008010")
	if !ok2 || entry2.Symbol != "FeedViewController.scrollViewDidScroll" || entry2.Line != 112 {
		t.Fatalf("unexpected json native entry: %+v ok=%v", entry2, ok2)
	}
}

func TestServiceAutoSymbolicatesFramesAndReportsMissingSymbols(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc := NewService(store)
	ctx := context.Background()

	// Without uploaded symbols -> MUST report missing_symbols honestly.
	missing := svc.ResolveFrames(ctx, LookupScope{
		AppID: "dev.caliper.demo", Platform: "android", AppVersion: "1.1.0", BuildNumber: "110",
	}, []protocol.StackFrame{{Symbol: "a.b.c.a", Line: 12}})
	if missing.Status != "missing_symbols" || missing.ResolvedCount != 0 {
		t.Fatalf("expected missing_symbols, got %+v", missing)
	}

	// Upload R8 mapping + Hermes SourceMap -> must resolve both frames in one pass!
	_, err = store.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID: "sym-r8-110", AppID: "dev.caliper.demo", Platform: "android",
		AppVersion: "1.1.0", BuildNumber: "110", Kind: "r8_mapping", SHA256: "aaa",
	}, []byte("com.caliper.demo.feed.FeedImageDecoder -> a.b.c:\n    10:15:void decodeSync(byte[]):90:95 -> a\n"))
	if err != nil {
		t.Fatalf("save r8: %v", err)
	}
	_, err = store.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID: "sym-js-110", AppID: "dev.caliper.demo", Platform: "android",
		AppVersion: "1.1.0", BuildNumber: "110", Kind: "hermes_sourcemap", SHA256: "bbb",
	}, []byte(`{"version":3,"sources":["src/screens/StreamingReaderScreen.tsx"],"names":["renderTokenChunk"],"mappings":"AASIA"}`))
	if err != nil {
		t.Fatalf("save sourcemap: %v", err)
	}

	res := svc.ResolveFrames(ctx, LookupScope{
		AppID: "dev.caliper.demo", Platform: "android", AppVersion: "1.1.0", BuildNumber: "110",
	}, []protocol.StackFrame{
		{Symbol: "a.b.c.a", Line: 12},
		{Symbol: "at anonymous (index.android.bundle:1:0)"},
	})
	if res.Status != "symbolicated" || res.ResolvedCount != 2 {
		t.Fatalf("expected full symbolication, got %+v", res)
	}
	if res.Frames[0].File != "FeedImageDecoder.kt" || res.Frames[0].Line != 92 {
		t.Fatalf("frame 0 mismatch: %+v", res.Frames[0])
	}
	if res.Frames[1].File != "src/screens/StreamingReaderScreen.tsx" || res.Frames[1].Line != 10 || res.Frames[1].Symbol != "renderTokenChunk" {
		t.Fatalf("frame 1 mismatch: %+v", res.Frames[1])
	}
}
