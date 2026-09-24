package scankit

import "testing"

func TestMatchEditAtomsRetainsDeletionDistanceWhenInputIsShort(t *testing.T) {
	atoms := []fuzzyAtom{{literal: bytePtr('a')}, {literal: bytePtr('b')}, {literal: bytePtr('c')}}
	ends, ok := matchEditAtoms(atoms, nil, 0, 3, 0)
	if !ok || len(ends) != 1 || ends[0] != 0 {
		t.Fatalf("短输入删除距离结果=%v, %v", ends, ok)
	}
}

func bytePtr(v byte) *byte { return new(v) }
