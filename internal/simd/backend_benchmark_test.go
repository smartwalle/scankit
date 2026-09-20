package simd_test

import (
	"testing"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/simd"
)

var benchmarkMask uint16

// BenchmarkBackendEqualByteMask 记录通用后端与主机分派后端的可重复掩码吞吐。
func BenchmarkBackendEqualByteMask(b *testing.B) {
	data := []byte("0123456789abcdef0123456789abcdef")
	generic := simd.GenericBackend{}
	host := dispatch.DefaultBackend()
	b.Run("generic", func(b *testing.B) {
		v, _ := generic.Load(data, 0)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkMask ^= generic.EqualByteMask(v, 'a')
		}
	})
	b.Run("dispatch", func(b *testing.B) {
		v, _ := host.Load(data, 0)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkMask ^= host.EqualByteMask(v, 'a')
		}
	})
}
