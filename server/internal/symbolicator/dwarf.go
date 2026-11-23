package symbolicator

import (
	"archive/zip"
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

const maxDecompressedSymbolBytes = 256 * 1024 * 1024 // 256 MiB zip-bomb guard

type dwarfLineRange struct {
	Low  uint64
	High uint64
	File string
	Line int
}

type dwarfFuncRange struct {
	Low     uint64
	High    uint64
	Name    string
	RawName string
}

type dwarfInlineRange struct {
	Low      uint64
	High     uint64
	Name     string
	CallFile string
	CallLine int
	Depth    int
}

type dwarfIndex struct {
	lines   []dwarfLineRange
	funcs   []dwarfFuncRange
	inlines []dwarfInlineRange
}

// isNativeBinaryFormat checks magic headers for ZIP (.dSYM.zip), Mach-O (Thin/Fat), and ELF (.so).
func isNativeBinaryFormat(raw []byte) bool {
	if len(raw) < 4 {
		return false
	}
	// ZIP archive (PK\x03\x04)
	if raw[0] == 'P' && raw[1] == 'K' && raw[2] == 0x03 && raw[3] == 0x04 {
		return true
	}
	// ELF (\x7fELF)
	if raw[0] == 0x7f && raw[1] == 'E' && raw[2] == 'L' && raw[3] == 'F' {
		return true
	}
	be := binary.BigEndian.Uint32(raw[:4])
	le := binary.LittleEndian.Uint32(raw[:4])
	switch be {
	case macho.Magic32, macho.Magic64, macho.MagicFat:
		return true
	}
	switch le {
	case macho.Magic32, macho.Magic64, macho.MagicFat:
		return true
	}
	return false
}

// parseNativeBinary auto-detects ZIP (.dSYM.zip), Mach-O (Thin or Universal/Fat), or ELF (.so)
// and extracts DWARF line/function/inline tables plus symtab fallbacks using pure Go stdlib.
func parseNativeBinary(raw []byte) (*NativeSymbolTable, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("binary too small")
	}
	if raw[0] == 'P' && raw[1] == 'K' && raw[2] == 0x03 && raw[3] == 0x04 {
		return parseZipNativeArchive(raw)
	}
	if raw[0] == 0x7f && raw[1] == 'E' && raw[2] == 'L' && raw[3] == 'F' {
		return parseELFBinary(raw)
	}
	return parseMachOBinary(raw)
}

func parseZipNativeArchive(raw []byte) (*NativeSymbolTable, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("open zip archive: %w", err)
	}
	// Prefer Contents/Resources/DWARF/* files first, then any .so / binary entry.
	type candidate struct {
		f        *zip.File
		priority int
	}
	var cands []candidate
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 == 0 {
			continue
		}
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if strings.HasPrefix(name, "__MACOSX/") || strings.HasSuffix(name, ".plist") {
			continue
		}
		prio := 2
		if strings.Contains(name, "/Contents/Resources/DWARF/") || strings.Contains(name, "Resources/DWARF/") {
			prio = 0
		} else if strings.HasSuffix(strings.ToLower(name), ".so") || strings.HasSuffix(strings.ToLower(name), ".dwarf") {
			prio = 1
		}
		cands = append(cands, candidate{f: f, priority: prio})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].priority < cands[j].priority })

	for _, c := range cands {
		rc, err := c.f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxDecompressedSymbolBytes+1))
		_ = rc.Close()
		if err != nil || len(data) == 0 || len(data) > maxDecompressedSymbolBytes {
			continue
		}
		if len(data) >= 4 && isNativeBinaryFormat(data) && !(data[0] == 'P' && data[1] == 'K') {
			tbl, err := parseNativeBinary(data)
			if err == nil && tbl != nil && (tbl.dwarf != nil || len(tbl.Entries) > 0) {
				if tbl.ModuleName == "" {
					base := strings.ReplaceAll(c.f.Name, "\\", "/")
					if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
						base = base[slash+1:]
					}
					tbl.ModuleName = base
				}
				return tbl, nil
			}
		}
	}
	return nil, fmt.Errorf("no Mach-O or ELF debug binary found inside zip archive")
}

