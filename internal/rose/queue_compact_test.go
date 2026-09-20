package rose

import (
	"math/rand"
	"reflect"
	"testing"
)

func sampleStates() []State {
	rng := rand.New(rand.NewSource(20240920))
	out := make([]State, 0, 512)
	for i := 0; i < 512; i++ {
		out = append(out, State{
			RoleID:   uint32(rng.Intn(7) + 1),
			Offset:   uint64(rng.Intn(64)),
			Priority: uint32(rng.Intn(3)),
		})
	}
	return out
}

// TestQueuePushAllMatchesSequentialPush 验证批量加入与逐个加入得到完全相同的队列。
func TestQueuePushAllMatchesSequentialPush(t *testing.T) {
	states := sampleStates()
	var sequential Queue
	for _, state := range states {
		sequential.Push(state)
	}
	var bulk Queue
	bulk.PushAll(states)
	if !reflect.DeepEqual(sequential.States(), bulk.States()) {
		t.Fatalf("批量入队与逐个入队结果不一致:\nsequential=%v\nbulk=%v", sequential.States(), bulk.States())
	}
}

// TestQueuePushAllAppendsToExistingQueue 验证批量加入会在既有队列上继续归并排序。
func TestQueuePushAllAppendsToExistingQueue(t *testing.T) {
	var q Queue
	q.Push(State{RoleID: 1, Offset: 5})
	q.PushAll([]State{{RoleID: 2, Offset: 1}, {RoleID: 1, Offset: 3}})
	want := []State{{RoleID: 2, Offset: 1}, {RoleID: 1, Offset: 3}, {RoleID: 1, Offset: 5}}
	if got := q.States(); !reflect.DeepEqual(got, want) {
		t.Fatalf("队列顺序错误: got=%v want=%v", got, want)
	}
}

// TestQueueCompactFiltersInvalidAndKeepsLowestPriority 验证压缩会丢弃非法状态，
// 并在同一角色/偏移上保留优先级数值最小的状态。
func TestQueueCompactFiltersInvalidAndKeepsLowestPriority(t *testing.T) {
	var q Queue
	q.items = []State{
		{RoleID: 1, Offset: 2, Priority: 7},
		{RoleID: 1, Offset: 2, Priority: 1},
		{RoleID: 0, Offset: 9},
		{RoleID: 2, Offset: 1, Priority: 5},
	}
	removed := q.Compact()
	if removed != 2 {
		t.Fatalf("应移除两个状态: %d", removed)
	}
	want := []State{{RoleID: 1, Offset: 2, Priority: 1}, {RoleID: 2, Offset: 1, Priority: 5}}
	if got := q.States(); !reflect.DeepEqual(got, want) {
		t.Fatalf("压缩结果错误: got=%v want=%v", got, want)
	}
	for i := q.Len(); i < cap(q.items); i++ {
		if q.items[:cap(q.items)][i] != (State{}) {
			t.Fatal("压缩后尾部槽位未清零")
		}
	}
}

// TestSchedulerBulkActivationMatchesSequentialActivation 验证批量激活与
// 逐个激活在队列内容、激活计数和优先级替换上完全一致。
func TestSchedulerBulkActivationMatchesSequentialActivation(t *testing.T) {
	p := New([]Role{
		{ID: 1, ReportID: 1, Literal: []byte("ab")},
		{ID: 2, ReportID: 2, Literal: []byte("b")},
		{ID: 3, ReportID: 3, Literal: []byte("abc")},
	})
	data := []byte("ababc ab b")
	matches := p.FindMatches(data)
	if len(matches) == 0 {
		t.Fatal("测试数据未产生角色命中")
	}
	sequential := NewScheduler(p)
	for _, state := range matches {
		sequential.Activate(state)
	}
	bulk := NewScheduler(p)
	if got := bulk.ActivateMatches(data); got != len(matches) {
		t.Fatalf("激活计数不一致: got=%d want=%d", got, len(matches))
	}
	if !reflect.DeepEqual(sequential.Pending(), bulk.Pending()) {
		t.Fatalf("批量激活待执行队列不一致:\nsequential=%v\nbulk=%v", sequential.Pending(), bulk.Pending())
	}
	if sequential.ActiveCount() != bulk.ActiveCount() {
		t.Fatalf("激活计数不一致: sequential=%d bulk=%d", sequential.ActiveCount(), bulk.ActiveCount())
	}
}

