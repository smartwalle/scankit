//go:build amd64

package x86

import (
	"math/rand"
	"os"
	"testing"

	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/simd/generic"
	"golang.org/x/sys/cpu"
)

// avx2Executable 报告当前测试进程能否执行 AVX2 指令。
//
// Rosetta 2 会在 CPUID 中隐藏 AVX/AVX2，却能正确执行对应指令，因此除常规能力探测外
// 还允许通过 SCANKIT_AVX2_TESTS=1 显式开启 AVX2 内核测试；真实 AVX2 主机无需该变量。
func avx2Executable() bool {
	return cpu.X86.HasAVX2 || os.Getenv("SCANKIT_AVX2_TESTS") == "1"
}

func x86ByteSetSets() [][4]uint64 {
	sets := [][4]uint64{
		{},
		{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)},
	}
	for h := range 16 {
		var set [4]uint64
		set[h>>2] |= uint64(0xFFFF) << (uint(h&3) * 16)
		sets = append(sets, set)
	}
	for b := range 256 {
		var set [4]uint64
		set[b/64] = 1 << uint(b%64)
		sets = append(sets, set)
	}
	rng := rand.New(rand.NewSource(7))
	for range 128 {
		var set [4]uint64
		for w := range set {
			set[w] = rng.Uint64()
		}
		sets = append(sets, set)
	}
	return sets
}

// TestNativeByteSetMaskMatchesGeneric 覆盖全部字节取值、全部 lane 位置与随机集合。
func TestNativeByteSetMaskMatchesGeneric(t *testing.T) {
	for _, raw := range x86ByteSetSets() {
		set := simd.NewByteSet(raw)
		for b := range 256 {
			for lane := range generic.Width {
				var v generic.Vector
				v[lane] = byte(b)
				want := generic.ByteSetMask(v, raw)
				if got := nativeByteSetMask(&v, set.TableVector()); got != want {
					t.Fatalf("raw=%v byte=%d lane=%d got=%#x want=%#x", raw, b, lane, got, want)
				}
			}
		}
	}
}

// TestNativeByteSetMaskRandomVectors 使用随机窗口核对原生路径与通用实现一致。
func TestNativeByteSetMaskRandomVectors(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	sets := x86ByteSetSets()
	for range 20000 {
		raw := sets[rng.Intn(len(sets))]
		set := simd.NewByteSet(raw)
		var v generic.Vector
		for j := range v {
			v[j] = byte(rng.Intn(256))
		}
		want := generic.ByteSetMask(v, raw)
		if got := nativeByteSetMask(&v, set.TableVector()); got != want {
			t.Fatalf("raw=%v v=%v got=%#x want=%#x", raw, v, got, want)
		}
	}
}

// TestNativeInRangeMaskMatchesGeneric 覆盖全部 256x256 区间组合与端点位。
func TestNativeInRangeMaskMatchesGeneric(t *testing.T) {
	for lo := range 256 {
		for hi := range 256 {
			for lane := range generic.Width {
				for b := 0; b < 256; b += 17 {
					var v generic.Vector
					for j := range v {
						v[j] = byte((b + j*13) & 0xFF)
					}
					v[lane] = byte(b)
					want := generic.InRangeMask(v, byte(lo), byte(hi))
					if got := nativeInRangeMask(&v, byte(lo), byte(hi)); got != want {
						t.Fatalf("lo=%d hi=%d v=%v got=%#x want=%#x", lo, hi, v, got, want)
					}
				}
			}
		}
	}
}

