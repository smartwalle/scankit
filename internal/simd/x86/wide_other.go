//go:build !amd64

package x86

import "github.com/smartwalle/scankit/internal/simd"

// WindowMask64 在非 x86 平台沿用通用标量参考实现，保证跨平台语义一致。
func (b Backend) WindowMask64(data []byte, off int, tables *simd.ByteSetTables, lanes int) (uint64, bool) {
	return simd.WindowMask64Scalar(data, off, tables, lanes)
}
