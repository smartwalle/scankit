package simd_test

import (
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/simd"
	armbackend "github.com/smartwalle/scankit/internal/simd/arm64"
	x86backend "github.com/smartwalle/scankit/internal/simd/x86"
)

// TestBackendConformance 验证各架构入口在边界、非对齐和掩码场景下保持一致语义。
func TestBackendConformance(t *testing.T) {
	backends := []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.New()},
		{"arm64", armbackend.New()},
	}
	data := append([]byte("_"), []byte("abcdefghijklmnop")...)
	for _, tc := range backends {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := tc.b.Load(data, -1); ok {
				t.Fatal("负偏移完整加载未拒绝")
			}
			partial := tc.b.PartialLoad(data, len(data)-2)
			if partial[0] != 'o' || partial[1] != 'p' {
				t.Fatalf("尾部加载错误: %v", partial)
			}
			v := tc.b.PartialLoad(data, 1)
			if tc.b.EqualByteMask(v, 'a') != 1 || tc.b.InRangeMask(v, 'a', 'a') != 1 {
				t.Fatal("非对齐比较错误")
			}
			var set [4]uint64
			set['a'/64] |= 1 << ('a' % 64)
			set['p'/64] |= 1 << ('p' % 64)
			if got := tc.b.ByteSetMask(v, set); got != 1|(1<<15) {
				t.Fatalf("字节集合比较错误: %x", got)
			}
			if got := tc.b.Permute(v, simd.Vector{0}); got[0] != 'a' {
				t.Fatalf("索引重排错误: %v", got)
			}
			if tc.b.Store(make([]byte, 15), 0, v) {
				t.Fatal("短目标写入未拒绝")
			}
		})
	}
}

func TestBackendByteSetAndNativeMasksCoverAllValues(t *testing.T) {
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i * 37)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.b.PartialLoad(data, 3)
			for value := range 256 {
				var set [4]uint64
				set[value/64] |= 1 << uint(value%64)
				want := uint16(0)
				for i, item := range v {
					if item == byte(value) {
						want |= 1 << uint(i)
					}
				}
				if got := tc.b.ByteSetMask(v, set); got != want {
					t.Fatalf("value=%d got=%x want=%x", value, got, want)
				}
			}
		})
	}
}

// TestBackendInRangeMaskCoversAllValues 验证各架构后端的闭区间范围掩码
// 在全部 (lo, hi) 组合上与通用实现逐位一致，覆盖原生路径和回退路径。
func TestBackendInRangeMaskCoversAllValues(t *testing.T) {
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i * 37)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.b.PartialLoad(data, 3)
			for lo := range 256 {
				for hi := lo; hi < 256; hi++ {
					var want uint16
					for i, item := range v {
						if item >= byte(lo) && item <= byte(hi) {
							want |= 1 << uint(i)
						}
					}
					if got := tc.b.InRangeMask(v, byte(lo), byte(hi)); got != want {
						t.Fatalf("lo=%d hi=%d got=%016b want=%016b", lo, hi, got, want)
					}
				}
			}
		})
	}
}

// TestBackendPreparedByteSetCoversAllValues 验证三个后端在预编译字节集合路径上
// 与通用实现逐位一致，覆盖每个字节在每个 lane 上的命中位置。
func TestBackendPreparedByteSetCoversAllValues(t *testing.T) {
	rawSets := [][4]uint64{
		{},
		{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)},
		// 半字节行的两侧：仅低 8 位与仅高 8 位。
		{0x00000000000000FF, 0, 0, 0},
		{0x000000000000FF00, 0, 0, 0},
		{0, 0, 0xFF00000000000000, 0xFF00000000000000},
	}
	for i := range 32 {
		var raw [4]uint64
		for w := range raw {
			raw[w] = uint64(i) * 0x9E3779B97F4A7C15 * uint64(w+1)
		}
		rawSets = append(rawSets, raw)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, raw := range rawSets {
				set := simd.NewByteSet(raw)
				for value := range 256 {
					for lane := range simd.SuperWidth / 2 {
						var v simd.Vector
						v[lane] = byte(value)
						want := simd.GenericBackend{}.ByteSetMask(v, raw)
						if got := tc.b.ByteSetMaskPrepared(v, &set); got != want {
							t.Fatalf("raw=%v value=%d lane=%d got=%#x want=%#x", raw, value, lane, got, want)
						}
					}
				}
			}
		})
	}
}

