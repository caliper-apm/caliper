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
	Inlined bool
}

// NativeSymbolTable resolves hex instruction addresses from native Mach-O (.dSYM / .dSYM.zip),
// ELF (.so DWARF/symtab), Breakpad (.sym), or JSON dSYM symbol tables.
type NativeSymbolTable struct {
	Format         string // macho-dwarf | macho-symtab | elf-dwarf | elf-symtab | breakpad | json
	ModuleName     string // Optional binary module name (e.g. "CaliperBenchHost" or "libengine.so") for strict frame matching
	BuildUUID      string // Mach-O LC_UUID or ELF .note.gnu.build-id
	ImageBase      uint64 // Mach-O __TEXT vmaddr or ELF executable PT_LOAD vaddr
	FileOffsetBias uint64 // ELF executable PT_LOAD (Vaddr - Off) for /proc/self/maps file-offset resolution (LLD/NDK)
	Entries        []NativeSymbolEntry
	dwarf          *dwarfIndex
}

type jsonNativeSymbol struct {
	Address string `json:"address"`
	Size    uint64 `json:"size"`
	Symbol  string `json:"symbol"`
	File    string `json:"file"`
	Line    int    `json:"line"`
}

// ParseNativeSymbols auto-detects and parses native Mach-O (.dSYM / .dSYM.zip), ELF (.so),
// JSON address maps, or Breakpad .sym text files.
func ParseNativeSymbols(raw []byte) *NativeSymbolTable {
	if len(raw) == 0 {
		return &NativeSymbolTable{}
	}
	if isNativeBinaryFormat(raw) {
		if tbl, err := parseNativeBinary(raw); err == nil && tbl != nil {
			return tbl
		}
	}
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
	tbl.Format = "json"
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
	return &NativeSymbolTable{Format: "breakpad", Entries: entries}
}

// ResolveAddress looks up an address string like "0x100042bc" or "0x42bc" (slide-relative).
// For inlined DWARF locations, it returns the innermost inlined frame; use ResolveAddressChain
// to retrieve all expanded inlined + caller frames.
func (t *NativeSymbolTable) ResolveAddress(addrStr string) (NativeSymbolEntry, bool) {
	chain, ok := t.ResolveAddressChain(addrStr)
	if !ok || len(chain) == 0 {
		return NativeSymbolEntry{}, false
	}
	return chain[0], true
}

// ResolveAddressChain resolves an address to one or more frames (innermost inlined frame first,
// followed by enclosing caller frames with callsite file/line when DWARF inline info is present).
func (t *NativeSymbolTable) ResolveAddressChain(addrStr string) ([]NativeSymbolEntry, bool) {
	if t == nil || (t.dwarf == nil && len(t.Entries) == 0) {
		return nil, false
	}
	addr, ok := parseHexAddress(addrStr)
	if !ok {
		return nil, false
	}

	candidates := []uint64{addr}
	if t.FileOffsetBias > 0 {
		candidates = append(candidates, addr+t.FileOffsetBias)
	}
	if t.ImageBase > 0 && addr < t.ImageBase && t.ImageBase != t.FileOffsetBias {
		candidates = append(candidates, addr+t.ImageBase)
	}

	if t.dwarf != nil {
		for _, a := range candidates {
			if chain, ok := t.dwarf.lookupChain(a); ok {
				return chain, true
			}
		}
	}

	for _, a := range candidates {
		if entry, ok := t.lookupTableEntry(a); ok {
			return []NativeSymbolEntry{entry}, true
		}
	}
	return nil, false
}

func (t *NativeSymbolTable) lookupTableEntry(addr uint64) (NativeSymbolEntry, bool) {
	if len(t.Entries) == 0 {
		return NativeSymbolEntry{}, false
	}
	// Find best matching range containing addr (prefer entry with line number and smallest size).
	var best *NativeSymbolEntry
	hi := sort.Search(len(t.Entries), func(i int) bool { return t.Entries[i].Address > addr })
	for i := hi - 1; i >= 0; i-- {
		e := &t.Entries[i]
		if addr >= e.Address && addr < e.Address+e.Size {
			if best == nil || (e.Line > 0 && best.Line == 0) || ((e.Line > 0) == (best.Line > 0) && e.Size < best.Size) {
				best = e
			}
		}
		if best != nil && addr-e.Address > 1024*1024 {
			break
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
