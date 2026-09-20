#include "textflag.h"

DATA ·byteSetLowNibble<>(SB)/8, $0x0f0f0f0f0f0f0f0f
DATA ·byteSetLowNibble<>+8(SB)/8, $0x0f0f0f0f0f0f0f0f
GLOBL ·byteSetLowNibble<>(SB), RODATA|NOPTR, $16

DATA ·byteSetSevens<>+0(SB)/8, $0x0707070707070707
DATA ·byteSetSevens<>+8(SB)/8, $0x0707070707070707
GLOBL ·byteSetSevens<>(SB), RODATA|NOPTR, $16

// 每个低半字节对应的位置位掩码：1<<(i&7)。
DATA ·byteSetBitTable<>+0(SB)/8, $0x8040201008040201
DATA ·byteSetBitTable<>+8(SB)/8, $0x8040201008040201
GLOBL ·byteSetBitTable<>(SB), RODATA|NOPTR, $16

// PSHUFB 预编译字节集合判定。
//
// 输入按高半字节 h 与低半字节 l 拆分，用两次 PSHUFB 取出该行的低/高 8 位成员图，
// 再按 l 的高位选择、查表得到位选掩码，最后做逐字节与并转换为位置掩码。
TEXT ·nativeByteSetMask(SB), NOSPLIT, $0-18
	MOVQ v+0(FP), AX
	MOVQ tables+8(FP), CX
	MOVOU (AX), X0
	MOVOU X0, X1
	PSRLW $4, X0
	MOVOU ·byteSetLowNibble<>(SB), X3
	MOVOU X1, X2
	PAND X3, X2
	PAND X3, X0
	MOVOU (CX), X4
	MOVOU 16(CX), X5
	MOVOU X0, X6
	PSHUFB X6, X4
	MOVOU X0, X6
	PSHUFB X6, X5
	MOVOU X2, X8
	MOVOU ·byteSetSevens<>(SB), X7
	PCMPGTB X7, X8
	MOVOU X5, X9
	PAND X8, X9
	PANDN X4, X8
	POR X9, X8
	MOVOU X2, X10
	PAND X7, X10
	MOVOU ·byteSetBitTable<>(SB), X11
	PSHUFB X10, X11
	PAND X11, X8
	PXOR X12, X12
	PCMPEQB X12, X8
	PMOVMSKB X8, DX
	XORW $0xffff, DX
	MOVW DX, ret+16(FP)
	RET
