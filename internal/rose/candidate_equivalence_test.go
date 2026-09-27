package rose

import (
	"fmt"
	"math/rand"
	"testing"
)

// candidateRoleSets 覆盖多角色候选路径可能遇到的约束组合：普通文字、大小写折叠、
// 重复文字、锚定、末尾锚定、偏移上下界与需确认角色。
func candidateRoleSets() [][]Role {
	return [][]Role{
		{{ID: 1, Literal: []byte("ab")}, {ID: 2, Literal: []byte("CD"), CaseInsensitive: true}},
		{{ID: 1, Literal: []byte("alpha")}, {ID: 2, Literal: []byte("BETA"), CaseInsensitive: true}, {ID: 3, Literal: []byte("gamma")}},
		{{ID: 1, Literal: []byte("ab")}, {ID: 2, Literal: []byte("ab")}},
		{{ID: 1, Literal: []byte("abc"), Anchored: true}, {ID: 2, Literal: []byte("cd")}},
		{{ID: 1, Literal: []byte("abc"), EndOfData: true}, {ID: 2, Literal: []byte("cd")}},
		{{ID: 1, Literal: []byte("abc"), MinOffset: 2, HasMaxOffset: true, MaxOffset: 40}, {ID: 2, Literal: []byte("cd")}},
		{{ID: 1, Literal: []byte("abc"), Confirm: true}, {ID: 2, Literal: []byte("cd")}},
		{{ID: 1, Literal: []byte("AbC"), CaseInsensitive: true}, {ID: 2, Literal: []byte("cD"), CaseInsensitive: true}},
	}
}

// referencedStates 用独立实现给出多角色扫描的完整参考结果：逐偏移比较文字，
// 再显式套用锚定、末尾与偏移上下界约束，最后按偏移与角色编号排序。它刻意不复用
// Role.Eligible，避免参考实现与被测实现同源。
func referencedStates(roles []Role, data []byte) []State {
	var out []State
	for off := 0; off < len(data); off++ {
		for _, role := range roles {
			if off+len(role.Literal) > len(data) {
				continue
			}
			if !literalMatches(role, data[off:off+len(role.Literal)]) {
				continue
			}
			if role.Anchored && off != 0 {
				continue
			}
			end := off + len(role.Literal)
			if role.EndOfData && end != len(data) {
				continue
			}
			if uint64(end) < role.MinOffset {
				continue
			}
			if role.HasMaxOffset && uint64(end) > role.MaxOffset {
				continue
			}
			out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
		}
	}
	return SortStates(out)
}

// TestMultiRoleCandidatesMatchBruteForce 差分校验多角色候选路径与逐偏移暴力参考
// 在随机语料上给出完全一致的命中集合，覆盖各类角色约束与后端窗口边界。
func TestMultiRoleCandidatesMatchBruteForce(t *testing.T) {
	alphabets := []string{"abAB", "abABcCdD", "abcdefgXYZ01"}
	rng := rand.New(rand.NewSource(20260927))
	coveredWide := 0
	for setIndex, roles := range candidateRoleSets() {
		program := New(roles)
		if program.matcher == nil {
			t.Fatalf("角色集 %d 未构建共享自动机", setIndex)
		}
		for trial := range 2000 {
			alphabet := alphabets[rng.Intn(len(alphabets))]
			length := rng.Intn(200)
			data := make([]byte, length)
			for i := range data {
				data[i] = alphabet[rng.Intn(len(alphabet))]
			}
			want := referencedStates(roles, data)
			got := program.FindMatches(data)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("角色集 %d 迭代 %d 长度 %d 数据 %q: got=%v want=%v", setIndex, trial, length, data, got, want)
			}
			for _, state := range got {
				if int(state.Offset) >= 64 {
					coveredWide++
				}
			}
		}
	}
	// 显式确认语料确实覆盖到第二个及之后的 64 字节宽窗口，避免测试空转。
	if coveredWide == 0 {
		t.Fatal("语料未覆盖 64 字节宽窗口之外，候选路径未被真正执行")
	}
}

// TestNormalizePreservesSharedMatcher 校验归一化之后共享自动机仍然有效，
// 不会因重新推导而丢失候选路径。
func TestNormalizePreservesSharedMatcher(t *testing.T) {
	roles := []Role{
		{ID: 3, Literal: []byte("abc")},
		{ID: 1, Literal: []byte("cd"), CaseInsensitive: true},
		{ID: 2, Literal: []byte("efg")},
	}
	program := New(roles)
	program.Normalize()
	if program.matcher == nil {
		t.Fatal("归一化后共享自动机丢失")
	}
	for _, length := range []int{0, 1, 63, 64, 65, 127, 128, 129, 191, 192, 193, 255, 256, 257} {
		data := make([]byte, length)
		for i := range data {
			data[i] = byte("abcdefgz"[i%8])
		}
		copy(data, "abc")
		if length >= 132 {
			copy(data[65:], "cd")
			copy(data[130:], "efg")
		}
		got := program.FindMatches(data)
		want := bruteForceStates(roles, data)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("长度=%d 归一化后候选不一致: got=%v want=%v", length, got, want)
		}
	}
}
