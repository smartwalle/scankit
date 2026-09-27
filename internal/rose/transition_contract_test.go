package rose

import "testing"

func TestSchedulerCheckedActivationAndTransition(t *testing.T) {
	s := NewScheduler(New([]Role{{ID: 1, Literal: []byte("a")}}))
	if s.ActivateChecked(State{RoleID: 9}) {
		t.Fatal("未知角色不应激活")
	}
	state := State{RoleID: 1, Offset: 2}
	if !s.ActivateChecked(state) {
		t.Fatal("角色激活失败")
	}
	if !s.Transition(state, 4) || s.ActiveCount() != 1 {
		t.Fatalf("迁移失败 pending=%v", s.Pending())
	}
	if s.Transition(state, 5) {
		t.Fatal("旧状态不应再次迁移")
	}
}
