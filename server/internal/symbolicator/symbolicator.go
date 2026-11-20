package symbolicator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

type parsedBundle struct {
	kind      string
	r8        *R8Mapping
	sourceMap *SourceMapV3
	nativeTbl *NativeSymbolTable
}

// LookupScope identifies which build's uploaded symbols should be consulted.
type LookupScope struct {
	AppID       string
	Platform    string
	AppVersion  string
	BuildNumber string
	SymbolID    string
}

// ResolveResult reports resolved stack frames alongside explicit symbolication coverage status.
type ResolveResult struct {
	Status          string                `json:"symbol_status"` // symbolicated | partial | missing_symbols | unresolved
	ResolvedCount   int                   `json:"resolved_count"`
	TotalFrames     int                   `json:"total_frames"`
	MatchedSymbolID []string              `json:"matched_symbol_ids,omitempty"`
	Frames          []protocol.StackFrame `json:"frames"`
}

// Service caches parsed build-bound symbol bundles and resolves R8, SourceMap v3, and Native frames.
type Service struct {
	meta  storage.MetaStore
	mu    sync.RWMutex
	cache map[string]*parsedBundle // key: symbol_id + ":" + sha256
}

// NewService creates a symbolication service backed by MetaStore.
func NewService(meta storage.MetaStore) *Service {
	return &Service{
		meta:  meta,
		cache: make(map[string]*parsedBundle),
	}
}

// ResolveFrames resolves stack frames against all matching uploaded symbols for scope.
func (s *Service) ResolveFrames(ctx context.Context, scope LookupScope, frames []protocol.StackFrame) ResolveResult {
	outFrames := make([]protocol.StackFrame, len(frames))
	copy(outFrames, frames)
	res := ResolveResult{
		Status:      "missing_symbols",
		TotalFrames: len(outFrames),
		Frames:      outFrames,
	}
	if len(outFrames) == 0 || s == nil || s.meta == nil {
		return res
	}

	bundles, matchedIDs := s.loadBundles(ctx, scope)
	if len(bundles) == 0 {
		return res
	}
	res.MatchedSymbolID = matchedIDs

	resolved := 0
	for i := range outFrames {
		f := &outFrames[i]
		frameResolved := false
		for _, b := range bundles {
			switch b.kind {
			case "r8_mapping":
				if sym, file, ln, ok := b.r8.ResolveFrame(f.Symbol, f.Line); ok {
					f.Symbol = sym
					if file != "" {
						f.File = file
					}
					if ln > 0 {
						f.Line = ln
					}
					frameResolved = true
				}
			case "hermes_sourcemap":
				genLine, genCol, hasLoc := ParseJSFrameLocation(f.Symbol)
				if !hasLoc && f.Line > 0 {
					genLine, genCol, hasLoc = f.Line, 0, true
				}
				if hasLoc {
					if file, origLine, _, sym, ok := b.sourceMap.Lookup(genLine, genCol); ok {
						f.File = file
						f.Line = origLine
						if sym != "" {
							f.Symbol = sym
						}
						frameResolved = true
					}
				}
			case "dsym", "breakpad":
				addr := f.Address
				if addr == "" && strings.HasPrefix(strings.TrimSpace(f.Symbol), "0x") {
					addr = strings.TrimSpace(f.Symbol)
				}
				if addr != "" {
					if entry, ok := b.nativeTbl.ResolveAddress(addr); ok {
						if entry.Symbol != "" {
							f.Symbol = entry.Symbol
						}
						if entry.File != "" {
							f.File = entry.File
						}
						if entry.Line > 0 {
							f.Line = entry.Line
						}
						frameResolved = true
					}
				}
			}
			if frameResolved {
				break
			}
		}
		if frameResolved {
			resolved++
		}
	}

	res.ResolvedCount = resolved
	switch {
	case resolved == len(outFrames):
		res.Status = "symbolicated"
	case resolved > 0:
		res.Status = "partial"
	default:
		res.Status = "unresolved"
	}
	return res
}

// ResolveLines resolves raw stack line strings (such as same-window fluency stall stacks).
func (s *Service) ResolveLines(ctx context.Context, scope LookupScope, lines []string) ([]string, int) {
	if len(lines) == 0 || s == nil || s.meta == nil {
		return lines, 0
	}
	bundles, _ := s.loadBundles(ctx, scope)
	if len(bundles) == 0 {
		return lines, 0
	}

	out := make([]string, len(lines))
	resolved := 0
	for i, raw := range lines {
		line := raw
		matched := false
		for _, b := range bundles {
			switch b.kind {
			case "r8_mapping":
				if next, ok := b.r8.ResolveLine(raw); ok {
					line = next
					matched = true
				}
			case "hermes_sourcemap":
				if gl, gc, ok := ParseJSFrameLocation(raw); ok {
					if file, ol, _, sym, found := b.sourceMap.Lookup(gl, gc); found {
						if sym != "" {
							line = fmt.Sprintf("%s (%s:%d)", sym, file, ol)
						} else {
							line = fmt.Sprintf("%s:%d", file, ol)
						}
						matched = true
					}
				}
			case "dsym", "breakpad":
				trimmed := strings.TrimSpace(raw)
				if entry, ok := b.nativeTbl.ResolveAddress(trimmed); ok {
					if entry.File != "" && entry.Line > 0 {
						line = fmt.Sprintf("%s (%s:%d)", entry.Symbol, entry.File, entry.Line)
					} else {
						line = entry.Symbol
					}
					matched = true
				}
			}
			if matched {
				break
			}
		}
		if matched {
			resolved++
		}
		out[i] = line
	}
	return out, resolved
}

func (s *Service) loadBundles(ctx context.Context, scope LookupScope) ([]*parsedBundle, []string) {
	var records []storage.SymbolRecord
	if scope.SymbolID != "" {
		rec, content, ok, err := s.meta.GetSymbolContent(ctx, scope.SymbolID)
		if err == nil && ok {
			if b := s.getOrParse(rec, content); b != nil {
				return []*parsedBundle{b}, []string{rec.SymbolID}
			}
		}
		return nil, nil
	}

	list, err := s.meta.ListSymbols(ctx, scope.AppID, scope.Platform, scope.AppVersion, scope.BuildNumber)
	if err != nil || len(list) == 0 {
		return nil, nil
	}
	records = list

	bundles := make([]*parsedBundle, 0, len(records))
	ids := make([]string, 0, len(records))
	for _, rec := range records {
		_, content, ok, err := s.meta.GetSymbolContent(ctx, rec.SymbolID)
		if err != nil || !ok {
			continue
		}
		if b := s.getOrParse(rec, content); b != nil {
			bundles = append(bundles, b)
			ids = append(ids, rec.SymbolID)
		}
	}
	return bundles, ids
}

func (s *Service) getOrParse(rec storage.SymbolRecord, content []byte) *parsedBundle {
	cacheKey := rec.SymbolID + ":" + rec.SHA256
	s.mu.RLock()
	cached := s.cache[cacheKey]
	s.mu.RUnlock()
	if cached != nil {
		return cached
	}

	b := &parsedBundle{kind: rec.Kind}
	switch rec.Kind {
	case "r8_mapping":
		b.r8 = ParseR8Mapping(content)
	case "hermes_sourcemap":
		sm, err := ParseSourceMapV3(content)
		if err != nil {
			return nil
		}
		b.sourceMap = sm
	case "dsym", "breakpad":
		b.nativeTbl = ParseNativeSymbols(content)
	default:
		return nil
	}

	s.mu.Lock()
	s.cache[cacheKey] = b
	s.mu.Unlock()
	return b
}
