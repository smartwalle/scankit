//go:build arm64

package arm64

import (
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/simd/generic"
)

// TestNativeInRangeVectorMatchesScalar 验证 NEON 原生范围比较在全部
// 区间边界、全字节取值和随机向量上与通用后端逐位一致。
func TestNativeInRangeVectorMatchesScalar(t *testing.T) {
	backend := NewWithTier(TierSVE2)
	reference := simd.GenericBackend{}
	var v generic.Vector
	for i := range v {
		v[i] = byte(i * 17)
	}
	for lo := 0; lo < 256; lo++ {
		for hi := lo; hi < 256; hi++ {
			if got, want := backend.InRangeMask(v, byte(lo), byte(hi)), reference.InRangeMask(v, byte(lo), byte(hi)); got != want {
				t.Fatalf("固定向量 lo=%d hi=%d got=%016b want=%016b", lo, hi, got, want)
			}
		}
	}
	rng := rand.New(rand.NewSource(20240920))
	for i := 0; i < 20000; i++ {
		for j := range v {
			v[j] = byte(rng.Intn(256))
		}
		lo := byte(rng.Intn(256))
		hi := byte(rng.Intn(256))
		if lo > hi {
			lo, hi = hi, lo
		}
		if got, want := backend.InRangeMask(v, lo, hi), reference.InRangeMask(v, lo, hi); got != want {
			t.Fatalf("随机向量 lo=%d hi=%d v=%v got=%016b want=%016b", lo, hi, v, got, want)
		}
	}
}

// TestNativeInRangeVectorBoundaryBytes 验证原生路径覆盖 0x00/0xFF 端点取值，
// 避免有符号/无符号比较混用导致端点漏报。
func TestNativeInRangeVectorBoundaryBytes(t *testing.T) {
	backend := NewWithTier(TierSVE2)
	v := generic.Vector{0x00, 0x7F, 0x80, 0xFF}
	// v 的前四个字节为 0x00/0x7F/0x80/0xFF，其余字节为 0x00。
	cases := []struct {
		lo, hi byte
		want   uint16
	}{
		{0x00, 0x7F, 0x0003 | 0xFFF0},
		{0x80, 0xFF, 0x000C},
		{0x00, 0xFF, 0xFFFF},
		{0x7F, 0x80, 0x0006},
		{0x01, 0x00, 0x0000},
	}
	for _, tc := range cases {
		if got := backend.InRangeMask(v, tc.lo, tc.hi); got != tc.want {
			t.Fatalf("lo=%#x hi=%#x got=%016b want=%016b", tc.lo, tc.hi, got, tc.want)
		}
	}
}

// TestNativeInRangeMaskPacksLanes 验证原生路径的位置位与向量下标一致，
// 覆盖首字节、跨 64 位边界和末字节三个打包边界。
func TestNativeInRangeMaskPacksLanes(t *testing.T) {
	backend := NewWithTier(TierSVE2)
	var v generic.Vector
	v[0], v[3], v[7], v[8], v[15] = 'a', 'a', 'a', 'a', 'a'
	rest := byte('z')
	for i := range v {
		if v[i] == 0 {
			v[i] = rest
		}
	}
	want := uint16(1<<0 | 1<<3 | 1<<7 | 1<<8 | 1<<15)
	if got := backend.InRangeMask(v, 'a', 'a'); got != want {
		t.Fatalf("位打包顺序错误: got=%016b want=%016b", got, want)
	}
}

// BenchmarkInRangeMask 对比 NEON 原生范围掩码与展开标量路径的开销。
func BenchmarkInRangeMask(b *testing.B) {
	native := NewWithTier(TierSVE2)
	scalar := NewWithTier(TierScalar)
	var v generic.Vector
	for i := range v {
		v[i] = byte(i * 7)
	}
	b.Run("native", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = native.InRangeMask(v, 0x20, 0x7E)
		}
	})
	b.Run("scalar", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = scalar.InRangeMask(v, 0x20, 0x7E)
		}
	})
}