// TestBackendPreparedByteSetMatchesRaw 验证预编译路径与原始位图路径在所有字节取值上一致。
func TestBackendPreparedByteSetMatchesRaw(t *testing.T) {
	var raw [4]uint64
	for b := range 256 {
		if b%3 == 0 || b >= 128 {
			raw[b/64] |= 1 << uint(b%64)
		}
	}
	set := simd.NewByteSet(raw)
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v simd.Vector
			for i := range v {
				v[i] = byte(i*11 + 3)
			}
			if got, want := tc.b.ByteSetMaskPrepared(v, &set), tc.b.ByteSetMask(v, raw); got != want {
				t.Fatalf("got=%#x want=%#x", got, want)
			}
			if got := tc.b.ByteSetMaskPrepared(v, nil); got != 0 {
				t.Fatalf("nil set must yield zero mask, got %#x", got)
			}
		})
	}
}

func confFoldASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// TestBackendEqualMasksCoverAllValues 验证三个后端的逐字节等值、折叠等值与双向量
// 等值掩码在全部取值与全部 lane 位置上与逐字节语义一致，覆盖原生与回退路径。
func TestBackendEqualMasksCoverAllValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for candidate := range 256 {
				for lane := range simd.SuperWidth / 2 {
					for _, item := range []byte{byte(candidate), byte(candidate ^ 0x20), byte(candidate ^ 0x80), 0} {
						var v simd.Vector
						v[lane] = item
						var wantEqual, wantFold uint16
						for i, c := range v {
							if c == byte(candidate) {
								wantEqual |= 1 << uint(i)
							}
							if confFoldASCII(c) == confFoldASCII(byte(candidate)) {
								wantFold |= 1 << uint(i)
							}
						}
						if got := tc.b.EqualByteMask(v, byte(candidate)); got != wantEqual {
							t.Fatalf("EqualByteMask candidate=%d lane=%d item=%d got=%#x want=%#x", candidate, lane, item, got, wantEqual)
						}
						if got := tc.b.EqualByteMaskFold(v, byte(candidate)); got != wantFold {
							t.Fatalf("EqualByteMaskFold candidate=%d lane=%d item=%d got=%#x want=%#x", candidate, lane, item, got, wantFold)
						}
						// 仅在第 lane 个字节上制造差异，其余位置保持相同。
						other := v
						other[lane] = item ^ 0xFF
						if got, want := tc.b.EqualMask(v, other), ^uint16(0)^(1<<uint(lane)); got != want {
							t.Fatalf("EqualMask 单 lane 差异 lane=%d got=%#x want=%#x", lane, got, want)
						}
						if got := tc.b.EqualMask(v, v); got != ^uint16(0) {
							t.Fatalf("EqualMask 同向量 lane=%d got=%#x", lane, got)
						}
					}
				}
			}
			// 整向量跨 lane 抽查，避免只覆盖单 lane 场景。
			for shift := range 256 {
				var a, b simd.Vector
				for i := range a {
					a[i] = byte((shift + i) & 0xFF)
					b[i] = byte((shift + i*3) & 0xFF)
				}
				var want uint16
				for i := range a {
					if a[i] == b[i] {
						want |= 1 << uint(i)
					}
				}
				if got := tc.b.EqualMask(a, b); got != want {
					t.Fatalf("EqualMask shift=%d got=%#x want=%#x", shift, got, want)
				}
			}
		})
	}
}

// TestBackendWindowMaskMatchesScalar 验证各后端在全部 lane 数量、多个偏移和
// 稀疏/稠密集合下生成的超向量窗口掩码与通用标量参考逐位一致。
func TestBackendWindowMaskMatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(20260923))
	backends := []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"x86/scalar", x86backend.NewWithTier(x86backend.TierScalar)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
		{"arm64/scalar", armbackend.NewWithTier(armbackend.TierScalar)},
	}
	data := make([]byte, simd.SuperWidth*3)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}
	for _, tc := range backends {
		t.Run(tc.name, func(t *testing.T) {
			for iter := range 64 {
				var laneSets [4][4]uint64
				for lane := range laneSets {
					for word := range laneSets[lane] {
						if iter%2 == 0 {
							laneSets[lane][word] = rng.Uint64()
							continue
						}
						// 稀疏集合：每个 lane 只保留少量候选字节，覆盖查找表全零行。
						for range 4 {
							value := rng.Intn(256)
							laneSets[lane][value/64] |= 1 << uint(value%64)
						}
					}
				}
				tables := simd.NewByteSetTables(laneSets)
				for _, off := range []int{0, 1, 2, simd.SuperWidth, 2 * simd.SuperWidth} {
					for lanes := 1; lanes <= 4; lanes++ {
						want, wantOK := simd.WindowMaskScalar(data, off, &tables, lanes)
						got, ok := tc.b.WindowMask(data, off, &tables, lanes)
						if ok != wantOK || got != want {
							t.Fatalf("迭代 %d 偏移 %d lane %d: got=%032b ok=%v want=%032b wantOK=%v",
								iter, off, lanes, got, ok, want, wantOK)
						}
					}
				}
			}
			if _, ok := tc.b.WindowMask(data, -1, &simd.ByteSetTables{}, 1); ok {
				t.Fatal("负偏移未被拒绝")
			}
			if _, ok := tc.b.WindowMask(data, len(data)-simd.SuperWidth+1, &simd.ByteSetTables{}, 1); ok {
				t.Fatal("不足一个窗口的尾部未被拒绝")
			}
			if got, ok := tc.b.WindowMask(data, 0, nil, 1); ok || got != 0 {
				t.Fatalf("空查找表未被拒绝: %032b", got)
			}
		})
	}
}

