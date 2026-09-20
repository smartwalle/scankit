//go:build arm64

package arm64

import (
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/simd/generic"
)

// TestNativeEqualByteMaskCoverage 覆盖全部候选值与全部 lane 位置。
func TestNativeEqualByteMaskCoverage(t *testing.T) {
	for candidate := 0; candidate < 256; candidate++ {
		for lane := 0; lane < generic.Width; lane++ {
			for _, item := range []byte{byte(candidate), byte(candidate ^ 0x20), byte(candidate ^ 0x80), 0} {
				var v generic.Vector
				v[lane] = item
				if got, want := nativeEqualByteMask(&v, byte(candidate)), generic.EqualByteMask(v, byte(candidate)); got != want {
					t.Fatalf("candidate=%d lane=%d item=%d got=%#x want=%#x", candidate, lane, item, got, want)
				}
				lower := byte(candidate)
				if lower >= 'A' && lower <= 'Z' {
					lower += 'a' - 'A'
				}
				upper := lower
				if lower >= 'a' && lower <= 'z' {
					upper = lower - ('a' - 'A')
				}
				if got, want := nativeEqualByteMaskFold(&v, lower, upper), generic.EqualByteMaskFold(v, byte(candidate)); got != want {
					t.Fatalf("fold candidate=%d lane=%d item=%d got=%#x want=%#x", candidate, lane, item, got, want)
				}
			}
		}
	}
}

// TestNativeEqualMaskRandom 使用随机向量对核对双向量等值掩码。
func TestNativeEqualMaskRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(31337))
	for i := 0; i < 20000; i++ {
		var a, b generic.Vector
		for j := range a {
			a[j] = byte(rng.Intn(256))
			// 一半位置强制相等，保证掩码非空与非满都有覆盖。
			if rng.Intn(2) == 0 {
				b[j] = a[j]
			} else {
				b[j] = byte(rng.Intn(256))
			}
		}
		if got, want := nativeEqualMask(&a, &b), generic.EqualMask(a, b); got != want {
			t.Fatalf("a=%v b=%v got=%#x want=%#x", a, b, got, want)
		}
	}
}

func BenchmarkNativeEqualMasks(b *testing.B) {
	var a, c generic.Vector
	for i := range a {
		a[i] = byte('a' + i%26)
		c[i] = a[i]
	}
	c[7] = 'Z'
	b.ReportAllocs()
	b.Run("equalByte_native", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if nativeEqualByteMask(&a, 'a') == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("equalByte_scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if generic.EqualByteMask(a, 'a') == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("equalMask_native", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			nativeEqualMask(&a, &c)
		}
	})
	b.Run("equalMask_scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			generic.EqualMask(a, c)
		}
	})
}
