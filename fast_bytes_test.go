package scankit

import "testing"

func TestFastByteMasksMatchScalarReference(t *testing.T) {
	t.Parallel()
	data := []byte{0, 'a', 'b', '@', 'a', 0xff, '@', 'z'}
	values := [8]byte{'a', '@', 'b'}
	if got, want := singleByteTriggerMask(data, 0, 'a', '@'), scalarSingleByteTriggerMask(data, 0, 'a', '@'); got&want != want {
		t.Fatalf("singleByteTriggerMask() = %#x omits scalar candidates %#x", got, want)
	}
	if got, want := singleByteTriggerMask(data, 0, 'a', 'a'), scalarSingleByteTriggerMask(data, 0, 'a', 'a'); got&want != want {
		t.Fatalf("singleByteTriggerMask() with one value = %#x omits scalar candidates %#x", got, want)
	}
	if got, want := rootByteTriggerMask(data, 0, values, 3), scalarRootByteTriggerMask(data, 0, values, 3); got&want != want {
		t.Fatalf("rootByteTriggerMask() = %#x omits scalar candidates %#x", got, want)
	}
	if got, want := rootByteSingleTriggerMask(data, 0, 'a'), scalarRootByteTriggerMask(data, 0, [8]byte{'a'}, 1); got&want != want {
		t.Fatalf("rootByteSingleTriggerMask() = %#x omits scalar candidates %#x", got, want)
	}
	values2 := [2]byte{'@', ':'}
	if got, want := byteWordRangeAndValuesMask(data, 0, '1', '6', values2, 1), scalarRangeAndValuesMask(data, 0, '1', '6', values2, 1); got != want {
		t.Fatalf("byteWordRangeAndValuesMask() = %#x, want %#x", got, want)
	}
	if got, want := byteWordRangeAndSingleMask(data, 0, '1', '6', '@'), scalarRangeAndValuesMask(data, 0, '1', '6', [2]byte{'@'}, 1); got != want {
		t.Fatalf("byteWordRangeAndSingleMask() = %#x, want %#x", got, want)
	}
}

func FuzzFastByteMasksMatchScalarReference(f *testing.F) {
	f.Add([]byte("abcdefgh"), byte('a'), byte('@'), byte(3))
	f.Add([]byte{0, 0xff, '@', 'a', 'b', 'c', 'd', 'e'}, byte('@'), byte('a'), byte(8))
	f.Fuzz(func(t *testing.T, data []byte, first, second, count byte) {
		if len(data) < 8 || len(data) > 128 {
			t.Skip()
		}
		var values [8]byte
		for index := range values {
			values[index] = byte(index*31) ^ first
		}
		count = count%8 + 1
		for offset := 0; offset+8 <= len(data); offset++ {
			if got, want := singleByteTriggerMask(data, offset, first, second), scalarSingleByteTriggerMask(data, offset, first, second); got&want != want {
				t.Fatalf("single mask at %d = %#x omits scalar candidates %#x", offset, got, want)
			}
			if got, want := rootByteTriggerMask(data, offset, values, count), scalarRootByteTriggerMask(data, offset, values, count); got&want != want {
				t.Fatalf("root mask at %d = %#x omits scalar candidates %#x", offset, got, want)
			}
			if got, want := rootByteSingleTriggerMask(data, offset, values[0]), scalarRootByteTriggerMask(data, offset, values, 1); got&want != want {
				t.Fatalf("single root mask at %d = %#x omits scalar candidates %#x", offset, got, want)
			}
			minimum, maximum := first&0x7f, second&0x7f
			if minimum > maximum {
				minimum, maximum = maximum, minimum
			}
			var remainder [2]byte
			remainder[0], remainder[1] = values[0], values[1]
			if got, want := byteWordRangeAndValuesMask(data, offset, minimum, maximum, remainder, count%3), scalarRangeAndValuesMask(data, offset, minimum, maximum, remainder, count%3); got != want {
				t.Fatalf("range mask at %d = %#x, want %#x", offset, got, want)
			}
			if got, want := byteWordRangeAndSingleMask(data, offset, minimum, maximum, remainder[0]), scalarRangeAndValuesMask(data, offset, minimum, maximum, remainder, 1); got != want {
				t.Fatalf("single range mask at %d = %#x, want %#x", offset, got, want)
			}
		}
	})
}

func scalarRangeAndValuesMask(data []byte, offset int, minimum, maximum byte, values [2]byte, count byte) uint64 {
	var mask uint64
	for lane := 0; lane < 8; lane++ {
		value := data[offset+lane]
		if value >= minimum && value <= maximum || count != 0 && value == values[0] || count == 2 && value == values[1] {
			mask |= uint64(0x80) << (lane * 8)
		}
	}
	return mask
}

func scalarSingleByteTriggerMask(data []byte, offset int, first, second byte) uint64 {
	var mask uint64
	for lane := 0; lane < 8; lane++ {
		if value := data[offset+lane]; value == first || value == second {
			mask |= uint64(0x80) << (lane * 8)
		}
	}
	return mask
}

func scalarRootByteTriggerMask(data []byte, offset int, values [8]byte, count byte) uint64 {
	var mask uint64
	for lane := 0; lane < 8; lane++ {
		for index := byte(0); index < count; index++ {
			if data[offset+lane] == values[index] {
				mask |= uint64(0x80) << (lane * 8)
				break
			}
		}
	}
	return mask
}