// TestX86BackendNativeMasksMatchGeneric 验证后端入口在能力允许时选择了原生路径且结果一致。
func TestX86BackendNativeMasksMatchGeneric(t *testing.T) {
	backend := NewWithTier(TierAVX512VBMI)
	if tier, ok := EffectiveTierOf(backend); !ok || tier < TierSSE4 {
		t.Skipf("host lacks SSE4, effective tier=%v", tier)
	}
	rng := rand.New(rand.NewSource(5))
	for range 5000 {
		var v generic.Vector
		for j := range v {
			v[j] = byte(rng.Intn(256))
		}
		lo := byte(rng.Intn(256))
		hi := byte(rng.Intn(256))
		if lo > hi {
			lo, hi = hi, lo
		}
		if got, want := backend.InRangeMask(v, lo, hi), generic.InRangeMask(v, lo, hi); got != want {
			t.Fatalf("InRangeMask lo=%d hi=%d got=%#x want=%#x", lo, hi, got, want)
		}
		raw := x86ByteSetSets()[rng.Intn(300)]
		set := simd.NewByteSet(raw)
		if got, want := backend.ByteSetMaskPrepared(v, &set), generic.ByteSetMask(v, raw); got != want {
			t.Fatalf("ByteSetMaskPrepared raw=%v got=%#x want=%#x", raw, got, want)
		}
	}
}

