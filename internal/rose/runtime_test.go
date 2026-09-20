package rose

import (
	"github.com/smartwalle/scankit/internal/report"
	"testing"
)

func TestSchedulerOrderingAndSingleMatch(t *testing.T) {
	s := NewScheduler(New(nil))
	s.Activate(State{RoleID: 1, Offset: 2})
	s.Activate(State{RoleID: 1, Offset: 1})
	events := s.Run(func(st State) []report.Event {
		return []report.Event{{ID: st.RoleID, From: st.Offset, To: st.Offset + 1, Flags: report.FlagSingleMatch}}
	})
	if len(events) != 1 || events[0].From != 1 {
		t.Fatal(events)
	}
}

func TestProgramEndRangeReturnsStableStateOrder(t *testing.T) {
	p := New([]Role{{ID: 2, Literal: []byte("a")}, {ID: 1, Literal: []byte("a")}})
	got := p.FindMatchesEndRange([]byte("aa"), 1, 2)
	if len(got) != 2 || got[0].RoleID != 1 || got[1].RoleID != 2 || got[0].Offset != 0 {
		t.Fatalf("结束区间状态顺序不稳定: %#v", got)
	}
}

func TestProgramEndRangeLimitPreservesOrder(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a")}, {ID: 2, Literal: []byte("a")}})
	got := p.FindMatchesEndRangeLimit([]byte("aa"), 0, 2, 1)
	if len(got) != 1 || got[0].RoleID != 1 || got[0].Offset != 0 {
		t.Fatalf("结束区间限量结果=%v", got)
	}
	if got := p.FindMatchesEndRangeLimit([]byte("aa"), 0, 2, -1); got != nil {
		t.Fatalf("负限量未拒绝=%v", got)
	}
}

func TestProgramEndRangeMatchesFullScanFilter(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("ab")}, {ID: 2, Literal: []byte("b")}, {ID: 3, Literal: []byte("ab")}})
	data := []byte("zabab")
	for from := 0; from <= len(data); from++ {
		for to := from; to <= len(data); to++ {
			want := make([]State, 0)
			for _, st := range p.FindMatches(data) {
				r, _ := p.FindRole(st.RoleID)
				end := int(st.Offset) + len(r.Literal)
				if end >= from && end < to {
					want = append(want, st)
				}
			}
			got := p.FindMatchesEndRange(data, from, to)
			if len(got) != len(want) {
				t.Fatalf("区间[%d,%d) 数量=%v want=%v", from, to, got, want)
			}
		}
	}
}

func TestFindMatchesLimitDeduplicatesFallbackRoles(t *testing.T) {
	p := New([]Role{
		{ID: 1, Literal: []byte("é"), CaseInsensitive: true},
		{ID: 1, Literal: []byte("é"), CaseInsensitive: true},
	})
	got := p.FindMatchesLimit([]byte("é"), 2)
	if len(got) != 1 || got[0].RoleID != 1 || got[0].Offset != 0 {
		t.Fatalf("回退角色去重错误=%v", got)
	}
}

func TestSchedulerOrdersEqualPriorityByOffsetAndRole(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}, {ID: 2, Literal: []byte("b")}}))
	s.Activate(State{RoleID: 2, Offset: 4})
	s.Activate(State{RoleID: 1, Offset: 2})
	s.Activate(State{RoleID: 2, Offset: 2})
	var got []State
	s.Run(func(st State) []report.Event {
		got = append(got, st)
		return nil
	})
	if len(got) != 3 || got[0].Offset != 2 || got[0].RoleID != 1 || got[1].RoleID != 2 || got[2].Offset != 4 {
		t.Fatalf("队列顺序=%v", got)
	}
}

func TestSchedulerDeduplicatesPendingState(t *testing.T) {
	scheduler := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}}))
	state := State{RoleID: 1, Offset: 2, Priority: 3}
	scheduler.Activate(state)
	scheduler.Activate(state)
	if scheduler.QueueLen() != 1 {
		t.Fatalf("duplicate state remained queued: %v", scheduler.Pending())
	}
	events := scheduler.Run(func(got State) []report.Event {
		return []report.Event{{ID: got.RoleID, From: got.Offset, To: got.Offset + 1}}
	})
	if len(events) != 1 || scheduler.QueueLen() != 0 {
		t.Fatalf("scheduler result=%v pending=%v", events, scheduler.Pending())
	}
	scheduler.Activate(state)
	if scheduler.QueueLen() != 1 {
		t.Fatal("completed state could not be activated again")
	}
}

