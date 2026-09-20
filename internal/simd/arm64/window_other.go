//go:build !arm64

package arm64

import "github.com/smartwalle/scankit/internal/simd"

// WindowMask 在非 ARM64 平台沿用通用标量参考实现，保证跨平台语义一致。
func (b Backend) WindowMask(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint32, bool) {
	return simd.WindowMaskScalar(data, off, tables, lanes)
}

// WindowMask64 在非 ARM64 平台沿用通用标量参考实现，保证跨平台语义一致。
func (b Backend) WindowMask64(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint64, bool) {
	return simd.WindowMask64Scalar(data, off, tables, lanes)
}