// TestSchedulerBulkActivationReplacesQueuedPriority 验证批量激活时，
// 队列内已存在状态会被同一批中优先级更低的状态替换且不产生重复。
func TestSchedulerBulkActivationReplacesQueuedPriority(t *testing.T) {
	s := NewScheduler(New(nil))
	s.Activate(State{RoleID: 1, Offset: 4, Priority: 9})
	s.activateBatch([]State{
		{RoleID: 1, Offset: 4, Priority: 2},
		{RoleID: 1, Offset: 4, Priority: 6},
		{RoleID: 2, Offset: 4, Priority: 3},
	})
	pending := s.Pending()
	want := []State{{RoleID: 1, Offset: 4, Priority: 2}, {RoleID: 2, Offset: 4, Priority: 3}}
	if !reflect.DeepEqual(pending, want) {
		t.Fatalf("批量激活替换结果错误: got=%v want=%v", pending, want)
	}
	if s.ActiveCount() != 2 {
		t.Fatalf("激活计数错误: %d", s.ActiveCount())
	}
}

// TestSchedulerBulkActivationHonorsMaxPending 验证批量激活仍受 maxPending 截断约束。
func TestSchedulerBulkActivationHonorsMaxPending(t *testing.T) {
	s := NewScheduler(New(nil))
	s.SetMaxPending(2)
	s.activateBatch([]State{
		{RoleID: 1, Offset: 3},
		{RoleID: 2, Offset: 1},
		{RoleID: 3, Offset: 2},
	})
	pending := s.Pending()
	if len(pending) != 2 || pending[0].Offset != 1 || pending[1].Offset != 2 {
		t.Fatalf("maxPending 截断错误: %v", pending)
	}
	if s.ActiveCount() != 2 {
		t.Fatalf("active 表未同步截断: %d", s.ActiveCount())
	}
}

// TestSchedulerBulkActivationKeepsExistingLowerPriority 验证既有状态优先级更低时
// 批量激活不会替换它，也不会产生重复队列项。
func TestSchedulerBulkActivationKeepsExistingLowerPriority(t *testing.T) {
	s := NewScheduler(New(nil))
	s.Activate(State{RoleID: 1, Offset: 4, Priority: 1})
	s.activateBatch([]State{{RoleID: 1, Offset: 4, Priority: 5}})
	pending := s.Pending()
	if len(pending) != 1 || pending[0].Priority != 1 {
		t.Fatalf("既有低优先级状态被覆盖: %v", pending)
	}
}

// BenchmarkQueuePushAll 对比批量入队与逐个入队的最坏顺序开销：
// 输入按偏移逆序给出，逐个 Push 的插入排序退化为 O(n²)。
func BenchmarkQueuePushAll(b *testing.B) {
	states := make([]State, 0, 4096)
	for i := 0; i < 4096; i++ {
		states = append(states, State{RoleID: 1, Offset: uint64(4096 - i)})
	}
	b.Run("bulk", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var q Queue
			q.PushAll(states)
		}
	})
	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var q Queue
			for _, state := range states {
				q.Push(state)
			}
		}
	})
}

// BenchmarkSchedulerActivateMatches 对比批量激活与逐个激活的扫描开销。
func BenchmarkSchedulerActivateMatches(b *testing.B) {
	roles := make([]Role, 0, 64)
	for i := 0; i < 64; i++ {
		roles = append(roles, Role{ID: uint32(i + 1), ReportID: uint32(i + 1), Literal: []byte("abcdefgh")})
	}
	p := New(roles)
	data := make([]byte, 4096)
	for i := range data {
		copy(data[i:], "abcdefgh")
		i += 7
	}
	b.Run("bulk", func(b *testing.B) {
		s := NewScheduler(p)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.ActivateMatches(data)
			s.Reset()
		}
	})
	b.Run("sequential", func(b *testing.B) {
		s := NewScheduler(p)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, state := range p.FindMatches(data) {
				s.Activate(state)
			}
			s.Reset()
		}
	})
}