func TestSchedulerKeepsHighestPriorityForSameRoleAndOffset(t *testing.T) {
	scheduler := NewScheduler(New(nil))
	scheduler.Activate(State{RoleID: 1, Offset: 2, Priority: 9})
	scheduler.Activate(State{RoleID: 1, Offset: 2, Priority: 1})
	pending := scheduler.Pending()
	if len(pending) != 1 || pending[0].Priority != 1 {
		t.Fatalf("priority replacement failed: %#v", pending)
	}
}

func TestSchedulerCatchUpAndRunReports(t *testing.T) {
	scheduler := NewScheduler(New([]Role{{ID: 3, ReportID: 9, Literal: []byte("abc")}}))
	data := []byte("abc--abc")
	if events := scheduler.CatchUp(data, 0, 3, func(state State) []report.Event {
		return []report.Event{{ID: state.RoleID, From: state.Offset, To: state.Offset + 3}}
	}); len(events) != 1 || events[0].From != 0 {
		t.Fatalf("catchup reports: %#v", events)
	}
	scheduler.Reset()
	if events := scheduler.RunReports(data); len(events) != 2 || events[0].ID != 9 || events[1].From != 5 {
		t.Fatalf("automatic reports: %#v", events)
	}
}

func TestSchedulerUsesRoleIDWhenReportIDIsOmitted(t *testing.T) {
	scheduler := NewScheduler(New([]Role{{ID: 4, Literal: []byte("x")}}))
	events := scheduler.RunReports([]byte("x"))
	if len(events) != 1 || events[0].ID != 4 {
		t.Fatalf("default report id: %#v", events)
	}
}

func TestSchedulerProgramInstructions(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a"), ReportID: 7}, {ID: 2, Literal: []byte("b"), ReportID: 8}})
	if err := p.SetInstructions([]Instruction{{Kind: InstructionReport, RoleID: 1, ReportID: 9, IncludeSOM: true}, {Kind: InstructionActivate, RoleID: 1, TargetID: 2, OffsetDelta: 1}}); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(p)
	s.Activate(State{RoleID: 1, Offset: 3})
	if got := s.RunProgram(false); len(got) != 2 || got[0].ID != 9 || got[0].SOM != 3 || got[1].ID != 8 {
		t.Fatalf("指令报告=%v", got)
	}
	if s.QueueLen() != 0 {
		t.Fatalf("指令激活未执行: %v", s.Pending())
	}
}

func TestSchedulerReportsOptionalSOM(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}}))
	events := s.RunReportsWithSOM([]byte("xa"), true)
	if len(events) != 1 || events[0].SOM != 1 {
		t.Fatalf("SOM 报告=%v", events)
	}
}

func TestSchedulerRunsAnchoredAndEndOfDataRoles(t *testing.T) {
	scheduler := NewScheduler(New([]Role{
		{ID: 1, Literal: []byte("a"), Anchored: true},
		{ID: 2, Literal: []byte("a"), EndOfData: true},
	}))
	events := scheduler.RunReports([]byte("aba"))
	if len(events) != 2 || events[0] != (report.Event{ID: 1, From: 0, To: 1}) || events[1] != (report.Event{ID: 2, From: 2, To: 3}) {
		t.Fatalf("anchored/EOD reports=%#v", events)
	}
}

func TestSchedulerRunLimitRejectsNegative(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}}))
	if got := s.RunLimit(nil, -1); got != nil {
		t.Fatalf("负报告限制=%v", got)
	}
}

func TestSchedulerRunStepLimitPreservesUnexecutedState(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}, {ID: 2, Literal: []byte("b")}}))
	s.SetMaxSteps(1)
	s.Activate(State{RoleID: 1, Offset: 0})
	s.Activate(State{RoleID: 2, Offset: 1})
	s.Run(func(st State) []report.Event { return nil })
	if s.QueueLen() != 1 || s.ActiveCount() != 1 {
		t.Fatalf("步数限制丢失未执行状态: queue=%d active=%d", s.QueueLen(), s.ActiveCount())
	}
}

func TestQueuePopCompactsStorage(t *testing.T) {
	var q Queue
	q.Push(State{RoleID: 1})
	q.Push(State{RoleID: 2})
	capBefore := q.Cap()
	if _, ok := q.Pop(); !ok {
		t.Fatal("队列弹出失败")
	}
	peek, ok := q.Peek()
	if !ok || q.Len() != 1 || peek.RoleID != 2 || q.Cap() != capBefore {
		t.Fatalf("队列状态异常: len=%d cap=%d", q.Len(), q.Cap())
	}
}

