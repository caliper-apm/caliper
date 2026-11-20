package symbolicator

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// NativeSymbolEntry maps a virtual address range [Address, Address+Size) to a source location.
type NativeSymbolEntry struct {
	Address uint64
	Size    uint64
	Symbol  string
	File    string
	Line    int
}

// NativeSymbolTable resolves hex instruction addresses from Breakpad (.sym) or JSON dSYM symbol tables.
type NativeSymbolTable struct {
	Entries []NativeSymbolEntry
}

type jsonNativeSymbol struct {
	Address string `json:"address"`
	Size    uint64 `json:"size"`
	Symbol  string `json:"symbol"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

// ParseNativeSymbols parses either a JSON dSYM/ELF address map or a Breakpad .sym text file.
func ParseNativeSymbols(raw []byte) *NativeSymbolTable {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return &NativeSymbolTable{}
	}
	if trimmed[0] == '[' {
		return parseJSONNativeSymbols(trimmed)
	}
	return parseBreakpadSymbols(trimmed)
}

func parseJSONNativeSymbols(raw []byte) *NativeSymbolTable {
	var list []jsonNativeSymbol
	if err := json.Unmarshal(raw, &list); err != nil {
		return &NativeSymbolTable{}
	}
	tbl := &NativeSymbolTable{Entries: make([]NativeSymbolEntry, 0, len(list))}
	for _, item := range list {
		addr, ok := parseHexAddress(item.Address)
		if !ok {
			continue
		}
		sz := item.Size
		if sz == 0 {
			sz = 1
		}
		tbl.Entries = append(tbl.Entries, NativeSymbolEntry{
			Address: addr,
			Size:    sz,
			Symbol:  item.Symbol,
			File:    item.File,
			Line:    item.Line,
		})
	}
	sort.Slice(tbl.Entries, func(i, j int) bool { return tbl.Entries[i].Address < tbl.Entries[j].Address })
	return tbl
}

// parseBreakpadSymbols parses standard Breakpad FILE, FUNC, and line records.
func parseBreakpadSymbols(raw []byte) *NativeSymbolTable {
	files := make(map[int]string)
	var entries []NativeSymbolEntry
	var curFunc string

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 32*1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "FILE "):
			parts := strings.SplitN(line, " ", 3)
			if len(parts) == 3 {
				if id, err := strconv.Atoi(parts[1]); err == nil {
					files[id] = parts[2]
				}
			}
		case strings.HasPrefix(line, "FUNC "):
			// FUNC [m] address size parameter_size name
			parts := strings.Fields(line)
			offset := 1
			if len(parts) > 1 && parts[1] == "m" {
				offset = 2
			}
			if len(parts) >= offset+4 {
				addr, errA := strconv.ParseUint(parts[offset], 16, 64)
				sz, errS := strconv.ParseUint(parts[offset+1], 16, 64)
				if errA == nil && errS == nil {
					curFunc = strings.Join(parts[offset+3:], " ")
					entries = append(entries, NativeSymbolEntry{
						Address: addr,
						Size:    maxU64(sz, 1),
						Symbol:  curFunc,
					})
				}
			}
		default:
			// Line record under current FUNC: address size line filenum
			if curFunc == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) == 4 {
				addr, errA := strconv.ParseUint(parts[0], 16, 64)
				sz, errS := strconv.ParseUint(parts[1], 16, 64)
				ln, errL := strconv.Atoi(parts[2])
				fid, errF := strconv.Atoi(parts[3])
				if errA == nil && errS == nil && errL == nil && errF == nil {
					entries = append(entries, NativeSymbolEntry{
						Address: addr,
						Size:    maxU64(sz, 1),
						Symbol:  curFunc,
						File:    files[fid],
						Line:    ln,
					})
				}
			}
		}
	}

	// Prefer more specific (smaller range / has line info) records when sorting same address.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Address != entries[j].Address {
			return entries[i].Address < entries[j].Address
		}
		return entries[i].Line > entries[j].Line
	})
	return &NativeSymbolTable{Entries: entries}
}

// ResolveAddress looks up an address string like "0x100042bc" or "100042bc".
func (t *NativeSymbolTable) ResolveAddress(addrStr string) (NativeSymbolEntry, bool) {
	if t == nil || len(t.Entries) == 0 {
		return NativeSymbolEntry{}, false
	}
	addr, ok := parseHexAddress(addrStr)
	if !ok {
		return NativeSymbolEntry{}, false
	}
	// Find best matching range containing addr (prefer entry with line number and smallest size).
	var best *NativeSymbolEntry
	for i := range t.Entries {
		e := &t.Entries[i]
		if e.Address > addr {
			break
		}
		if addr >= e.Address && addr < e.Address+e.Size {
			if best == nil || (e.Line > 0 && best.Line == 0) || (e.Line > 0 == (best.Line > 0) && e.Size < best.Size) {
				best = e
			}
		}
	}
	if best == nil {
		return NativeSymbolEntry{}, false
	}
	return *best, true
}

func parseHexAddress(raw string) (uint64, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 64)
	return v, err == nil
}

func maxU64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}