// TestBackendWindowMaskCoversAllBytes 验证全部 256 个字节在同一窗口位置上的
// 掩码位序与参考实现一致：全集命中全部位置，空集不命中任何位置。
func TestBackendWindowMaskCoversAllBytes(t *testing.T) {
	window := make([]byte, simd.SuperWidth)
	for i := range window {
		window[i] = byte(i * 8 % 256)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := simd.NewByteSetTables([4][4]uint64{{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}})
			if got, ok := tc.b.WindowMask(window, 0, &full, 1); !ok || got != ^uint32(0) {
				t.Fatalf("全集应命中全部位置: got=%032b ok=%v", got, ok)
			}
			empty := simd.NewByteSetTables([4][4]uint64{})
			if got, ok := tc.b.WindowMask(window, 0, &empty, 4); !ok || got != 0 {
				t.Fatalf("空集不应命中任何位置: got=%032b ok=%v", got, ok)
			}
		})
	}
}

// BenchmarkWindowMaskBackends 记录各后端在 32 字节窗口判定上的吞吐与单窗口成本，
// 用于对照通用标量实现、SSE 半区拼接与 NEON 合并路径。
func BenchmarkWindowMaskBackends(b *testing.B) {
	var laneSets [4][4]uint64
	for lane := range 4 {
		for value := 0; value < 256; value += 3 {
			laneSets[lane][value/64] |= 1 << uint(value%64)
		}
	}
	tables := simd.NewByteSetTables(laneSets)
	data := make([]byte, simd.SuperWidth*256)
	for i := range data {
		data[i] = byte(i * 37)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		for _, lanes := range []int{1, 4} {
			b.Run(tc.name+"/lanes"+string(rune('0'+lanes)), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				var acc uint32
				for i := 0; i < b.N; i++ {
					for off := 0; off+simd.SuperWidth <= len(data); off += simd.SuperWidth {
						mask, ok := tc.b.WindowMask(data, off, &tables, lanes)
						if !ok {
							b.Fatal("窗口掩码不应越界")
						}
						acc |= mask
					}
				}
				if acc == 0 {
					b.Fatal("掩码不应全为零")
				}
			})
		}
	}
}

// TestBackendWindowMask64MatchesScalar 验证各后端在全部 lane 数量、多个偏移和
// 稀疏/稠密集合下生成的 64 字节宽窗口掩码与通用标量参考逐位一致。
func TestBackendWindowMask64MatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(20260926))
	backends := []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"x86/scalar", x86backend.NewWithTier(x86backend.TierScalar)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
		{"arm64/scalar", armbackend.NewWithTier(armbackend.TierScalar)},
	}
	data := make([]byte, simd.WideWidth*2)
	for i := range data {
		data[i] = byte(rng.Intn(256))
	}
	for _, tc := range backends {
		t.Run(tc.name, func(t *testing.T) {
			for iter := range 64 {
				var laneSets [4][4]uint64
				for lane := range laneSets {
					for word := range laneSets[lane] {
						if iter%2 == 0 {
							laneSets[lane][word] = rng.Uint64()
							continue
						}
						// 稀疏集合：每个 lane 只保留少量候选字节，覆盖查找表全零行。
						for range 4 {
							value := rng.Intn(256)
							laneSets[lane][value/64] |= 1 << uint(value%64)
						}
					}
				}
				tables := simd.NewByteSetTables(laneSets)
				for _, off := range []int{0, 1, 2, 3, simd.WideWidth - 1, simd.WideWidth} {
					for lanes := 1; lanes <= 4; lanes++ {
						want, wantOK := simd.WindowMask64Scalar(data, off, &tables, lanes)
						got, ok := tc.b.WindowMask64(data, off, &tables, lanes)
						if ok != wantOK || got != want {
							t.Fatalf("迭代 %d 偏移 %d lane %d: got=%064b ok=%v want=%064b wantOK=%v",
								iter, off, lanes, got, ok, want, wantOK)
						}
					}
				}
			}
			if _, ok := tc.b.WindowMask64(data, -1, &simd.ByteSetTables{}, 1); ok {
				t.Fatal("负偏移未被拒绝")
			}
			if _, ok := tc.b.WindowMask64(data, len(data)-simd.WideWidth+1, &simd.ByteSetTables{}, 1); ok {
				t.Fatal("不足一个宽窗口的尾部未被拒绝")
			}
			if got, ok := tc.b.WindowMask64(data, 0, nil, 1); ok || got != 0 {
				t.Fatalf("空查找表未被拒绝: %064b", got)
			}
		})
	}
}

