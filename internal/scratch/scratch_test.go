package scratch

import "testing"

func TestScratchPartitions(t *testing.T) {
	s := New()
	if len(s.ReserveBytes(4)) != 4 || len(s.ReserveStates(2)) != 2 {
		t.Fatal()
	}
	s.Report = append(s.Report, 1)
	s.Reset()
	if len(s.Bytes) != 0 || len(s.Report) != 0 {
		t.Fatal()
	}
}

func TestScratchReserveAll(t *testing.T) {
	s := New()
	if !s.ReserveAll(2, 3, 4, 5) || s.TotalCapacity() < 2+3*4+4*4+5 {
		t.Fatal("分区容量准备失败")
	}
	if s.ReserveAll(-1, 0, 0, 0) {
		t.Fatal("负容量未拒绝")
	}
}

func TestScratchBudgetAndZero(t *testing.T) {
	s := New()
	if !s.ReserveAllWithin(2, 3, 4, 5, 35) {
		t.Fatal("预算内分配失败")
	}
	if s.ReserveAllWithin(2, 3, 4, 5, 34) {
		t.Fatal("超预算分配被接受")
	}
	s.Bytes[0] = 7
	s.Temp = append(s.Temp, 9)
	s.Zero()
	b, st, r, tp := s.CapacityBytes()
	if s.TotalCount() != 0 || b+st+r+tp == 0 {
		t.Fatal("清零失败")
	}
}

func TestScratchValidateAndGlobalLimit(t *testing.T) {
	s := New()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.ReserveAllWithin(64<<20, 1, 0, 0, 0) {
		t.Fatal("超过工作区总上限的申请未拒绝")
	}
}
func FuzzScratch(f *testing.F) {
	f.Add(4)
	f.Fuzz(func(t *testing.T, n int) {
		if n < 0 || n > 1000 {
			return
		}
		s := New()
		if len(s.ReserveBytes(n)) != n {
			t.Fatal()
		}
		s.Reset()
	})
}
func TestPool(t *testing.T) {
	p := NewPool()
	s := p.Get()
	s.Bytes = append(s.Bytes, 1)
	p.Put(s)
	if len(p.Get().Bytes) != 0 {
		t.Fatal()
	}
}