func parseMachOBinary(raw []byte) (*NativeSymbolTable, error) {
	r := bytes.NewReader(raw)
	// Try Fat / Universal Mach-O first (0xcafebabe / 0xbebafeca).
	if fat, err := macho.NewFatFile(r); err == nil {
		defer fat.Close()
		var chosen *macho.File
		for _, arch := range fat.Arches {
			if arch.Cpu == macho.CpuArm64 {
				chosen = arch.File
				break
			}
		}
		if chosen == nil && len(fat.Arches) > 0 {
			chosen = fat.Arches[0].File
		}
		if chosen == nil {
			return nil, fmt.Errorf("empty universal Mach-O archive")
		}
		return buildTableFromMachO(chosen)
	}
	// Thin Mach-O binary.
	mf, err := macho.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse Mach-O: %w", err)
	}
	defer mf.Close()
	return buildTableFromMachO(mf)
}

func buildTableFromMachO(mf *macho.File) (*NativeSymbolTable, error) {
	tbl := &NativeSymbolTable{
		Format:    "macho-symtab",
		BuildUUID: extractMachOUUID(mf),
	}
	if seg := mf.Segment("__TEXT"); seg != nil {
		tbl.ImageBase = seg.Addr
	}
	if d, err := mf.DWARF(); err == nil && d != nil {
		idx := buildDWARFIndex(d)
		if len(idx.lines) > 0 || len(idx.funcs) > 0 {
			tbl.dwarf = idx
			tbl.Format = "macho-dwarf"
		}
	}
	// Always extract Symtab entries as fallback (handles functions without DWARF line info).
	if mf.Symtab != nil && len(mf.Symtab.Syms) > 0 {
		type rawSym struct {
			addr uint64
			name string
		}
		syms := make([]rawSym, 0, len(mf.Symtab.Syms))
		for _, s := range mf.Symtab.Syms {
			// Skip STAB debug symbols (N_STAB = 0xe0) and undefined symbols (Sect == 0).
			if s.Type&0xe0 != 0 || s.Sect == 0 || s.Value == 0 || s.Name == "" {
				continue
			}
			syms = append(syms, rawSym{addr: s.Value, name: DemangleNativeSymbol(s.Name)})
		}
		sort.Slice(syms, func(i, j int) bool { return syms[i].addr < syms[j].addr })
		for i, s := range syms {
			sz := uint64(0x1000)
			if i+1 < len(syms) && syms[i+1].addr > s.addr {
				sz = syms[i+1].addr - s.addr
			}
			tbl.Entries = append(tbl.Entries, NativeSymbolEntry{
				Address: s.addr,
				Size:    maxU64(sz, 1),
				Symbol:  s.name,
			})
		}
	}
	return tbl, nil
}

func extractMachOUUID(mf *macho.File) string {
	const lcUUID = 0x1b
	for _, load := range mf.Loads {
		raw := load.Raw()
		if len(raw) < 24 {
			continue
		}
		cmd := mf.ByteOrder.Uint32(raw[:4])
		if cmd == lcUUID {
			u := raw[8:24]
			return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]))
		}
	}
	return ""
}

func parseELFBinary(raw []byte) (*NativeSymbolTable, error) {
	ef, err := elf.NewFile(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse ELF: %w", err)
	}
	defer ef.Close()

	tbl := &NativeSymbolTable{
		Format:    "elf-symtab",
		BuildUUID: extractELFBuildID(ef),
	}
	if sonames, err := ef.DynString(elf.DT_SONAME); err == nil && len(sonames) > 0 {
		tbl.ModuleName = sonames[0]
	}
	var minExec uint64
	var fileBias uint64
	hasExec := false
	for _, p := range ef.Progs {
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 {
			if !hasExec || p.Vaddr < minExec {
				minExec = p.Vaddr
				if p.Vaddr >= p.Off {
					fileBias = p.Vaddr - p.Off
				}
				hasExec = true
			}
		}
	}
	if hasExec {
		tbl.ImageBase = minExec
		tbl.FileOffsetBias = fileBias
	}

	if d, err := ef.DWARF(); err == nil && d != nil {
		idx := buildDWARFIndex(d)
		if len(idx.lines) > 0 || len(idx.funcs) > 0 {
			tbl.dwarf = idx
			tbl.Format = "elf-dwarf"
		}
	}

	// Collect static + dynamic function symbols as fallback.
	addSyms := func(list []elf.Symbol) {
		for _, s := range list {
			if s.Value == 0 || s.Name == "" {
				continue
			}
			if elf.ST_TYPE(s.Info) != elf.STT_FUNC && elf.ST_TYPE(s.Info) != elf.STT_NOTYPE {
				continue
			}
			tbl.Entries = append(tbl.Entries, NativeSymbolEntry{
				Address: s.Value,
				Size:    maxU64(s.Size, 1),
				Symbol:  DemangleNativeSymbol(s.Name),
			})
		}
	}
	if syms, err := ef.Symbols(); err == nil {
		addSyms(syms)
	}
	if dsyms, err := ef.DynamicSymbols(); err == nil {
		addSyms(dsyms)
	}
	sort.Slice(tbl.Entries, func(i, j int) bool { return tbl.Entries[i].Address < tbl.Entries[j].Address })
	return tbl, nil
}

