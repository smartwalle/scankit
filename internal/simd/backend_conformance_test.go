package simd_test

import (
	"github.com/smartwalle/scankit/internal/simd"
	armbackend "github.com/smartwalle/scankit/internal/simd/arm64"
	x86backend "github.com/smartwalle/scankit/internal/simd/x86"
	"testing"
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
			for value := 0; value < 256; value++ {
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
