//go:build !amd64 && !arm64

package scankit

const singleByteWordScanAvailable = false

func singleByteTriggerMask(data []byte, offset int, first, second byte) uint64 {
	var mask uint64
	for lane := 0; lane < 8; lane++ {
		value := data[offset+lane]
		if value == first || value == second {
			mask |= uint64(0x80) << (lane * 8)
		}
	}
	return mask
}

func rootByteTriggerMask(data []byte, offset int, values [8]byte, count uint8) uint64 {
	var mask uint64
	for lane := 0; lane < 8; lane++ {
		value := data[offset+lane]
		for index := uint8(0); index < count; index++ {
			if value == values[index] {
				mask |= uint64(0x80) << (lane * 8)
				break
			}
		}
	}
	return mask
}

func rootByteSingleTriggerMask(data []byte, offset int, value byte) uint64 {
	var mask uint64
	for lane := 0; lane < 8; lane++ {
		if data[offset+lane] == value {
			mask |= uint64(0x80) << (lane * 8)
		}
	}
	return mask
}