func extractELFBuildID(ef *elf.File) string {
	sec := ef.Section(".note.gnu.build-id")
	if sec == nil {
		return ""
	}
	data, err := sec.Data()
	if err != nil || len(data) < 16 {
		return ""
	}
	bo := ef.ByteOrder
	nameSz := bo.Uint32(data[0:4])
	descSz := bo.Uint32(data[4:8])
	noteType := bo.Uint32(data[8:12])
	if noteType != 3 || descSz == 0 {
		return ""
	}
	alignedName := (nameSz + 3) &^ 3
	descStart := 12 + int(alignedName)
	descEnd := descStart + int(descSz)
	if descEnd > len(data) {
		return ""
	}
	return hex.EncodeToString(data[descStart:descEnd])
}

type dieNameInfo struct {
	name          string
	linkageName   string
	abstractRef   dwarf.Offset
	hasAbstract   bool
	specRef       dwarf.Offset
	hasSpec       bool
	parentOffset  dwarf.Offset
	hasParent     bool
	tag           dwarf.Tag
}

func buildDWARFIndex(d *dwarf.Data) *dwarfIndex {
	idx := &dwarfIndex{}

	// Pass 1: collect DIE name metadata for abstract_origin / specification / enclosing class resolution.
	dieMeta := make(map[dwarf.Offset]dieNameInfo, 256)
	{
		r := d.Reader()
		var parentStack []dwarf.Offset
		for {
			e, err := r.Next()
			if err != nil || e == nil {
				break
			}
			if e.Tag == 0 {
				if len(parentStack) > 0 {
					parentStack = parentStack[:len(parentStack)-1]
				}
				continue
			}
			info := dieNameInfo{tag: e.Tag}
			if len(parentStack) > 0 {
				info.parentOffset = parentStack[len(parentStack)-1]
				info.hasParent = true
			}
			if v, ok := e.Val(dwarf.AttrName).(string); ok {
				info.name = v
			}
			if v, ok := e.Val(dwarf.AttrLinkageName).(string); ok {
				info.linkageName = v
			} else if v, ok := e.Val(dwarf.Attr(0x2007)).(string); ok { // DW_AT_MIPS_linkage_name
				info.linkageName = v
			}
			if off, ok := e.Val(dwarf.AttrAbstractOrigin).(dwarf.Offset); ok {
				info.abstractRef = off
				info.hasAbstract = true
			}
			if off, ok := e.Val(dwarf.AttrSpecification).(dwarf.Offset); ok {
				info.specRef = off
				info.hasSpec = true
			}
			if info.name != "" || info.linkageName != "" || info.hasAbstract || info.hasSpec ||
				e.Tag == dwarf.TagNamespace || e.Tag == dwarf.TagClassType || e.Tag == dwarf.TagStructType {
				dieMeta[e.Offset] = info
			}
			if e.Children {
				parentStack = append(parentStack, e.Offset)
			}
		}
	}

	resolveDIEName := func(off dwarf.Offset) (demangled string, rawName string) {
		seen := 0
		cur := off
		for seen < 8 {
			seen++
			info, ok := dieMeta[cur]
			if !ok {
				break
			}
			if info.linkageName != "" {
				return DemangleNativeSymbol(info.linkageName), info.linkageName
			}
			if info.name != "" {
				// Build qualified C++ scope (namespace::Class::method) if parent DIEs are namespaces/classes.
				parts := []string{info.name}
				pOff, hasP := info.parentOffset, info.hasParent
				hops := 0
				for hasP && hops < 8 {
					hops++
					pInfo, ok := dieMeta[pOff]
					if !ok {
						break
					}
					if pInfo.name != "" && (pInfo.tag == dwarf.TagNamespace || pInfo.tag == dwarf.TagClassType || pInfo.tag == dwarf.TagStructType) {
						parts = append([]string{pInfo.name}, parts...)
					}
					pOff, hasP = pInfo.parentOffset, pInfo.hasParent
				}
				full := strings.Join(parts, "::")
				return DemangleNativeSymbol(full), full
			}
			if info.hasAbstract {
				cur = info.abstractRef
				continue
			}
			if info.hasSpec {
				cur = info.specRef
				continue
			}
			break
		}
		return "", ""
	}

	// Pass 2: walk CUs for LineReader ranges, subprograms, and inlined subroutines.
	r := d.Reader()
	var curFiles []*dwarf.LineFile
	var depth int
	var inSubprogramDepth int

	for {
		e, err := r.Next()
		if err != nil || e == nil {
			break
		}
		if e.Tag == 0 {
			if depth > 0 {
				depth--
			}
			if depth < inSubprogramDepth {
				inSubprogramDepth = 0
			}
			continue
		}

		switch e.Tag {
		case dwarf.TagCompileUnit:
			curFiles = nil
			if lr, err := d.LineReader(e); err == nil && lr != nil {
				curFiles = lr.Files()
				var le dwarf.LineEntry
				var prev *dwarf.LineEntry
				for {
					err := lr.Next(&le)
					if err != nil {
						break
					}
					if prev != nil && !prev.EndSequence && le.Address > prev.Address && prev.File != nil {
						idx.lines = append(idx.lines, dwarfLineRange{
							Low:  prev.Address,
							High: le.Address,
							File: prev.File.Name,
							Line: prev.Line,
						})
					}
					cp := le
					prev = &cp
				}
			}

		case dwarf.TagSubprogram:
			if ranges, err := d.Ranges(e); err == nil && len(ranges) > 0 {
				name, rawName := resolveDIEName(e.Offset)
				if name != "" {
					for _, rng := range ranges {
						if rng[1] > rng[0] {
							idx.funcs = append(idx.funcs, dwarfFuncRange{
								Low:     rng[0],
								High:    rng[1],
								Name:    name,
								RawName: rawName,
							})
						}
					}
				}
			}
			if e.Children {
				inSubprogramDepth = depth + 1
			}

		case dwarf.TagInlinedSubroutine:
			if ranges, err := d.Ranges(e); err == nil && len(ranges) > 0 {
				name, _ := resolveDIEName(e.Offset)
				if name != "" {
					callLine := 0
					if v, ok := e.Val(dwarf.AttrCallLine).(int64); ok {
						callLine = int(v)
					}
					callFile := ""
					if v, ok := e.Val(dwarf.AttrCallFile).(int64); ok {
						fi := int(v)
						if fi >= 0 && fi < len(curFiles) && curFiles[fi] != nil {
							callFile = curFiles[fi].Name
						}
					}
					inlDepth := depth - inSubprogramDepth + 1
					if inlDepth < 1 {
						inlDepth = 1
					}
					for _, rng := range ranges {
						if rng[1] > rng[0] {
							idx.inlines = append(idx.inlines, dwarfInlineRange{
								Low:      rng[0],
								High:     rng[1],
								Name:     name,
								CallFile: callFile,
								CallLine: callLine,
								Depth:    inlDepth,
							})
						}
					}
				}
			}
		}

		if e.Children {
			depth++
		}
	}

	sort.Slice(idx.lines, func(i, j int) bool {
		if idx.lines[i].Low != idx.lines[j].Low {
			return idx.lines[i].Low < idx.lines[j].Low
		}
		return idx.lines[i].High < idx.lines[j].High
	})
	sort.Slice(idx.funcs, func(i, j int) bool {
		if idx.funcs[i].Low != idx.funcs[j].Low {
			return idx.funcs[i].Low < idx.funcs[j].Low
		}
		return idx.funcs[i].High < idx.funcs[j].High
	})
	sort.Slice(idx.inlines, func(i, j int) bool {
		if idx.inlines[i].Low != idx.inlines[j].Low {
			return idx.inlines[i].Low < idx.inlines[j].Low
		}
		return idx.inlines[i].Depth > idx.inlines[j].Depth
	})
	return idx
}

