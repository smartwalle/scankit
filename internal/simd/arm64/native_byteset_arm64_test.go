//go:build arm64

package arm64

import (
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/simd"
	"github.com/smartwalle/scankit/internal/simd/generic"
)

func bytesetSets() [][4]uint64 {
	sets := [][4]uint64{
		{},
		{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)},
	}
	// 每个高半字节行单独置位，覆盖 lo/hi 行选择的两侧。
	for h := range 16 {
		var set [4]uint64
		set[h>>2] |= uint64(0xFFFF) << (uint(h&3) * 16)
		sets = append(sets, set)
	}
	// 每个字节单独成集。
	for b := range 256 {
		var set [4]uint64
		set[b/64] = 1 << uint(b%64)
		sets = append(sets, set)
	}
	rng := rand.New(rand.NewSource(4242))
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
	for _, raw := range bytesetSets() {
		set := simd.NewByteSet(raw)
		// 单字节向量：把每个字节放到每个 lane，逐位核对。
		for b := range 256 {
			for lane := range generic.Width {
				var v generic.Vector
				v[lane] = byte(b)
				want := generic.ByteSetMask(v, raw)
				got := nativeByteSetMask(&v, set.TableVector())
				if got != want {
					t.Fatalf("raw=%v byte=%d lane=%d got=%#x want=%#x", raw, b, lane, got, want)
				}
			}
		}
	}
}

// TestNativeByteSetMaskRandomVectors 使用随机窗口核对原生路径与通用实现一致。
func TestNativeByteSetMaskRandomVectors(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	sets := bytesetSets()
	for range 20000 {
		raw := sets[rng.Intn(len(sets))]
		set := simd.NewByteSet(raw)
		var v generic.Vector
		for j := range v {
			v[j] = byte(rng.Intn(256))
		}
		want := generic.ByteSetMask(v, raw)
		got := nativeByteSetMask(&v, set.TableVector())
		if got != want {
			t.Fatalf("raw=%v v=%v got=%#x want=%#x", raw, v, got, want)
		}
	}
}

// TestNativeByteSetMaskBoundaryBytes 固定检查 0、15、16、127、128、255 这些半字节边界。
func TestNativeByteSetMaskBoundaryBytes(t *testing.T) {
	var raw [4]uint64
	boundary := []byte{0, 15, 16, 31, 127, 128, 240, 255}
	for _, b := range boundary {
		raw[b/64] |= 1 << uint(b%64)
	}
	set := simd.NewByteSet(raw)
	var v generic.Vector
	for i := range v {
		v[i] = boundary[i%len(boundary)]
	}
	if got, want := nativeByteSetMask(&v, set.TableVector()), generic.ByteSetMask(v, raw); got != want {
		t.Fatalf("got=%#x want=%#x", got, want)
	}
}

func BenchmarkNativeByteSetMask(b *testing.B) {
	raw := [4]uint64{0, 0x03FF000000000000, 0x7FFFFFE00000000, 0}
	set := simd.NewByteSet(raw)
	var v generic.Vector
	for i := range v {
		v[i] = byte('a' + i%26)
	}
	b.ReportAllocs()
	b.Run("native", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if nativeByteSetMask(&v, set.TableVector()) == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("scalar", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if generic.ByteSetMask(v, raw) == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
}
