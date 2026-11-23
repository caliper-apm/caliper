package symbolicator

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestItaniumDemangler(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"__ZN5atlas5media12FrameDecoder12decodePacketEPKhi", "atlas::media::FrameDecoder::decodePacket(const unsigned char*, int)"},
		{"_ZN5atlas5media12FrameDecoderC1Ev", "atlas::media::FrameDecoder::FrameDecoder()"},
		{"_ZN5atlas5media12FrameDecoderD1Ev", "atlas::media::FrameDecoder::~FrameDecoder()"},
		{"_ZNK5atlas4core6Buffer4sizeEv", "atlas::core::Buffer::size() const"},
		{"_ZN5atlas3net10parseChunkERKSs", "atlas::net::parseChunk(const std::string&)"},
		{"_Z12unpack_frameii", "unpack_frame(int, int)"},
		{"_render_native_frame", "render_native_frame"},
		{"-[AtlasFeedController viewDidLoad]", "-[AtlasFeedController viewDidLoad]"},
		{"+[CaliperCache shared]", "+[CaliperCache shared]"},
	}
	for _, tc := range cases {
		got := DemangleNativeSymbol(tc.raw)
		if got != tc.want {
			t.Errorf("DemangleNativeSymbol(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestRealMachODSYMAndZipWithInlinedFrames(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Mach-O clang++/dsymutil integration test runs on macOS")
	}
	clangPath, err1 := exec.LookPath("clang++")
	dsymutilPath, err2 := exec.LookPath("dsymutil")
	if err1 != nil || err2 != nil {
		t.Skip("clang++ or dsymutil not installed")
	}

	tmp := t.TempDir()
	srcPath := filepath.Join(tmp, "decoder.cpp")
	// Line numbers are intentionally deterministic and checked below!
	cppSrc := `// line 1
#include <cstdint>
namespace atlas {
namespace media {

__attribute__((always_inline))
inline uint32_t unpackColorPlane(const uint8_t* buf, int len) {
    uint32_t acc = 0;
    for (int i = 0; i < len; ++i) {
        acc = (acc * 33u) ^ static_cast<uint32_t>(buf[i]);
    }
    return acc;
}

class FrameDecoder {
public:
    __attribute__((noinline))
    uint32_t decodePacket(const uint8_t* buf, int len) {
        uint32_t v = unpackColorPlane(buf, len);
        return v ^ 0xA5A5u;
    }
};

} // namespace media
} // namespace atlas

int main(int argc, char** argv) {
    atlas::media::FrameDecoder dec;
    uint8_t sample[4] = {1, 2, 3, static_cast<uint8_t>(argc)};
    return static_cast<int>(dec.decodePacket(sample, 4) & 0x7f);
}
`
	if err := os.WriteFile(srcPath, []byte(cppSrc), 0o600); err != nil {
		t.Fatalf("write cpp: %v", err)
	}

	objPath := filepath.Join(tmp, "decoder.o")
	binPath := filepath.Join(tmp, "atlas_demo")
	dsymDir := filepath.Join(tmp, "atlas_demo.dSYM")
	// Compile to an explicit .o file first so Mach-O OSO debug map stays intact for dsymutil (matching Xcode).
	if out, err := exec.Command(clangPath, "-std=c++17", "-g", "-O2", "-c", "-o", objPath, srcPath).CombinedOutput(); err != nil {
		t.Fatalf("clang++ compile failed: %v\n%s", err, string(out))
	}
	if out, err := exec.Command(clangPath, "-o", binPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("clang++ link failed: %v\n%s", err, string(out))
	}
	if out, err := exec.Command(dsymutilPath, binPath, "-o", dsymDir).CombinedOutput(); err != nil {
		t.Fatalf("dsymutil failed: %v\n%s", err, string(out))
	}

	dwarfFile := filepath.Join(dsymDir, "Contents", "Resources", "DWARF", "atlas_demo")
	rawMachO, err := os.ReadFile(dwarfFile)
	if err != nil {
		t.Fatalf("read dsym dwarf: %v", err)
	}

	// 1. Parse raw Mach-O DWARF directly
	tbl := ParseNativeSymbols(rawMachO)
	if tbl.Format != "macho-dwarf" {
		t.Fatalf("expected format=macho-dwarf, got %q", tbl.Format)
	}
	if tbl.BuildUUID == "" || len(tbl.BuildUUID) < 32 {
		t.Fatalf("expected Mach-O LC_UUID, got %q", tbl.BuildUUID)
	}
	if tbl.ImageBase == 0 {
		t.Fatalf("expected non-zero Mach-O __TEXT ImageBase")
	}

	// Find an instruction address inside unpackColorPlane inlined into decodePacket.
	var inlinedAddr uint64
	if tbl.dwarf != nil {
		for _, inl := range tbl.dwarf.inlines {
			if strings.Contains(inl.Name, "unpackColorPlane") {
				inlinedAddr = inl.Low
				break
			}
		}
	}
	if inlinedAddr == 0 {
		t.Fatalf("expected DWARF inlined_subroutine for unpackColorPlane; funcs=%d inlines=%d", len(tbl.dwarf.funcs), len(tbl.dwarf.inlines))
	}

	// 2. Verify full virtual address AND slide-relative offset resolution + inline expansion.
	absHex := fmt.Sprintf("0x%x", inlinedAddr)
	relHex := fmt.Sprintf("0x%x", inlinedAddr-tbl.ImageBase)
	for _, addrStr := range []string{absHex, relHex} {
		chain, ok := tbl.ResolveAddressChain(addrStr)
		if !ok || len(chain) < 2 {
			t.Fatalf("ResolveAddressChain(%s): expected >=2 frames (inlined + caller), got ok=%v chain=%+v", addrStr, ok, chain)
		}
		if !chain[0].Inlined || !strings.Contains(chain[0].Symbol, "unpackColorPlane") {
			t.Fatalf("frame[0] (%s): expected inlined unpackColorPlane, got %+v", addrStr, chain[0])
		}
		if !strings.HasSuffix(chain[0].File, "decoder.cpp") || chain[0].Line < 7 || chain[0].Line > 13 {
			t.Fatalf("frame[0] (%s): expected decoder.cpp:7..13, got %s:%d", addrStr, chain[0].File, chain[0].Line)
		}
		outer := chain[len(chain)-1]
		if outer.Inlined || !strings.Contains(outer.Symbol, "atlas::media::FrameDecoder::decodePacket") {
			t.Fatalf("outer (%s): expected demangled atlas::media::FrameDecoder::decodePacket, got %+v", addrStr, outer)
		}
		if !strings.HasSuffix(outer.File, "decoder.cpp") || outer.Line != 19 {
			t.Fatalf("outer (%s): expected callsite decoder.cpp:19, got %s:%d", addrStr, outer.File, outer.Line)
		}
	}

	// 3. Zip into .dSYM.zip and verify end-to-end Service.ResolveFrames expansion!
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	w, err := zw.Create("atlas_demo.dSYM/Contents/Resources/DWARF/atlas_demo")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	_, _ = w.Write(rawMachO)
	_ = zw.Close()

	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer store.Close()
	_, err = store.SaveSymbol(context.Background(), storage.SymbolRecord{
		SymbolID:    "sym-ios-real-dsym-zip",
		AppID:       "dev.caliper.demo",
		Platform:    "ios",
		Env:         "prod",
		AppVersion:  "1.6.0",
		BuildNumber: "160",
		Kind:        "dsym",
		SHA256:      "realdsymzipsha",
	}, zipBuf.Bytes())
	if err != nil {
		t.Fatalf("save dsym zip: %v", err)
	}

	svc := NewService(store)
	res := svc.ResolveFrames(context.Background(), LookupScope{
		AppID: "dev.caliper.demo", Platform: "ios", AppVersion: "1.6.0", BuildNumber: "160",
	}, []protocol.StackFrame{
		{Module: "atlas_demo", Address: relHex, Symbol: relHex},
	})
	if res.Status != "symbolicated" || res.ResolvedCount != 1 {
		t.Fatalf("expected symbolicated resolved=1, got %+v", res)
	}
	if len(res.Frames) < 2 {
		t.Fatalf("expected expanded inline+caller frames (>=2), got %+v", res.Frames)
	}
	if !res.Frames[0].Inlined || !strings.Contains(res.Frames[0].Symbol, "unpackColorPlane") {
		t.Fatalf("expected expanded frame[0] unpackColorPlane inlined, got %+v", res.Frames[0])
	}
	if !strings.Contains(res.Frames[1].Symbol, "atlas::media::FrameDecoder::decodePacket") || res.Frames[1].Line != 19 {
		t.Fatalf("expected expanded caller frame[1] decodePacket:19, got %+v", res.Frames[1])
	}
}

