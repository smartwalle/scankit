//go:build arm64

package arm64

import (
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/simd"
)

// TestNativeByteSetMask32CoversAllValues 验证 32 字节原生判定对全部 256 个取值
// 的命中/未命中判定都正确，覆盖两个 128 位半区。
func TestNativeByteSetMask32CoversAllValues(t *testing.T) {
	for _, raw := range bytesetSets() {
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

// TestNativeByteSetMask32PinsBitOrder 使用单字节集合逐位置验证掩码位序。
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
	rng := rand.New(rand.NewSource(17))
	sets := bytesetSets()
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

// BenchmarkNativeByteSetMask32 记录 32 字节 NEON 判定的单次成本。
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

// TestNativeByteSetMask64CoversAllValues 验证 64 字节原生判定对全部 256 个取值
// 的命中/未命中判定都正确，覆盖四个 128 位半区。
func TestNativeByteSetMask64CoversAllValues(t *testing.T) {
	for _, raw := range bytesetSets() {
		set := simd.NewByteSet(raw)
		for b := range 256 {
			window := make([]byte, simd.WideWidth)
			for i := range window {
				window[i] = byte(b)
			}
			want := uint64(0)
			if raw[b/64]&(1<<uint(b%64)) != 0 {
				want = ^uint64(0)
			}
			if got := nativeByteSetMask64(&window[0], set.TableVector()); got != want {
				t.Fatalf("raw=%v byte=%d got=%064b want=%064b", raw, b, got, want)
			}
		}
	}
}

// TestNativeByteSetMask64PinsBitOrder 使用单字节集合逐位置验证掩码位序。
func TestNativeByteSetMask64PinsBitOrder(t *testing.T) {
	for b := range 256 {
		var raw [4]uint64
		raw[b/64] = 1 << uint(b%64)
		set := simd.NewByteSet(raw)
		for lane := range simd.WideWidth {
			window := make([]byte, simd.WideWidth)
			for i := range window {
				window[i] = byte(b + 1)
			}
			window[lane] = byte(b)
			want := uint64(1) << uint(lane)
			if got := nativeByteSetMask64(&window[0], set.TableVector()); got != want {
				t.Fatalf("byte=%d lane=%d got=%064b want=%064b", b, lane, got, want)
			}
		}
	}
}

// TestNativeByteSetMask64RandomWindows 使用随机窗口核对原生路径与通用实现一致。
func TestNativeByteSetMask64RandomWindows(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	sets := bytesetSets()
	for range 20000 {
		raw := sets[rng.Intn(len(sets))]
		set := simd.NewByteSet(raw)
		window := make([]byte, simd.WideWidth)
		want := uint64(0)
		for j := range window {
			value := byte(rng.Intn(256))
			window[j] = value
			if raw[value/64]&(1<<uint(value%64)) != 0 {
				want |= 1 << uint(j)
			}
		}
		if got := nativeByteSetMask64(&window[0], set.TableVector()); got != want {
			t.Fatalf("raw=%v window=%v got=%064b want=%064b", raw, window, got, want)
		}
	}
}

// BenchmarkNativeByteSetMask64 记录 64 字节 NEON 判定的单次成本，便于与
// 两次 32 字节调用拼接的旧路径比较。
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
	b.ReportAllocs()
	var acc uint64
	for b.Loop() {
		acc |= nativeByteSetMask64(&window[0], tables)
	}
	if acc == 0 {
		b.Fatal("掩码不应全为零")
	}
}