// TestBackendWindowMask64CoversAllBytes 验证全部 256 个字节在宽窗口各位置上的
// 掩码位序与参考实现一致：全集命中全部位置，空集不命中任何位置。
func TestBackendWindowMask64CoversAllBytes(t *testing.T) {
	window := make([]byte, simd.WideWidth)
	for i := range window {
		window[i] = byte(i * 4 % 256)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := simd.NewByteSetTables([4][4]uint64{{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}})
			if got, ok := tc.b.WindowMask64(window, 0, &full, 1); !ok || got != ^uint64(0) {
				t.Fatalf("全集应命中全部位置: got=%064b ok=%v", got, ok)
			}
			empty := simd.NewByteSetTables([4][4]uint64{})
			if got, ok := tc.b.WindowMask64(window, 0, &empty, 4); !ok || got != 0 {
				t.Fatalf("空集不应命中任何位置: got=%064b ok=%v", got, ok)
			}
		})
	}
}

// BenchmarkWindowMask64Backends 记录各后端在 64 字节宽窗口判定上的吞吐与单窗口成本，
// 用于对照通用标量实现、SSE/AVX2 半区拼接与 AVX512 单寄存器路径。
func BenchmarkWindowMask64Backends(b *testing.B) {
	var laneSets [4][4]uint64
	for lane := range 4 {
		for value := 0; value < 256; value += 3 {
			laneSets[lane][value/64] |= 1 << uint(value%64)
		}
	}
	tables := simd.NewByteSetTables(laneSets)
	data := make([]byte, simd.WideWidth*256)
	for i := range data {
		data[i] = byte(i * 37)
	}
	for _, tc := range []struct {
		name string
		b    simd.Backend
	}{
		{"generic", simd.Default()},
		{"x86", x86backend.NewWithTier(x86backend.TierAVX512VBMI)},
		{"arm64", armbackend.NewWithTier(armbackend.TierSVE2)},
	} {
		for _, lanes := range []int{1, 4} {
			b.Run(tc.name+"/lanes"+string(rune('0'+lanes)), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				var acc uint64
				for i := 0; i < b.N; i++ {
					for off := 0; off+simd.WideWidth <= len(data); off += simd.WideWidth {
						mask, ok := tc.b.WindowMask64(data, off, &tables, lanes)
						if !ok {
							b.Fatal("窗口掩码不应越界")
						}
						acc |= mask
					}
				}
				if acc == 0 {
					b.Fatal("掩码不应全为零")
				}
			})
		}
	}
}

// BenchmarkWindowMask32Vs64PerByte 在同一份 4KiB 语料上对照 32 字节窗口与 64 字节
// 宽窗口掩码内核的同字节成本，用于确认宽窗口内核本身不会引入回归。
func BenchmarkWindowMask32Vs64PerByte(b *testing.B) {
	var laneSets [4][4]uint64
	for lane := range 4 {
		// 全部小写字母都进入集合，保证 lanes>=2 的严格判定也有命中。
		for value := byte('a'); value <= 'z'; value++ {
			laneSets[lane][value/64] |= 1 << uint(value%64)
		}
	}
	tables := simd.NewByteSetTables(laneSets)
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	backend := simd.Default()
	for _, lanes := range []int{1, 2, 4} {
		b.Run("w32/lanes"+string(rune('0'+lanes)), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			var acc uint32
			for i := 0; i < b.N; i++ {
				for off := 0; off+simd.SuperWidth <= len(data); off += simd.SuperWidth {
					mask, ok := backend.WindowMask(data, off, &tables, lanes)
					if !ok {
						b.Fatal("窗口掩码不应越界")
					}
					acc |= mask
				}
			}
			if acc == 0 {
				b.Fatal("掩码不应全为零")
			}
		})
		b.Run("w64/lanes"+string(rune('0'+lanes)), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			var acc uint64
			for i := 0; i < b.N; i++ {
				for off := 0; off+simd.WideWidth <= len(data); off += simd.WideWidth {
					mask, ok := backend.WindowMask64(data, off, &tables, lanes)
					if !ok {
						b.Fatal("宽窗口掩码不应越界")
					}
					acc |= mask
				}
			}
			if acc == 0 {
				b.Fatal("掩码不应全为零")
			}
		})
	}
}