func TestRealARM64ELFDWARFAndNativeElfKind(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not in PATH")
	}
	tmp := t.TempDir()
	mainPath := filepath.Join(tmp, "ndk_sample.go")
	src := `package main

import "fmt"

//go:noinline
func decodeMediaTile(x int) int {
	return x*7 + 13
}

func main() {
	fmt.Println(decodeMediaTile(42))
}
`
	if err := os.WriteFile(mainPath, []byte(src), 0o600); err != nil {
		t.Fatalf("write go src: %v", err)
	}
	elfPath := filepath.Join(tmp, "libatlas_ndk.so")
	cmd := exec.Command(goPath, "build", "-o", elfPath, mainPath)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile arm64 ELF failed: %v\n%s", err, string(out))
	}

	rawELF, err := os.ReadFile(elfPath)
	if err != nil {
		t.Fatalf("read ELF: %v", err)
	}

	tbl := ParseNativeSymbols(rawELF)
	if tbl.Format != "elf-dwarf" {
		t.Fatalf("expected format=elf-dwarf, got %q", tbl.Format)
	}

	// Locate main.decodeMediaTile address inside DWARF index.
	var targetAddr uint64
	for _, fn := range tbl.dwarf.funcs {
		if strings.HasSuffix(fn.Name, "decodeMediaTile") {
			targetAddr = fn.Low
			break
		}
	}
	if targetAddr == 0 {
		t.Fatalf("decodeMediaTile not found in ELF DWARF funcs (%d funcs)", len(tbl.dwarf.funcs))
	}

	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer store.Close()
	_, err = store.SaveSymbol(context.Background(), storage.SymbolRecord{
		SymbolID:    "sym-android-ndk-elf",
		AppID:       "dev.caliper.demo",
		Platform:    "android",
		Env:         "prod",
		AppVersion:  "1.6.0",
		BuildNumber: "160",
		Kind:        "native_elf",
		SHA256:      "realelfsha",
	}, rawELF)
	if err != nil {
		t.Fatalf("save elf: %v", err)
	}

	svc := NewService(store)
	addrHex := fmt.Sprintf("0x%x", targetAddr)
	res := svc.ResolveFrames(context.Background(), LookupScope{
		AppID: "dev.caliper.demo", Platform: "android", AppVersion: "1.6.0", BuildNumber: "160",
	}, []protocol.StackFrame{{Module: "libatlas_ndk.so", Address: addrHex, Symbol: addrHex}})
	if res.Status != "symbolicated" || res.ResolvedCount != 1 {
		t.Fatalf("expected ELF symbolicated, got %+v", res)
	}
	if !strings.Contains(res.Frames[0].Symbol, "decodeMediaTile") || !strings.HasSuffix(res.Frames[0].File, "ndk_sample.go") || res.Frames[0].Line < 6 || res.Frames[0].Line > 8 {
		t.Fatalf("unexpected ELF resolved frame: %+v", res.Frames[0])
	}
}

