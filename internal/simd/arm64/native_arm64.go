//go:build arm64

package arm64

import "github.com/smartwalle/scankit/internal/simd/generic"

func nativeEqualByteVector(v *generic.Vector, value byte, out *generic.Vector)

func nativeEqualVector(a, b *generic.Vector, out *generic.Vector)
