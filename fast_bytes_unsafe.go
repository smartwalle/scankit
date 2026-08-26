//go:build amd64 || arm64

package scankit

import "unsafe"

const singleByteWordScanAvailable = true

const (
	byteWordOnes = uint64(0x0101010101010101)
	byteWordHigh = uint64(0x8080808080808080)
)

// singleByteTriggerMask 为后续八字节中的每个候选字节返回一个高位。调用方必须在调用前
// 检查 offset+8 边界，并在消费前确认原始字节：零字节位技巧仅是预过滤，跨字节通道借位时
// 不能作为逐通道相等性证明。
func singleByteTriggerMask(data []byte, offset int, first, second byte) uint64 {
	word := *(*uint64)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(data)), offset))
	return byteWordMatchMask(word, first) | byteWordMatchMask(word, second)
}

// rootByteTriggerMask 报告完整输入机器字是否包含一个 Aho-Corasick 根边。编译器将该快路径
// 限制为八个根字节，为普通规则集保留紧凑的纯寄存器预过滤。与 singleByteTriggerMask 相同，
// 调用方必须将结果视为保守结果，因为零字节减法可能标记相邻字节通道。
func rootByteTriggerMask(data []byte, offset int, values [8]byte, count uint8) uint64 {
	word := *(*uint64)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(data)), offset))
	first := byteWordMatchMask(word, values[0])
	if count == 1 {
		return first
	}
	second := first | byteWordMatchMask(word, values[1])
	if count == 2 {
		return second
	}
	third := second | byteWordMatchMask(word, values[2])
	if count == 3 {
		return third
	}
	fourth := third | byteWordMatchMask(word, values[3])
	switch count {
	case 4:
		return fourth
	case 5:
		return fourth | byteWordMatchMask(word, values[4])
	case 6:
		return fourth | byteWordMatchMask(word, values[4]) | byteWordMatchMask(word, values[5])
	case 7:
		return fourth | byteWordMatchMask(word, values[4]) | byteWordMatchMask(word, values[5]) | byteWordMatchMask(word, values[6])
	default:
		return fourth | byteWordMatchMask(word, values[4]) | byteWordMatchMask(word, values[5]) | byteWordMatchMask(word, values[6]) | byteWordMatchMask(word, values[7])
	}
}

// rootByteSingleTriggerMask 是常见单锚定正则通道使用的单根字节特化。将其分离可让编译器
// 内联加载和字节掩码操作，而不携带混合规则集所需的八值分派。
func rootByteSingleTriggerMask(data []byte, offset int, value byte) uint64 {
	word := *(*uint64)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(data)), offset))
	return byteWordMatchMask(word, value)
}

func byteWordRangeAndValuesMask(data []byte, offset int, minimum, maximum byte, values [2]byte, count uint8) uint64 {
	word := *(*uint64)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(data)), offset))
	mask := byteWordRangeMask(word, minimum, maximum)
	if count != 0 {
		mask |= byteWordMatchMask(word, values[0])
	}
	if count == 2 {
		mask |= byteWordMatchMask(word, values[1])
	}
	return mask
}

func byteWordRangeAndSingleMask(data []byte, offset int, minimum, maximum, value byte) uint64 {
	word := *(*uint64)(unsafe.Add(unsafe.Pointer(unsafe.SliceData(data)), offset))
	return byteWordRangeMask(word, minimum, maximum) | byteWordMatchMask(word, value)
}

func byteWordMatchMask(word uint64, value byte) uint64 {
	xor := word ^ uint64(value)*byteWordOnes
	return (xor - byteWordOnes) &^ xor & byteWordHigh
}

// byteWordRangeMask 精确标记 [minimum, maximum] 内的 ASCII byte lanes。计算先清除原
// 高位，使每个 lane 的加减均不会跨 lane 借位或进位；原高位字节在最终交集里被排除。
func byteWordRangeMask(word uint64, minimum, maximum byte) uint64 {
	low := word &^ byteWordHigh
	greaterOrEqual := (low + uint64(0x80-minimum)*byteWordOnes) & byteWordHigh
	lessOrEqual := (uint64(0x80+maximum)*byteWordOnes - low) & byteWordHigh
	return greaterOrEqual & lessOrEqual &^ word
}
