#include "textflag.h"

// NEON 字节比较掩码：一次完成 16 字节比较，
// 再用 SWAR 乘加把每 8 个命中字节压缩成一个字节，直接返回 16 位位置掩码。
TEXT ·nativeEqualByteMask(SB), NOSPLIT, $0-18
	MOVD v+0(FP), R0
	MOVBU value+8(FP), R1
	VDUP R1, V0.B16
	VLD1 (R0), [V1.B16]
	VCMEQ V0.B16, V1.B16, V2.B16
	VUSHR $7, V2.B16, V2.B16
	VMOV V2.D[0], R5
	VMOV V2.D[1], R6
	MOVD $0x0102040810204080, R4
	MUL R4, R5, R5
	MUL R4, R6, R6
	LSR $56, R5, R5
	LSR $56, R6, R6
	// D[1] 对应字节 8..15，需要放到掩码高位，D[0] 留在低位。
	LSL $8, R6, R6
	ORR R6, R5, R5
	MOVH R5, ret+16(FP)
	RET

// NEON 大小写折叠等值掩码：上下两种取值合并后一次压缩为位置掩码。
TEXT ·nativeEqualByteMaskFold(SB), NOSPLIT, $0-18
	MOVD v+0(FP), R0
	MOVBU lower+8(FP), R1
	MOVBU upper+9(FP), R2
	VDUP R1, V0.B16
	VDUP R2, V3.B16
	VLD1 (R0), [V1.B16]
	VCMEQ V0.B16, V1.B16, V2.B16
	VCMEQ V3.B16, V1.B16, V4.B16
	VORR V4.B16, V2.B16, V2.B16
	VUSHR $7, V2.B16, V2.B16
	VMOV V2.D[0], R5
	VMOV V2.D[1], R6
	MOVD $0x0102040810204080, R4
	MUL R4, R5, R5
	MUL R4, R6, R6
	LSR $56, R5, R5
	LSR $56, R6, R6
	LSL $8, R6, R6
	ORR R6, R5, R5
	MOVH R5, ret+16(FP)
	RET

// NEON 双向量等值掩码。
TEXT ·nativeEqualMask(SB), NOSPLIT, $0-18
	MOVD a+0(FP), R0
	MOVD b+8(FP), R1
	VLD1 (R0), [V0.B16]
	VLD1 (R1), [V1.B16]
	VCMEQ V1.B16, V0.B16, V2.B16
	VUSHR $7, V2.B16, V2.B16
	VMOV V2.D[0], R5
	VMOV V2.D[1], R6
	MOVD $0x0102040810204080, R4
	MUL R4, R5, R5
	MUL R4, R6, R6
	LSR $56, R5, R5
	LSR $56, R6, R6
	LSL $8, R6, R6
	ORR R6, R5, R5
	MOVH R5, ret+16(FP)
	RET

// NEON 无符号闭区间比较：一次完成 16 字节比较，
// 再用 SWAR 乘加把每 8 个命中字节压缩成一个字节，直接返回 16 位位置掩码。
TEXT ·nativeInRangeMask(SB), NOSPLIT, $0-18
	MOVD v+0(FP), R0
	MOVBU lo+8(FP), R1
	MOVBU hi+9(FP), R2
	VLD1 (R0), [V2.B16]
	VDUP R1, V0.B16
	VDUP R2, V1.B16
	VCMHS V0.B16, V2.B16, V3.B16
	VCMHS V2.B16, V1.B16, V4.B16
	VAND V4.B16, V3.B16, V3.B16
	VUSHR $7, V3.B16, V3.B16
	VMOV V3.D[0], R5
	VMOV V3.D[1], R6
	MOVD $0x0102040810204080, R4
	MUL R4, R5, R5
	MUL R4, R6, R6
	LSR $56, R5, R5
	LSR $56, R6, R6
	// D[1] 对应字节 8..15，需要放到掩码高位，D[0] 留在低位。
	LSL $8, R6, R6
	ORR R6, R5, R5
	MOVH R5, ret+16(FP)
	RET
