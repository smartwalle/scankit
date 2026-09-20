#include "textflag.h"

// NEON 字节比较向量实现。
TEXT ·nativeEqualByteVector(SB), NOSPLIT, $0-24
	MOVD v+0(FP), R0
	MOVBU value+8(FP), R1
	MOVD out+16(FP), R2
	VDUP R1, V0.B16
	VLD1 (R0), [V1.B16]
	VCMEQ V0.B16, V1.B16, V1.B16
	VST1 [V1.B16], (R2)
	RET

// NEON 双向量逐字节比较。
TEXT ·nativeEqualVector(SB), NOSPLIT, $0-24
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	MOVD out+16(FP), R2
	VLD1 (R0), [V0.B16]
	VLD1 (R1), [V1.B16]
	VCMEQ V1.B16, V0.B16, V0.B16
	VST1 [V0.B16], (R2)
	RET
