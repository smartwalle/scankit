#include "textflag.h"

// SSE2 无符号闭区间比较。
//
// 利用饱和减法构造“零即命中”的判据：v-hi 与 lo-v 同时为 0 时 v 落在 [lo, hi]。
TEXT ·nativeInRangeMask(SB), NOSPLIT, $0-18
	MOVQ v+0(FP), AX
	MOVBQZX lo+8(FP), CX
	MOVBQZX hi+9(FP), DX
	MOVOU (AX), X0
	MOVD CX, X1
	PUNPCKLBW X1, X1
	PUNPCKLWL X1, X1
	PSHUFD $0, X1, X1
	MOVD DX, X2
	PUNPCKLBW X2, X2
	PUNPCKLWL X2, X2
	PSHUFD $0, X2, X2
	MOVOU X0, X3
	PSUBUSB X2, X3
	MOVOU X1, X4
	PSUBUSB X0, X4
	POR X4, X3
	PXOR X5, X5
	PCMPEQB X5, X3
	PMOVMSKB X3, DX
	MOVW DX, ret+16(FP)
	RET
