package rose

import (
	"fmt"
	"math/rand"
	"testing"
)

// miracleEquivalenceRoleSets 覆盖候选器可能遇到的约束组合：普通文字、大小写折叠、
// 重复文字、锚定、末尾锚定、偏移上下界与需确认角色。
func miracleEquivalenceRoleSets() [][]Role {
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

// matcherEngineStates 用通用多模式匹配器给出与候选路径对照的命中最集。
func matcherEngineStates(p *Program, data []byte, from, to int) []State {
	matcher := buildRoleMatcher(p.Roles)
	if matcher == nil {
		return nil
	}
	var out []State
	for _, match := range matcher.FindRange(data, from, to) {
		role, ok := p.findRole(match.ID)
		if !ok || !role.Eligible(data, match.From) {
			continue
		}
		out = append(out, State{RoleID: match.ID, Offset: uint64(match.From)})
	}
	return SortStates(dedupStates(out))
}

// TestMiracleCandidatesMatchMatcherEngine 差分校验候选器与通用多模式匹配器在随机
// 语料上给出完全一致的命中集合，覆盖宽窗口步进、尾部回退与各类角色约束。
func TestMiracleCandidatesMatchMatcherEngine(t *testing.T) {
	alphabets := []string{"abAB", "abABcCdD", "abcdefgXYZ01"}
	rng := rand.New(rand.NewSource(20260927))
	coveredWide := 0
	for setIndex, roles := range miracleEquivalenceRoleSets() {
		program := New(roles)
		if !program.miracleReady || program.matcher != nil {
			t.Fatalf("角色集 %d 未走候选路径: ready=%v matcher=%v", setIndex, program.miracleReady, program.matcher != nil)
		}
		for trial := range 2000 {
			alphabet := alphabets[rng.Intn(len(alphabets))]
			length := rng.Intn(200)
			data := make([]byte, length)
			for i := range data {
				data[i] = alphabet[rng.Intn(len(alphabet))]
			}
			want := matcherEngineStates(program, data, 0, len(data))
			got := program.findMiracleMulti(data, 0, len(data), 0)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("角色集 %d 迭代 %d 长度 %d 数据 %q: got=%v want=%v", setIndex, trial, length, data, got, want)
			}
			// 公共入口必须与候选器保持一致，避免构建期“跳过匹配器”的决定被还原。
			if fmt.Sprint(program.FindMatches(data)) != fmt.Sprint(want) {
				t.Fatalf("角色集 %d 迭代 %d 长度 %d: FindMatches 未走候选路径", setIndex, trial, length)
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

// TestNormalizePreservesMiracleCandidatePath 校验归一化之后仍然保留构建期
// “全部角色可由候选器定位”的判断，不会重新构建通用匹配器。
func TestNormalizePreservesMiracleCandidatePath(t *testing.T) {
	roles := []Role{
		{ID: 3, Literal: []byte("abc")},
		{ID: 1, Literal: []byte("cd"), CaseInsensitive: true},
		{ID: 2, Literal: []byte("efg")},
	}
	program := New(roles)
	program.Normalize()
	if !program.miracleReady {
		t.Fatal("归一化后候选器未就绪")
	}
	if program.matcher != nil {
		t.Fatal("归一化重新构建了通用匹配器，候选路径失效")
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