func TestActivateCheckedAcceptsPriorityReplacement(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}}))
	if !s.ActivateChecked(State{RoleID: 1, Offset: 2, Priority: 5}) {
		t.Fatal("首次激活失败")
	}
	if !s.ActivateChecked(State{RoleID: 1, Offset: 2, Priority: 1}) {
		t.Fatal("优先级替换未被识别")
	}
	if s.ActivateChecked(State{RoleID: 9, Offset: 2}) {
		t.Fatal("未声明角色被激活")
	}
}

func TestSchedulerSetMaxPendingRemovesDroppedActiveStates(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}, {ID: 2, Literal: []byte("b")}, {ID: 3, Literal: []byte("c")}}))
	s.Activate(State{RoleID: 1, Offset: 1})
	s.Activate(State{RoleID: 2, Offset: 2})
	s.Activate(State{RoleID: 3, Offset: 3})
	s.SetMaxPending(1)
	if s.QueueLen() != 1 || s.ActiveCount() != 1 {
		t.Fatalf("待执行上限未同步清理: queue=%v active=%d", s.Pending(), s.ActiveCount())
	}
}

func TestSchedulerMaxPendingAppliesToLaterActivations(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}, {ID: 2, Literal: []byte("b")}}))
	s.SetMaxPending(1)
	s.Activate(State{RoleID: 1, Offset: 4})
	s.Activate(State{RoleID: 2, Offset: 1})
	if s.QueueLen() != 1 || s.Pending()[0].RoleID != 2 || s.ActiveCount() != 1 {
		t.Fatalf("动态待执行上限未生效: queue=%v active=%d", s.Pending(), s.ActiveCount())
	}
}

func TestProgramInstructionIndexTracksUpdates(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a"), ReportID: 1}})
	if err := p.SetInstructions([]Instruction{{Kind: InstructionReport, RoleID: 1, ReportID: 7}}); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(p)
	s.Activate(State{RoleID: 1, Offset: 0})
	events := s.RunProgram(false)
	if len(events) != 1 || events[0].ID != 7 {
		t.Fatalf("指令索引未更新: %#v", events)
	}
}

func TestSchedulerStopsSelfActivationCycle(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a")}})
	if err := p.SetInstructions([]Instruction{{Kind: InstructionActivate, RoleID: 1, TargetID: 1}}); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(p)
	s.Activate(State{RoleID: 1, Offset: 0})
	if got := s.RunProgram(false); len(got) != 0 || s.QueueLen() != 0 || s.ActiveCount() != 0 {
		t.Fatalf("自激活循环未收敛: events=%v pending=%d active=%d", got, s.QueueLen(), s.ActiveCount())
	}
}

func TestSchedulerBoundsOffsetGrowingInstructionCycle(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a")}})
	if err := p.SetInstructions([]Instruction{{Kind: InstructionActivate, RoleID: 1, TargetID: 1, OffsetDelta: 1}}); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(p)
	s.SetMaxSteps(7)
	s.Activate(State{RoleID: 1})
	count, _ := s.RunCount(func(state State) []report.Event {
		s.ActivateChecked(State{RoleID: 1, Offset: state.Offset + 1})
		return nil
	})
	if count != 7 || s.QueueLen() == 0 {
		t.Fatalf("偏移递增环未按预算停止: count=%d pending=%d", count, s.QueueLen())
	}
	s.Reset()
	s.Activate(State{RoleID: 1})
	if count, _ := s.RunCount(func(state State) []report.Event {
		s.ActivateChecked(State{RoleID: 1, Offset: state.Offset + 1})
		return nil
	}); count != 7 {
		t.Fatalf("重置后预算行为改变: %d", count)
	}
}

func TestActivateStateAtReportsPriorityReplacement(t *testing.T) {
	p := New([]Role{{ID: 1, Literal: []byte("a"), ReportID: 1}})
	s := NewScheduler(p)
	if !s.ActivateStateAt(1, 4, 10) || s.ActivateStateAt(1, 4, 20) {
		t.Fatal("低优先级状态替换语义错误")
	}
	if !s.ActivateStateAt(1, 4, 1) {
		t.Fatal("高优先级状态替换未报告")
	}
	state, ok := s.Queue.Peek()
	if !ok || state.Priority != 1 {
		t.Fatalf("队列状态=%#v", state)
	}
}

func FuzzQueue(f *testing.F) {
	f.Add(uint32(1), uint64(2))
	f.Fuzz(func(t *testing.T, id uint32, off uint64) {
		var q Queue
		q.Push(State{id, off, 0})
		if q.Len() != 1 {
			t.Fatal()
		}
		_, ok := q.Pop()
		if !ok {
			t.Fatal()
		}
	})
}
