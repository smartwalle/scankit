package repeat

import "testing"

func TestProgramMatchesBoundedAndUnboundedRepeats(t *testing.T) {
	p, err := New([]byte("ab"), 1, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	ends := p.MatchAt([]byte("ababab"), 0)
	if len(ends) != 3 || ends[0] != 2 || ends[2] != 6 {
		t.Fatalf("bounded ends: %v", ends)
	}
	if end, ok := p.MatchFirst([]byte("ababab"), 0); !ok || end != 6 {
		t.Fatalf("greedy end: %d %v", end, ok)
	}
	q, err := New([]byte("a"), 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	if end, ok := q.MatchFirst([]byte("aaa"), 0); !ok || end != 0 {
		t.Fatalf("non-greedy end: %d %v", end, ok)
	}
}

func TestRepeatFindRangeLimitRejectsNilAndInvalidRange(t *testing.T) {
	var p *Program
	if got := p.FindRangeLimit([]byte("a"), 0, 1, 1); got != nil {
		t.Fatalf("空程序结果=%v", got)
	}
	p, err := New([]byte("a"), 1, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.FindRangeLimit([]byte("a"), 2, 1, 1); got != nil {
		t.Fatalf("非法区间结果=%v", got)
	}
}

func TestProgramRoundTrip(t *testing.T) {
	p, err := New([]byte("x"), 2, -1, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.Dump()
	if err != nil {
		t.Fatal(err)
	}
	q, err := Load(raw)
	if err != nil || q == p || len(q.Find([]byte("xxx"))) == 0 {
		t.Fatalf("round trip: %#v %v", q, err)
	}
}

func TestRepeatLimits(t *testing.T) {
	p, err := New([]byte("a"), 0, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.MatchAtLimit([]byte("aaa"), 0, 2); len(got) != 2 {
		t.Fatalf("结束偏移=%v", got)
	}
	if got := p.FindLimit([]byte("aaa"), 1); len(got) != 1 {
		t.Fatalf("区间=%v", got)
	}
	if p.MatchAtLimit([]byte("a"), 0, -1) != nil {
		t.Fatal("负预算应返回空")
	}
}

func TestRepeatBudgetStopsBySteps(t *testing.T) {
	p, err := New([]byte("a"), 0, -1, true)
	if err != nil {
		t.Fatal(err)
	}
	ends, steps, stopped := p.MatchAtBudget([]byte("aaaa"), 0, 2, 0)
	if !stopped || ends != nil || steps <= 2 {
		t.Fatalf("预算结果=%v steps=%d stopped=%v", ends, steps, stopped)
	}
	ends, _, stopped = p.MatchAtBudget([]byte("aaaa"), 0, 10, 2)
	if !stopped || len(ends) != 2 {
		t.Fatalf("结果预算=%v stopped=%v", ends, stopped)
	}
}

func TestRepeatRangeAndReverseQueries(t *testing.T) {
	p, _ := New([]byte("a"), 1, 3, true)
	if got := p.MatchAtRange([]byte("aaa"), 0, 2, 3); len(got) != 2 {
		t.Fatalf("起点区间=%v", got)
	}
	if got := p.MatchAtRangeLimit([]byte("aaa"), 0, 0, 3, 1); len(got) != 1 {
		t.Fatalf("起点区间限制=%v", got)
	}
	if got := p.FindEndRangeLimit([]byte("aaa"), 1, 3, 2); len(got) != 2 {
		t.Fatalf("终点区间限制=%v", got)
	}
	if p.CountRangeLimit([]byte("aaa"), 0, 3, 2) != 2 {
		t.Fatal("统计限制错误")
	}
	if !p.Contains([]byte("xaaa")) {
		t.Fatal("应检测到命中")
	}
	if len(p.FindReverseLimit([]byte("aaa"), 1)) != 1 {
		t.Fatal("逆序限制错误")
	}
}

func TestRepeatMetadataQueries(t *testing.T) {
	p, _ := New([]byte("xy"), 0, -1, false)
	if p.Width() != 2 || !p.CanMatchEmpty() {
		t.Fatal("重复元数据错误")
	}
	minCount, maxCount := p.Bounds()
	if minCount != 0 || maxCount != -1 {
		t.Fatalf("边界=%d,%d", minCount, maxCount)
	}
}

func TestRepeatFindBudget(t *testing.T) {
	p, _ := New([]byte("a"), 1, 2, true)
	hits, steps, stopped := p.FindBudget([]byte("aaa"), 2, 0)
	if !stopped || hits != nil || steps <= 2 {
		t.Fatalf("步骤预算=%v,%d,%v", hits, steps, stopped)
	}
	hits, _, stopped = p.FindBudget([]byte("aaa"), 100, 2)
	if !stopped || len(hits) != 2 {
		t.Fatalf("结果预算=%v,%v", hits, stopped)
	}
}

func TestRepeatFirstBudgetHonorsGreediness(t *testing.T) {
	p, _ := New([]byte("a"), 1, 3, true)
	if end, ok, stopped := p.MatchFirstBudget([]byte("aaa"), 0, 10); !ok || stopped || end != 3 {
		t.Fatalf("贪婪首结果=%d,%v,%v", end, ok, stopped)
	}
	p, _ = New([]byte("a"), 1, 3, false)
	if end, ok, _ := p.MatchFirstBudget([]byte("aaa"), 0, 10); !ok || end != 1 {
		t.Fatalf("非贪婪首结果=%d,%v", end, ok)
	}
}

func TestRepeatByteWidthAndAcceptCount(t *testing.T) {
	p, _ := New([]byte("ab"), 1, 3, true)
	if p.MinBytes() != 2 || p.MaxBytes() != 6 || p.AcceptCount([]byte("ababab"), 0) != 3 {
		t.Fatal("重复字节元数据错误")
	}
	if _, err := New(make([]byte, 1<<20+1), 1, 1, true); err == nil {
		t.Fatal("超长重复单元未拒绝")
	}
}

func TestRepeatFindFromAndTo(t *testing.T) {
	p, _ := New([]byte("a"), 1, 1, true)
	if got := p.FindFrom([]byte("aba"), 2); len(got) != 1 || got[0] != [2]int{2, 3} {
		t.Fatalf("起点过滤=%v", got)
	}
	if got := p.FindTo([]byte("aba"), 1); len(got) != 1 || got[0] != [2]int{0, 1} {
		t.Fatalf("终点过滤=%v", got)
	}
}

func FuzzRepeatQueriesAreBounded(f *testing.F) {
	f.Add([]byte("aaaa"), 1, 3)
	f.Add([]byte(""), 0, -1)
	f.Fuzz(func(t *testing.T, data []byte, minCount, maxCount int) {
		if minCount < 0 || minCount > 8 || maxCount < -1 || maxCount > 8 || maxCount >= 0 && maxCount < minCount {
			return
		}
		p, err := New([]byte("a"), minCount, maxCount, true)
		if err != nil {
			return
		}
		for _, hit := range p.FindRangeLimit(data, 0, len(data), 4) {
			if hit[0] < 0 || hit[1] < hit[0] || hit[1] > len(data) {
				t.Fatalf("非法区间=%v", hit)
			}
		}
	})
}
