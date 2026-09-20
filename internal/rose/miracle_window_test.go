package rose

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// bruteForceStates 用逐偏移逐角色比较给出候选结果参考，作为窗口批量枚举的独立对照。
func bruteForceStates(roles []Role, data []byte) []State {
	var out []State
	for off := 0; off < len(data); off++ {
		for _, role := range roles {
			if off+len(role.Literal) > len(data) {
				continue
			}
			if literalMatches(role, data[off:off+len(role.Literal)]) {
				out = append(out, State{RoleID: role.ID, Offset: uint64(off)})
			}
		}
	}
	return out
}

// bruteForceStatesEndRange 按右开区间 [from, to) 过滤结束偏移后给出同一参考结果。
func bruteForceStatesEndRange(roles []Role, data []byte, from, to int) []State {
	var out []State
	for _, state := range bruteForceStates(roles, data) {
		end := int(state.Offset) + len(roles[state.RoleID-1].Literal)
		if end >= from && end < to {
			out = append(out, state)
		}
	}
	return out
}

// literalMatches 独立实现大小写敏感与 ASCII 大小写不敏感的字面量比较。
func literalMatches(role Role, window []byte) bool {
	if bytes.Equal(window, role.Literal) {
		return true
	}
	return role.CaseInsensitive && bytes.EqualFold(window, role.Literal)
}

// TestMiracleWindowCandidatesMatchBruteForce 校验多角色候选路径在 64 字节宽窗口边界
// 附近（含尾部截断）不丢命中：数据长度从 0 覆盖到 96，随机内容含大小写变体。
func TestMiracleWindowCandidatesMatchBruteForce(t *testing.T) {
	// WideWidthGuard 取宽窗口宽度，确保语料覆盖到第二个宽窗口之后。
	const WideWidthGuard = 64
	roles := []Role{
		{ID: 1, Literal: []byte("ab")},
		{ID: 2, Literal: []byte("CD"), CaseInsensitive: true},
		{ID: 3, Literal: []byte("efg")},
	}
	program := New(roles)
	if !program.miracleReady || program.matcher != nil {
		t.Fatalf("多角色纯文字程序未走候选路径: ready=%v matcher=%v", program.miracleReady, program.matcher != nil)
	}
	rng := rand.New(rand.NewSource(90210))
	alphabet := []byte("abABcCdDeEfFgGz")
	maxOffset := -1
	for length := 0; length <= 96; length++ {
		data := make([]byte, length)
		for i := range data {
			data[i] = alphabet[rng.Intn(len(alphabet))]
		}
		want := bruteForceStates(roles, data)
		got := program.FindMatches(data)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("长度=%d 数据=%q got=%v want=%v", length, data, got, want)
		}
		for _, state := range got {
			if int(state.Offset) > maxOffset {
				maxOffset = int(state.Offset)
			}
		}
	}
	// 窗口步进只有在命中落在第二个及之后的宽窗口时才会进入尾部或多次迭代路径，
	// 这里显式确认语料确实覆盖到 64 字节宽窗口之外，避免测试空转。
	if maxOffset < WideWidthGuard {
		t.Fatalf("测试语料未覆盖 64 字节宽窗口之外，最大命中偏移=%d", maxOffset)
	}
}

// TestMiracleWindowCandidatesRespectEndRange 校验候选结果与右开区间结束偏移语义一致。
func TestMiracleWindowCandidatesRespectEndRange(t *testing.T) {
	roles := []Role{
		{ID: 1, Literal: []byte("ab")},
		{ID: 2, Literal: []byte("cd"), CaseInsensitive: true},
	}
	program := New(roles)
	data := []byte("ab" + string(bytes.Repeat([]byte("z"), 30)) + "CD" + "cd" + "!")
	got := program.FindMatchesEndRange(data, 0, len(data))
	want := bruteForceStatesEndRange(roles, data, 0, len(data))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("区间候选 got=%v want=%v", got, want)
	}
	if len(want) < 3 {
		t.Fatalf("测试语料命中过少: %v", want)
	}
	limited := program.FindMatchesEndRangeLimit(data, 0, len(data), 2)
	if len(limited) != 2 || fmt.Sprint(limited) != fmt.Sprint(want[:2]) {
		t.Fatalf("区间限量候选=%v want=%v", limited, want[:2])
	}
}

// BenchmarkMiracleWindowCandidates 记录候选路径在 64KiB 输入上的吞吐与分配。
func BenchmarkMiracleWindowCandidates(b *testing.B) {
	program := New([]Role{
		{ID: 1, Literal: []byte("alpha")},
		{ID: 2, Literal: []byte("BETA"), CaseInsensitive: true},
		{ID: 3, Literal: []byte("gamma")},
	})
	data := make([]byte, 64<<10)
	for i := range data {
		data[i] = byte('a' + i%11)
	}
	copy(data[1024:], "alpha")
	copy(data[30<<10:], "beta")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := program.FindMatches(data); len(got) == 0 {
			b.Fatal("没有候选命中")
		}
	}
}

// TestMiracleWindowWideBoundaryCandidatesMatched 用确定性语料覆盖 64 字节宽窗口边界：
// 命中分别落在宽窗口起点（偏移 0/64/128/192）、窗口末端与尾部回退区间，任何一段的
// 候选枚举出现漏报或重复都会与暴力参考不一致。
func TestMiracleWindowWideBoundaryCandidatesMatched(t *testing.T) {
	roles := []Role{
		{ID: 1, Literal: []byte("abc")},
		{ID: 2, Literal: []byte("cd"), CaseInsensitive: true},
		{ID: 3, Literal: []byte("efg")},
	}
	program := New(roles)
	if !program.miracleReady {
		t.Fatal("多角色纯文字程序未走候选路径")
	}
	// 两组落点互不重叠，且刻意跨越 64 字节宽窗口边界。
	wideStarts := []int{0, 8, 30, 38, 60, 68, 126, 134, 190}
	shortStarts := []int{4, 34, 64, 130, 194}
	for _, length := range []int{63, 64, 65, 127, 128, 129, 191, 192, 193, 256, 257} {
		data := bytes.Repeat([]byte("q"), length)
		for _, start := range wideStarts {
			if start+3 <= length {
				copy(data[start:], "abc")
			}
		}
		for _, start := range shortStarts {
			if start+2 <= length {
				copy(data[start:], "cd")
			}
		}
		got := program.FindMatches(data)
		want := bruteForceStates(roles, data)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("长度=%d 候选不一致: got=%v want=%v", length, got, want)
		}
		if length < 128 {
			continue
		}
		// 长语料必须存在落在第二个及之后的宽窗口内的命中，避免测试空转。
		maxOffset := -1
		for _, state := range got {
			if int(state.Offset) > maxOffset {
				maxOffset = int(state.Offset)
			}
		}
		if maxOffset < 64 {
			t.Fatalf("长度=%d 语料未覆盖 64 字节宽窗口之外，最大命中偏移=%d", length, maxOffset)
		}
	}
}
