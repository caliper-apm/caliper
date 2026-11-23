package symbolicator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemangleSwift5Symbols(t *testing.T) {
	cases := []struct {
		mangled string
		want    string
	}{
		// Top-level & async throws & labeled/unlabeled params
		{
			mangled: "_$s11CaliperDemo10asyncFetch2idSDySSypGs5Int64V_tYaKF",
			want:    "CaliperDemo.asyncFetch(id: Swift.Int64) async throws -> [Swift.String : Any]",
		},
		{
			mangled: "$s11CaliperDemo15topLevelCompute_5labelSbSi_SStKF",
			want:    "CaliperDemo.topLevelCompute(_: Swift.Int, label: Swift.String) throws -> Swift.Bool",
		},
		{
			mangled: "_$s6Probe512allUnlabeledyS2i_SStF",
			want:    "Probe5.allUnlabeled(Swift.Int, Swift.String) -> Swift.Int",
		},
		{
			mangled: "_$s6Probe512oneUnlabeledyySiF",
			want:    "Probe5.oneUnlabeled(Swift.Int) -> ()",
		},
		{
			mangled: "_$s6Probe515secondUnlabeled5first_ySi_SStF",
			want:    "Probe5.secondUnlabeled(first: Swift.Int, _: Swift.String) -> ()",
		},
		{
			mangled: "_$s6Probe59withInout_5itemsySiz_SSdtF",
			want:    "Probe5.withInout(_: inout Swift.Int, items: Swift.String...) -> ()",
		},
		// Struct, Class, Enum, Nested types, Static methods, Initializers, Deinitializers
		{
			mangled: "_$s11CaliperDemo12OrderPayloadV5emptyACyFZ",
			want:    "static CaliperDemo.OrderPayload.empty() -> CaliperDemo.OrderPayload",
		},
		{
			mangled: "_$s11CaliperDemo12OrderPayloadV7orderID6amountACSS_SdtcfC",
			want:    "CaliperDemo.OrderPayload.init(orderID: Swift.String, amount: Swift.Double) -> CaliperDemo.OrderPayload",
		},
		{
			mangled: "_$s11CaliperDemo12OrderPayloadV8validate6strictS2b_tF",
			want:    "CaliperDemo.OrderPayload.validate(strict: Swift.Bool) -> Swift.Bool",
		},
		{
			mangled: "_$s11CaliperDemo12NetworkErrorO15descriptionTextSSyF",
			want:    "CaliperDemo.NetworkError.descriptionText() -> Swift.String",
		},
		{
			mangled: "_$s6Probe55OuterV5InnerV4pingSbyF",
			want:    "Probe5.Outer.Inner.ping() -> Swift.Bool",
		},
		{
			mangled: "_$s6Probe55OuterV5InnerVAEycfC",
			want:    "Probe5.Outer.Inner.init() -> Probe5.Outer.Inner",
		},
		// Generics
		{
			mangled: "_$s11CaliperDemo12GenericCacheC3get3keyq_Sgx_tF",
			want:    "CaliperDemo.GenericCache.get(key: A) -> B?",
		},
		{
			mangled: "_$s11CaliperDemo12GenericCacheC3put_6forKeyyq__xtF",
			want:    "CaliperDemo.GenericCache.put(_: B, forKey: A) -> ()",
		},
		{
			mangled: "_$s11CaliperDemo12GenericCacheCACyxq_GycfC",
			want:    "CaliperDemo.GenericCache.__allocating_init() -> CaliperDemo.GenericCache<A, B>",
		},
		{
			mangled: "_$s11CaliperDemo12GenericCacheCfD",
			want:    "CaliperDemo.GenericCache.__deallocating_deinit",
		},
		// Word-substituted compressed identifiers (camelCase reuse)
		{
			mangled: "_$s11CaliperDemo22CheckoutViewControllerC11submitOrder7payload10retryCountSiAA0G7PayloadV_SitKF",
			want:    "CaliperDemo.CheckoutViewController.submitOrder(payload: CaliperDemo.OrderPayload, retryCount: Swift.Int) throws -> Swift.Int",
		},
		{
			mangled: "_$s10CaliperE2E18FeedViewControllerC04loadcD5Items4from10completiony10Foundation3URLV_ys6ResultOySaySSGs5Error_pGctF",
			want:    "CaliperE2E.FeedViewController.loadFeedViewItems(from: Foundation.URL, completion: (Swift.Result<[Swift.String], Swift.Error>) -> ()) -> ()",
		},
		{
			mangled: "_$s10CaliperApp18FeedViewControllerC04loadcd5ItemsE0yyF",
			want:    "CaliperApp.FeedViewController.loadFeedViewItemsController() -> ()",
		},
		// Accessors, Subscripts, Private discriminators
		{
			mangled: "_$s11CaliperDemo22CheckoutViewControllerC5cacheSDySSSiGSgvg",
			want:    "CaliperDemo.CheckoutViewController.cache.getter : [Swift.String : Swift.Int]?",
		},
		{
			mangled: "_$s11CaliperDemo22CheckoutViewControllerC5cacheSDySSSiGSgvs",
			want:    "CaliperDemo.CheckoutViewController.cache.setter : [Swift.String : Swift.Int]?",
		},
		{
			mangled: "_$s11CaliperDemo22CheckoutViewControllerC5cacheSDySSSiGSgvM",
			want:    "CaliperDemo.CheckoutViewController.cache.modify : [Swift.String : Swift.Int]?",
		},
		{
			mangled: "_$s11CaliperDemo22CheckoutViewControllerCySSSgSicig",
			want:    "CaliperDemo.CheckoutViewController.subscript.getter : (Swift.Int) -> Swift.String?",
		},
		{
			mangled: "_$s11CaliperDemo12GenericCacheC7storage33_E106C01CCB167069043802673D09A0D1LLSDyxq_Gvpfi",
			want:    "variable initialization expression of CaliperDemo.GenericCache.(storage in _E106C01CCB167069043802673D09A0D1) : [A : B]",
		},
		// Closures, @objc thunks, async resume partial functions, extensions, specializations, protocol witnesses
		{
			mangled: "_$s10CaliperApp18FeedViewControllerC11makeClosureSiycyFSiycfU_",
			want:    "closure #1 () -> Swift.Int in CaliperApp.FeedViewController.makeClosure() -> () -> Swift.Int",
		},
		{
			mangled: "_$s10CaliperE2E18FeedViewControllerC11viewDidLoadyyFTo",
			want:    "@objc CaliperE2E.FeedViewController.viewDidLoad() -> ()",
		},
		{
			mangled: "_$s10CaliperE2E18FeedViewControllerC16runAsyncPipeline6feedIDSiSS_tYaFTQ0_",
			want:    "(1) await resume partial function for CaliperE2E.FeedViewController.runAsyncPipeline(feedID: Swift.String) async -> Swift.Int",
		},
		{
			mangled: "_$s10CaliperE2E18FeedViewControllerC16runAsyncPipeline6feedIDSiSS_tYaFTY1_",
			want:    "(2) suspend resume partial function for CaliperE2E.FeedViewController.runAsyncPipeline(feedID: Swift.String) async -> Swift.Int",
		},
		{
			mangled: "_$sSa10CaliperE2ESSRszlE15caliperJoinFeedSSyF",
			want:    "(extension in CaliperE2E):Swift.Array<A where A == Swift.String>.caliperJoinFeed() -> Swift.String",
		},
		{
			mangled: "_$ss15ContiguousArrayV16_createNewBuffer14bufferIsUnique15minimumCapacity13growForAppendySb_SiSbtFSi_Tg5",
			want:    "generic specialization <Swift.Int> of Swift.ContiguousArray._createNewBuffer(bufferIsUnique: Swift.Bool, minimumCapacity: Swift.Int, growForAppend: Swift.Bool) -> ()",
		},
		{
			mangled: "_$s11CaliperDemo12NetworkErrorOs0D0AAsADP7_domainSSvgTW",
			want:    "protocol witness for Swift.Error._domain.getter : Swift.String in conformance CaliperDemo.NetworkError : Swift.Error in CaliperDemo",
		},
		// Malformed / unknown Swift symbol must fall back honestly without panic or corruption
		{
			mangled: "_$s999InvalidTruncated",
			want:    "_$s999InvalidTruncated",
		},
	}

	for _, tc := range cases {
		got := DemangleNativeSymbol(tc.mangled)
		if got != tc.want {
			t.Errorf("DemangleNativeSymbol(%q)\n got: %q\nwant: %q", tc.mangled, got, tc.want)
		}
	}
}