func TestELFNoteGNUBuildIDExtraction(t *testing.T) {
	// Synthesize a minimal valid ELF-64 binary with .shstrtab and .note.gnu.build-id.
	// Note payload: namesz=4 ("GNU\x00"), descsz=8, type=3 (NT_GNU_BUILD_ID), desc=0123456789abcdef
	var note bytes.Buffer
	_ = binary.Write(&note, binary.LittleEndian, uint32(4))
	_ = binary.Write(&note, binary.LittleEndian, uint32(8))
	_ = binary.Write(&note, binary.LittleEndian, uint32(3))
	note.Write([]byte("GNU\x00"))
	note.Write([]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})

	shstrtab := []byte("\x00.shstrtab\x00.note.gnu.build-id\x00")
	const ehdrSize = 64
	noteOff := uint64(ehdrSize)
	shstrOff := noteOff + uint64(note.Len())
	shdrOff := (shstrOff + uint64(len(shstrtab)) + 7) &^ 7

	var buf bytes.Buffer
	// ELF64 header
	hdr := elf.Header64{
		Ident:     [elf.EI_NIDENT]byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)},
		Type:      uint16(elf.ET_DYN),
		Machine:   uint16(elf.EM_AARCH64),
		Version:   uint32(elf.EV_CURRENT),
		Shoff:     shdrOff,
		Ehsize:    ehdrSize,
		Shentsize: 64,
		Shnum:     3,
		Shstrndx:  1,
	}
	_ = binary.Write(&buf, binary.LittleEndian, hdr)
	buf.Write(note.Bytes())
	buf.Write(shstrtab)
	for uint64(buf.Len()) < shdrOff {
		buf.WriteByte(0)
	}
	// Section 0: NULL
	_ = binary.Write(&buf, binary.LittleEndian, elf.Section64{})
	// Section 1: .shstrtab
	_ = binary.Write(&buf, binary.LittleEndian, elf.Section64{
		Name: 1, Type: uint32(elf.SHT_STRTAB), Off: shstrOff, Size: uint64(len(shstrtab)),
	})
	// Section 2: .note.gnu.build-id
	_ = binary.Write(&buf, binary.LittleEndian, elf.Section64{
		Name: 11, Type: uint32(elf.SHT_NOTE), Off: noteOff, Size: uint64(note.Len()),
	})

	tbl := ParseNativeSymbols(buf.Bytes())
	if tbl.BuildUUID != "0123456789abcdef" {
		t.Fatalf("expected ELF build-id 0123456789abcdef, got %q", tbl.BuildUUID)
	}
}