func BenchmarkNativeMasks(b *testing.B) {
	raw := [4]uint64{0, 0x03FF000000000000, 0x7FFFFFE00000000, 0}
	set := simd.NewByteSet(raw)
	var v generic.Vector
	for i := range v {
		v[i] = byte('a' + i%26)
	}
	b.Run("byteset_native", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if nativeByteSetMask(&v, set.TableVector()) == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("byteset_scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if generic.ByteSetMask(v, raw) == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("inrange_native", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if nativeInRangeMask(&v, 'a', 'z') == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("inrange_scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if generic.InRangeMask(v, 'a', 'z') == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
}

// TestNativeByteSetMask32CoversAllValues 验证 32 字节原生判定对全部 256 个取值
// 的命中/未命中判定与位序都正确。
func TestNativeByteSetMask32CoversAllValues(t *testing.T) {
	for _, raw := range x86ByteSetSets() {
		set := simd.NewByteSet(raw)
		for b := range 256 {
			window := make([]byte, simd.SuperWidth)
			for i := range window {
				window[i] = byte(b)
			}
			want := uint32(0)
			if raw[b/64]&(1<<uint(b%64)) != 0 {
				want = ^uint32(0)
			}
			if got := nativeByteSetMask32(window, set.TableVector()); got != want {
				t.Fatalf("raw=%v byte=%d got=%032b want=%032b", raw, b, got, want)
			}
		}
	}
}

// TestNativeByteSetMask32PinsBitOrder 使用单字节集合逐位置验证掩码位序：
// 第 i 位必须对应窗口内第 i 个字节，跨两个 128 位半区不发生错位。
func TestNativeByteSetMask32PinsBitOrder(t *testing.T) {
	for b := range 256 {
		var raw [4]uint64
		raw[b/64] = 1 << uint(b%64)
		set := simd.NewByteSet(raw)
		for lane := range simd.SuperWidth {
			window := make([]byte, simd.SuperWidth)
			for i := range window {
				window[i] = byte(b + 1)
			}
			window[lane] = byte(b)
			want := uint32(1) << uint(lane)
			if got := nativeByteSetMask32(window, set.TableVector()); got != want {
				t.Fatalf("byte=%d lane=%d got=%032b want=%032b", b, lane, got, want)
			}
		}
	}
}

// TestNativeByteSetMask32RandomWindows 使用随机窗口核对原生路径与通用实现一致。
func TestNativeByteSetMask32RandomWindows(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	sets := x86ByteSetSets()
	for range 20000 {
		raw := sets[rng.Intn(len(sets))]
		set := simd.NewByteSet(raw)
		window := make([]byte, simd.SuperWidth)
		want := uint32(0)
		for j := range window {
			value := byte(rng.Intn(256))
			window[j] = value
			if raw[value/64]&(1<<uint(value%64)) != 0 {
				want |= 1 << uint(j)
			}
		}
		if got := nativeByteSetMask32(window, set.TableVector()); got != want {
			t.Fatalf("raw=%v window=%v got=%032b want=%032b", raw, window, got, want)
		}
	}
}

// BenchmarkNativeByteSetMask32 记录 32 字节 SSE 半区拼接判定的单次成本。
func BenchmarkNativeByteSetMask32(b *testing.B) {
	var raw [4]uint64
	for value := 'a'; value <= 'z'; value += 2 {
		raw[value/64] |= 1 << uint(value%64)
	}
	set := simd.NewByteSet(raw)
	tables := set.TableVector()
	window := make([]byte, simd.SuperWidth)
	for i := range window {
		window[i] = byte('a' + i%26)
	}
	b.ReportAllocs()
	var acc uint32
	for b.Loop() {
		acc |= nativeByteSetMask32(window, tables)
	}
	if acc == 0 {
		b.Fatal("掩码不应全为零")
	}
}

// TestNativeByteSetMask32AVX2CoversAllValues 覆盖全部 256 个取值在两个半区上的判定。
func TestNativeByteSetMask32AVX2CoversAllValues(t *testing.T) {
	if !avx2Executable() {
		t.Skip("当前环境未声明 AVX2 能力")
	}
	for _, raw := range x86ByteSetSets() {
		set := simd.NewByteSet(raw)
		for b := range 256 {
			window := make([]byte, simd.SuperWidth)
			for i := range window {
				window[i] = byte(b)
			}
			want := uint32(0)
			if raw[b/64]&(1<<uint(b%64)) != 0 {
				want = ^uint32(0)
			}
			if got := nativeByteSetMask32AVX2(window, set.TableVector()); got != want {
				t.Fatalf("raw=%v byte=%d got=%032b want=%032b", raw, b, got, want)
			}
		}
	}
}

// TestNativeByteSetMask32AVX2PinsBitOrder 逐位置验证 256 位判定的掩码位序。
func TestNativeByteSetMask32AVX2PinsBitOrder(t *testing.T) {
	if !avx2Executable() {
		t.Skip("当前环境未声明 AVX2 能力")
	}
	for b := range 256 {
		var raw [4]uint64
		raw[b/64] = 1 << uint(b%64)
		set := simd.NewByteSet(raw)
		for lane := range simd.SuperWidth {
			window := make([]byte, simd.SuperWidth)
			for i := range window {
				window[i] = byte(b - 1)
			}
			window[lane] = byte(b)
			want := uint32(1) << uint(lane)
			if got := nativeByteSetMask32AVX2(window, set.TableVector()); got != want {
				t.Fatalf("byte=%d lane=%d got=%032b want=%032b", b, lane, got, want)
			}
		}
	}
}

// TestNativeByteSetMask32AVX2MatchesSSE 用随机窗口与已对齐通用实现的 SSE 内核对照。
func TestNativeByteSetMask32AVX2MatchesSSE(t *testing.T) {
	if !avx2Executable() {
		t.Skip("当前环境未声明 AVX2 能力")
	}
	rng := rand.New(rand.NewSource(23))
	sets := x86ByteSetSets()
	for range 20000 {
		raw := sets[rng.Intn(len(sets))]
		set := simd.NewByteSet(raw)
		window := make([]byte, simd.SuperWidth)
		for j := range window {
			window[j] = byte(rng.Intn(256))
		}
		sse := nativeByteSetMask32(window, set.TableVector())
		avx := nativeByteSetMask32AVX2(window, set.TableVector())
		if sse != avx {
			t.Fatalf("raw=%v window=%v sse=%032b avx2=%032b", raw, window, sse, avx)
		}
	}
}

// TestBackendWindowMaskAVX2MatchesScalar 强制生效层级为 AVX2，验证分派路径在 AVX2
// 内核与通用标量参考之间逐位一致（含全部 lane 组合与窗口末端退让）。
func TestBackendWindowMaskAVX2MatchesScalar(t *testing.T) {
	if !avx2Executable() {
		t.Skip("当前环境未声明 AVX2 能力")
	}
	backend := Backend{tier: TierAVX2, resolved: TierAVX2}
	rng := rand.New(rand.NewSource(29))
	data := make([]byte, simd.SuperWidth*3)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}
	for iter := range 64 {
		var laneSets [4][4]uint64
		for lane := range laneSets {
			for word := range laneSets[lane] {
				if iter%2 == 0 {
					laneSets[lane][word] = rng.Uint64()
					continue
				}
				for range 4 {
					value := rng.Intn(256)
					laneSets[lane][value/64] |= 1 << uint(value%64)
				}
			}
		}
		tables := simd.NewByteSetTables(laneSets)
		for _, off := range []int{0, 1, simd.SuperWidth, 2 * simd.SuperWidth} {
			for lanes := 1; lanes <= 4; lanes++ {
				want, wantOK := simd.WindowMaskScalar(data, off, &tables, lanes)
				got, ok := backend.WindowMask(data, off, &tables, lanes)
				if ok != wantOK || got != want {
					t.Fatalf("迭代 %d 偏移 %d lane %d: got=%032b ok=%v want=%032b wantOK=%v",
						iter, off, lanes, got, ok, want, wantOK)
				}
			}
		}
	}
}

// BenchmarkNativeByteSetMask32AVX2 记录 256 位 AVX2 判定的单次成本。
func BenchmarkNativeByteSetMask32AVX2(b *testing.B) {
	if !avx2Executable() {
		b.Skip("当前环境未声明 AVX2 能力")
	}
	var raw [4]uint64
	for value := 'a'; value <= 'z'; value += 2 {
		raw[value/64] |= 1 << uint(value%64)
	}
	set := simd.NewByteSet(raw)
	tables := set.TableVector()
	window := make([]byte, simd.SuperWidth)
	for i := range window {
		window[i] = byte('a' + i%26)
	}
	b.ReportAllocs()
	var acc uint32
	for b.Loop() {
		acc |= nativeByteSetMask32AVX2(window, tables)
	}
	if acc == 0 {
		b.Fatal("掩码不应全为零")
	}
}

// avx512Executable 报告当前测试进程能否执行 AVX512BW 指令。
//
// 部分虚拟化环境会在 CPUID 中隐藏 AVX512 能力，因此除常规能力探测外还允许通过
// SCANKIT_AVX512_TESTS=1 显式开启宽窗口内核测试；不具备该指令的主机会以非法指令
// 终止测试进程，属于调用方主动选择的评测行为。VBMI 内核额外要求 AVX512VBMI。
func avx512Executable() bool {
	return cpu.X86.HasAVX512BW || os.Getenv("SCANKIT_AVX512_TESTS") == "1"
}

// avx512VBMIExecutable 报告当前测试进程能否执行 AVX512VBMI 指令。
func avx512VBMIExecutable() bool {
	return (cpu.X86.HasAVX512VBMI && cpu.X86.HasAVX512BW) || os.Getenv("SCANKIT_AVX512_TESTS") == "1"
}

// nativeWideMaskReference 逐字节查表给出单个 lane 在 64 字节宽窗口上的原始位置掩码，
// 不施加 lane 组合与末端退让，作为 512 位内核的直接对照。
func nativeWideMaskReference(window []byte, tables *[32]byte) uint64 {
	var mask uint64
	for i, value := range window[:simd.WideWidth] {
		index := int(value>>4) + 16*int((value>>3)&1)
		if tables[index]&(1<<uint(value&7)) != 0 {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// wideKernelCases 构造覆盖全部 256 个字节取值、稀疏集合与随机窗口的内核测试语料。
func wideKernelCases() []struct {
	window []byte
	raw    [4]uint64
} {
	sets := x86ByteSetSets()
	cases := make([]struct {
		window []byte
		raw    [4]uint64
	}, 0, 256+len(sets))
	// 每个窗口内所有字节相同，逐值确认命中/未命中判定与 64 位位序。
	for b := range 256 {
		window := make([]byte, simd.WideWidth)
		for i := range window {
			window[i] = byte(b)
		}
		var raw [4]uint64
		raw[b/64] = 1 << uint(b%64)
		cases = append(cases, struct {
			window []byte
			raw    [4]uint64
		}{window, raw})
	}
	// 单字节集合逐窗口位置验证位序，跨四个 128 位半区不发生错位。
	rng := rand.New(rand.NewSource(31))
	for _, raw := range sets {
		window := make([]byte, simd.WideWidth)
		for i := range window {
			window[i] = byte(rng.Intn(256))
		}
		cases = append(cases, struct {
			window []byte
			raw    [4]uint64
		}{window, raw})
	}
	for b := range 256 {
		window := make([]byte, simd.WideWidth)
		for i := range window {
			window[i] = byte(b + 1)
		}
		window[b%simd.WideWidth] = byte(b)
		var raw [4]uint64
		raw[b/64] = 1 << uint(b%64)
		cases = append(cases, struct {
			window []byte
			raw    [4]uint64
		}{window, raw})
	}
	return cases
}

// TestNativeByteSetMask64AVX512MatchesReference 覆盖全部字节取值与随机窗口，
// 校验 AVX512BW 宽窗口内核的命中判定与位序。
func TestNativeByteSetMask64AVX512MatchesReference(t *testing.T) {
	if !avx512Executable() {
		t.Skip("当前环境未声明 AVX512BW 能力")
	}
	for i, tc := range wideKernelCases() {
		set := simd.NewByteSet(tc.raw)
		want := nativeWideMaskReference(tc.window, set.TableVector())
		if got := nativeByteSetMask64AVX512(tc.window, set.TableVector()); got != want {
			t.Fatalf("用例=%d raw=%v got=%064b want=%064b", i, tc.raw, got, want)
		}
	}
}

// TestNativeByteSetMask64VBMIMatchesReference 覆盖全部字节取值与随机窗口，
// 校验 AVX512VBMI 宽窗口内核的命中判定与位序。
func TestNativeByteSetMask64VBMIMatchesReference(t *testing.T) {
	if !avx512VBMIExecutable() {
		t.Skip("当前环境未声明 AVX512VBMI 能力")
	}
	for i, tc := range wideKernelCases() {
		set := simd.NewByteSet(tc.raw)
		want := nativeWideMaskReference(tc.window, set.TableVector())
		if got := nativeByteSetMask64VBMI(tc.window, set.TableVector()); got != want {
			t.Fatalf("用例=%d raw=%v got=%064b want=%064b", i, tc.raw, got, want)
		}
	}
}

// TestNativeByteSetMask64KernelsAgree 对照 AVX512BW 与 VBMI 两条宽窗口内核的实现差异。
func TestNativeByteSetMask64KernelsAgree(t *testing.T) {
	if !avx512VBMIExecutable() {
		t.Skip("当前环境未声明 AVX512VBMI 能力")
	}
	for i, tc := range wideKernelCases() {
		set := simd.NewByteSet(tc.raw)
		bw := nativeByteSetMask64AVX512(tc.window, set.TableVector())
		vbmi := nativeByteSetMask64VBMI(tc.window, set.TableVector())
		if bw != vbmi {
			t.Fatalf("用例=%d raw=%v bw=%064b vbmi=%064b", i, tc.raw, bw, vbmi)
		}
	}
}

// TestWideKernelSelfCheckPasses 确认一次性自检对两条 512 位内核都给出通过结论，
// 保证分派路径不会因为自检误判而永久退回 256 位拼接。
func TestWideKernelSelfCheckPasses(t *testing.T) {
	if !avx512Executable() {
		t.Skip("当前环境未声明 AVX512BW 能力")
	}
	if !verifyWideKernel(nativeByteSetMask64AVX512) {
		t.Fatal("AVX512BW 宽窗口内核未通过自检")
	}
	if avx512VBMIExecutable() && !verifyWideKernel(nativeByteSetMask64VBMI) {
		t.Fatal("AVX512VBMI 宽窗口内核未通过自检")
	}
}

// TestBackendWindowMask64AVX2MatchesScalar 强制生效层级为 AVX2，验证宽窗口分派在
// 256 位拼接回退路径上与通用标量参考逐位一致（含全部 lane 组合与末端退让）。
func TestBackendWindowMask64AVX2MatchesScalar(t *testing.T) {
	if !avx2Executable() {
		t.Skip("当前环境未声明 AVX2 能力")
	}
	backend := Backend{tier: TierAVX2, resolved: TierAVX2}
	rng := rand.New(rand.NewSource(33))
	data := make([]byte, simd.WideWidth*2)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}
	for iter := range 64 {
		var laneSets [4][4]uint64
		for lane := range laneSets {
			for word := range laneSets[lane] {
				if iter%2 == 0 {
					laneSets[lane][word] = rng.Uint64()
					continue
				}
				for range 4 {
					value := rng.Intn(256)
					laneSets[lane][value/64] |= 1 << uint(value%64)
				}
			}
		}
		tables := simd.NewByteSetTables(laneSets)
		for _, off := range []int{0, 1, simd.SuperWidth, simd.WideWidth - 1} {
			for lanes := 1; lanes <= 4; lanes++ {
				want, wantOK := simd.WindowMask64Scalar(data, off, &tables, lanes)
				got, ok := backend.WindowMask64(data, off, &tables, lanes)
				if ok != wantOK || got != want {
					t.Fatalf("迭代 %d 偏移 %d lane %d: got=%064b ok=%v want=%064b wantOK=%v",
						iter, off, lanes, got, ok, want, wantOK)
				}
			}
		}
	}
}

// TestBackendWindowMask64AVX512MatchesScalar 强制生效层级为 AVX512，验证 512 位
// 宽窗口内核与通用标量参考逐位一致；不具备该指令的主机会跳过。
func TestBackendWindowMask64AVX512MatchesScalar(t *testing.T) {
	if !avx512Executable() {
		t.Skip("当前环境未声明 AVX512BW 能力")
	}
	rng := rand.New(rand.NewSource(35))
	data := make([]byte, simd.WideWidth*2)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}
	for _, tier := range []Tier{TierAVX512, TierAVX512VBMI} {
		if tier == TierAVX512VBMI && !avx512VBMIExecutable() {
			continue
		}
		backend := Backend{tier: tier, resolved: tier}
		for iter := range 64 {
			var laneSets [4][4]uint64
			for lane := range laneSets {
				for word := range laneSets[lane] {
					laneSets[lane][word] = rng.Uint64()
				}
			}
			tables := simd.NewByteSetTables(laneSets)
			for _, off := range []int{0, 1, simd.WideWidth - 1} {
				for lanes := 1; lanes <= 4; lanes++ {
					want, wantOK := simd.WindowMask64Scalar(data, off, &tables, lanes)
					got, ok := backend.WindowMask64(data, off, &tables, lanes)
					if ok != wantOK || got != want {
						t.Fatalf("层级=%v 迭代 %d 偏移 %d lane %d: got=%064b ok=%v want=%064b wantOK=%v",
							tier, iter, off, lanes, got, ok, want, wantOK)
					}
				}
			}
		}
	}
}

// BenchmarkNativeByteSetMask64 记录 512 位宽窗口内核的单次成本，用于对照 256 位拼接。
func BenchmarkNativeByteSetMask64(b *testing.B) {
	var raw [4]uint64
	for value := 'a'; value <= 'z'; value += 2 {
		raw[value/64] |= 1 << uint(value%64)
	}
	set := simd.NewByteSet(raw)
	tables := set.TableVector()
	window := make([]byte, simd.WideWidth)
	for i := range window {
		window[i] = byte('a' + i%26)
	}
	if avx512Executable() {
		b.Run("avx512bw", func(b *testing.B) {
			b.ReportAllocs()
			var acc uint64
			for i := 0; i < b.N; i++ {
				acc |= nativeByteSetMask64AVX512(window, tables)
			}
			if acc == 0 {
				b.Fatal("掩码不应全为零")
			}
		})
	}
	if avx512VBMIExecutable() {
		b.Run("avx512vbmi", func(b *testing.B) {
			b.ReportAllocs()
			var acc uint64
			for i := 0; i < b.N; i++ {
				acc |= nativeByteSetMask64VBMI(window, tables)
			}
			if acc == 0 {
				b.Fatal("掩码不应全为零")
			}
		})
	}
}