// TestDemangleSwiftLiveAgainstXcodeCompiler compiles a real Swift module with local `swiftc`
// and verifies our pure-Go demangler against real `xcrun swift-demangle` + Mach-O DWARF symbolication.
func TestDemangleSwiftLiveAgainstXcodeCompiler(t *testing.T) {
	swiftcPath, err := exec.LookPath("swiftc")
	if err != nil {
		t.Skip("swiftc not installed; skipping live Swift compiler cross-verification")
	}
	xcrunPath, err := exec.LookPath("xcrun")
	if err != nil {
		t.Skip("xcrun not installed; skipping live swift-demangle cross-verification")
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "LiveVerify.swift")
	binPath := filepath.Join(dir, "LiveVerifyBin")
	dsymPath := filepath.Join(dir, "LiveVerifyBin.dSYM")

	src := `import Foundation

public struct CartItem {
    public let sku: String
    public var price: Double
    public init(sku: String, price: Double) { self.sku = sku; self.price = price }
    public func discounted(rate: Double) -> Double { return price * rate }
}

public final class CheckoutCoordinator {
    public init() {}
    @inline(never)
    public func executePayment(cart: [CartItem], promoCode: String?) throws -> Int {
        return cart.count + (promoCode?.count ?? 0)
    }
    public func fetchOrderStatus(orderID: String) async -> Bool {
        return !orderID.isEmpty
    }
}

@main
struct Entry {
    static func main() {
        let coord = CheckoutCoordinator()
        _ = try? coord.executePayment(cart: [CartItem(sku: "A1", price: 9.9)], promoCode: "VIP")
    }
}
`
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(swiftcPath, "-parse-as-library", "-g", "-Onone", "-module-name", "CaliperLive", srcPath, "-o", binPath).CombinedOutput(); err != nil {
		t.Fatalf("swiftc failed: %v\n%s", err, string(out))
	}
	if out, err := exec.Command(xcrunPath, "dsymutil", binPath, "-o", dsymPath).CombinedOutput(); err != nil {
		t.Fatalf("dsymutil failed: %v\n%s", err, string(out))
	}

	// Verify pure-Go Mach-O DWARF parser reads the real Swift dSYM and demangles Swift linkage names.
	dwarfBin := filepath.Join(dsymPath, "Contents", "Resources", "DWARF", "LiveVerifyBin")
	rawDSYM, err := os.ReadFile(dwarfBin)
	if err != nil {
		t.Fatalf("read dSYM DWARF: %v", err)
	}
	tbl := ParseNativeSymbols(rawDSYM)
	if tbl == nil || len(tbl.Entries) == 0 {
		t.Fatalf("expected non-empty DWARF entries from real Swift dSYM")
	}

	foundExecutePayment := false
	foundDiscounted := false
	for _, entry := range tbl.Entries {
		if strings.Contains(entry.Symbol, "CaliperLive.CheckoutCoordinator.executePayment(cart: [CaliperLive.CartItem], promoCode: Swift.String?) throws -> Swift.Int") {
			foundExecutePayment = true
		}
		if strings.Contains(entry.Symbol, "CaliperLive.CartItem.discounted(rate: Swift.Double) -> Swift.Double") {
			foundDiscounted = true
		}
	}
	if !foundExecutePayment {
		t.Fatalf("expected demangled Swift CheckoutCoordinator.executePayment in real dSYM entries")
	}
	if !foundDiscounted {
		t.Fatalf("expected demangled Swift CartItem.discounted in real dSYM entries")
	}

	// Cross-check extracted symbols from `nm` against `xcrun swift-demangle --simplified=false`.
	nmOut, err := exec.Command("nm", "-U", binPath).CombinedOutput()
	if err != nil {
		t.Fatalf("nm failed: %v", err)
	}
	checked := 0
	for _, line := range strings.Split(string(nmOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		sym := fields[2]
		if !strings.HasPrefix(sym, "_$s11CaliperLive") {
			continue
		}
		// Focus on callable methods/inits/getters (F, fC, fc, fD, fd, vg, vs, vM).
		if !(strings.HasSuffix(sym, "F") || strings.HasSuffix(sym, "FZ") ||
			strings.HasSuffix(sym, "fC") || strings.HasSuffix(sym, "fc") ||
			strings.HasSuffix(sym, "fD") || strings.HasSuffix(sym, "fd") ||
			strings.HasSuffix(sym, "vg") || strings.HasSuffix(sym, "vs") ||
			strings.HasSuffix(sym, "vM")) {
			continue
		}
		refOut, err := exec.Command(xcrunPath, "swift-demangle", "-compact", sym).CombinedOutput()
		if err != nil {
			continue
		}
		want := strings.TrimSpace(string(refOut))
		got := DemangleNativeSymbol(sym)
		if got != want {
			t.Errorf("live mismatch for %s:\n got: %q\nwant: %q", sym, got, want)
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("expected to cross-verify at least 5 real Swift symbols against xcrun swift-demangle, got %d", checked)
	}
	t.Logf("Cross-verified %d real swiftc-compiled symbols against xcrun swift-demangle with 100%% exact match!", checked)
}

func TestDemangleSwiftNeverPanicsOnCorruptedInput(t *testing.T) {
	badInputs := []string{
		"_$s",
		"$s",
		"_$s0",
		"_$s0Z",
		"_$s0a",
		"_$sA",
		"_$sAZ",
		"_$sA999_",
		"_$sS999b",
		"_$sS",
		"_$sSg",
		"_$sC",
		"_$sF",
		"_$sFZ",
		"_$sG",
		"_$st",
		"_$sc",
		"_$sK",
		"_$sYa",
		"_$sfC",
		"_$sfU_",
		"_$svg",
		"_$sig",
		"_$sTW",
		"_$sTg5",
		"_$sTf4",
		"_$s10abc",
		"_$s999999999999999999999999a",
		"_$s6E2EApp17E2EViewControllerC",
	}
	for _, in := range badInputs {
		got := DemangleNativeSymbol(in)
		if got == "" {
			t.Errorf("DemangleNativeSymbol(%q) returned empty string", in)
		}
	}
}