func (idx *dwarfIndex) lookupChain(addr uint64) ([]NativeSymbolEntry, bool) {
	if idx == nil {
		return nil, false
	}
	lineEntry, hasLine := idx.lookupLine(addr)
	outerFunc, hasFunc := idx.lookupFunc(addr)
	inlines := idx.lookupInlines(addr) // sorted innermost-first (highest Depth first)

	if !hasLine && !hasFunc && len(inlines) == 0 {
		return nil, false
	}

	if len(inlines) == 0 {
		e := NativeSymbolEntry{
			Address: addr,
			Size:    1,
		}
		if hasFunc {
			e.Address = outerFunc.Low
			e.Size = maxU64(outerFunc.High-outerFunc.Low, 1)
			e.Symbol = outerFunc.Name
		}
		if hasLine {
			e.File = lineEntry.File
			e.Line = lineEntry.Line
		}
		return []NativeSymbolEntry{e}, true
	}

	// Build chain: innermost inlined frame gets exact DWARF lineEntry;
	// each outer frame gets the CallFile:CallLine where the next inner frame was inlined!
	chain := make([]NativeSymbolEntry, 0, len(inlines)+1)
	for i, inl := range inlines {
		entry := NativeSymbolEntry{
			Address: inl.Low,
			Size:    maxU64(inl.High-inl.Low, 1),
			Symbol:  inl.Name,
			Inlined: true,
		}
		if i == 0 {
			if hasLine {
				entry.File = lineEntry.File
				entry.Line = lineEntry.Line
			}
		} else {
			prevInl := inlines[i-1]
			entry.File = prevInl.CallFile
			entry.Line = prevInl.CallLine
		}
		chain = append(chain, entry)
	}
	if hasFunc {
		outermostInl := inlines[len(inlines)-1]
		outer := NativeSymbolEntry{
			Address: outerFunc.Low,
			Size:    maxU64(outerFunc.High-outerFunc.Low, 1),
			Symbol:  outerFunc.Name,
			File:    outermostInl.CallFile,
			Line:    outermostInl.CallLine,
			Inlined: false,
		}
		chain = append(chain, outer)
	}
	return chain, true
}

