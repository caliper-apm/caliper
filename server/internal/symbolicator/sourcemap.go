package symbolicator

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type rawSourceMapV3 struct {
	Version  int      `json:"version"`
	Sources  []string `json:"sources"`
	Names    []string `json:"names"`
	Mappings string   `json:"mappings"`
}

// SourceMapSegment represents one decoded Source Map v3 VLQ mapping entry.
type SourceMapSegment struct {
	GenCol   int
	SrcIdx   int
	OrigLine int // 1-based
	OrigCol  int // 0-based
	NameIdx  int // -1 if absent
}

// SourceMapV3 holds a pre-decoded Source Map v3 lookup table indexed by 1-based generated line.
type SourceMapV3 struct {
	Sources []string
	Names   []string
	Lines   [][]SourceMapSegment // index = genLine - 1
}

// ParseSourceMapV3 parses a standard JS / Hermes Source Map v3 JSON document.
func ParseSourceMapV3(raw []byte) (*SourceMapV3, error) {
	var doc rawSourceMapV3
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode sourcemap json: %w", err)
	}
	if doc.Version != 3 {
		return nil, fmt.Errorf("unsupported sourcemap version %d", doc.Version)
	}

	lineStrs := strings.Split(doc.Mappings, ";")
	sm := &SourceMapV3{
		Sources: doc.Sources,
		Names:   doc.Names,
		Lines:   make([][]SourceMapSegment, len(lineStrs)),
	}

	srcIdx, origLine0, origCol, nameIdx := 0, 0, 0, 0
	for li, lineStr := range lineStrs {
		if lineStr == "" {
			continue
		}
		genCol := 0
		segStrs := strings.Split(lineStr, ",")
		segs := make([]SourceMapSegment, 0, len(segStrs))
		for _, segStr := range segStrs {
			if segStr == "" {
				continue
			}
			vals, err := decodeVLQSegment(segStr)
			if err != nil {
				return nil, fmt.Errorf("line %d vlq decode: %w", li+1, err)
			}
			if len(vals) < 1 {
				continue
			}
			genCol += vals[0]
			if len(vals) >= 4 {
				srcIdx += vals[1]
				origLine0 += vals[2]
				origCol += vals[3]
				nIdx := -1
				if len(vals) >= 5 {
					nameIdx += vals[4]
					nIdx = nameIdx
				}
				segs = append(segs, SourceMapSegment{
					GenCol:   genCol,
					SrcIdx:   srcIdx,
					OrigLine: origLine0 + 1,
					OrigCol:  origCol,
					NameIdx:  nIdx,
				})
			}
		}
		sort.Slice(segs, func(i, j int) bool { return segs[i].GenCol < segs[j].GenCol })
		sm.Lines[li] = segs
	}
	return sm, nil
}

// Lookup resolves 1-based generated line and 0-based generated column to original source coordinates.
func (sm *SourceMapV3) Lookup(genLine, genCol int) (file string, origLine, origCol int, symbol string, ok bool) {
	if sm == nil || genLine < 1 || genLine > len(sm.Lines) {
		return "", 0, 0, "", false
	}
	segs := sm.Lines[genLine-1]
	if len(segs) == 0 {
		return "", 0, 0, "", false
	}
	if genCol < 0 {
		genCol = 0
	}
	// Binary search for largest segment with GenCol <= genCol.
	idx := sort.Search(len(segs), func(i int) bool { return segs[i].GenCol > genCol }) - 1
	if idx < 0 {
		idx = 0
	}
	seg := segs[idx]
	if seg.SrcIdx < 0 || seg.SrcIdx >= len(sm.Sources) {
		return "", 0, 0, "", false
	}
	file = sm.Sources[seg.SrcIdx]
	if seg.NameIdx >= 0 && seg.NameIdx < len(sm.Names) {
		symbol = sm.Names[seg.NameIdx]
	}
	return file, seg.OrigLine, seg.OrigCol, symbol, true
}

// ParseJSFrameLocation extracts (line, col) from JS stack strings like "at fn (index.bundle:1:42)" or "index.bundle:1:42".
func ParseJSFrameLocation(s string) (line int, col int, ok bool) {
	trimmed := strings.TrimSpace(s)
	trimmed = strings.TrimSuffix(trimmed, ")")
	if idx := strings.LastIndexByte(trimmed, '('); idx >= 0 {
		trimmed = trimmed[idx+1:]
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) < 2 {
		return 0, 0, false
	}
	// Try file:line:col first
	if len(parts) >= 3 {
		l, errL := strconv.Atoi(parts[len(parts)-2])
		c, errC := strconv.Atoi(parts[len(parts)-1])
		if errL == nil && errC == nil && l > 0 && c >= 0 {
			return l, c, true
		}
	}
	// Fallback file:line
	l, errL := strconv.Atoi(parts[len(parts)-1])
	if errL == nil && l > 0 {
		return l, 0, true
	}
	return 0, 0, false
}

const b64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var b64Decode = func() [128]int8 {
	var tbl [128]int8
	for i := range tbl {
		tbl[i] = -1
	}
	for i := 0; i < len(b64Alphabet); i++ {
		tbl[b64Alphabet[i]] = int8(i)
	}
	return tbl
}()

func decodeVLQSegment(seg string) ([]int, error) {
	out := make([]int, 0, 5)
	var result, shift int
	for i := 0; i < len(seg); i++ {
		ch := seg[i]
		if ch >= 128 {
			return nil, errors.New("non-ascii vlq character")
		}
		digit := int(b64Decode[ch])
		if digit < 0 {
			return nil, fmt.Errorf("invalid base64 vlq char %q", ch)
		}
		continuation := (digit & 32) != 0
		digit &= 31
		result += digit << shift
		if continuation {
			shift += 5
			if shift > 30 {
				return nil, errors.New("vlq integer overflow")
			}
			continue
		}
		// Least-significant bit encodes sign in SourceMap VLQ.
		neg := (result & 1) == 1
		val := result >> 1
		if neg {
			val = -val
		}
		out = append(out, val)
		result, shift = 0, 0
	}
	if shift != 0 {
		return nil, errors.New("truncated vlq sequence")
	}
	return out, nil
}