func (idx *dwarfIndex) lookupLine(addr uint64) (dwarfLineRange, bool) {
	if len(idx.lines) == 0 {
		return dwarfLineRange{}, false
	}
	i := sort.Search(len(idx.lines), func(k int) bool { return idx.lines[k].Low > addr }) - 1
	for j := i; j >= 0 && j >= i-4; j-- {
		r := idx.lines[j]
		if addr >= r.Low && addr < r.High {
			return r, true
		}
	}
	return dwarfLineRange{}, false
}

func (idx *dwarfIndex) lookupFunc(addr uint64) (dwarfFuncRange, bool) {
	if len(idx.funcs) == 0 {
		return dwarfFuncRange{}, false
	}
	i := sort.Search(len(idx.funcs), func(k int) bool { return idx.funcs[k].Low > addr }) - 1
	var best *dwarfFuncRange
	for j := i; j >= 0 && j >= i-8; j-- {
		r := &idx.funcs[j]
		if addr >= r.Low && addr < r.High {
			if best == nil || (r.High-r.Low) < (best.High-best.Low) {
				best = r
			}
		}
	}
	if best == nil {
		return dwarfFuncRange{}, false
	}
	return *best, true
}

func (idx *dwarfIndex) lookupInlines(addr uint64) []dwarfInlineRange {
	if len(idx.inlines) == 0 {
		return nil
	}
	var matched []dwarfInlineRange
	// Scan ranges with Low <= addr (binary search upper bound first).
	hi := sort.Search(len(idx.inlines), func(k int) bool { return idx.inlines[k].Low > addr })
	for j := hi - 1; j >= 0; j-- {
		r := idx.inlines[j]
		if addr >= r.Low && addr < r.High {
			matched = append(matched, r)
		}
		// Heuristic early stop once we pass 64 KB behind addr for large binaries.
		if addr > r.Low && addr-r.Low > 1024*1024 && len(matched) > 0 {
			break
		}
	}
	if len(matched) > 1 {
		// Sort innermost (highest Depth, smallest range) first.
		sort.Slice(matched, func(a, b int) bool {
			if matched[a].Depth != matched[b].Depth {
				return matched[a].Depth > matched[b].Depth
			}
			return (matched[a].High - matched[a].Low) < (matched[b].High - matched[b].Low)
		})
	}
	return matched
}
