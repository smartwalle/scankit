package teddy

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/hwlm"
	"github.com/smartwalle/scankit/internal/simd"
)

// TestWindowMaskAppliesSecondByte 验证第二字节掩码过滤掉首字节相同但后继
// 字节不匹配的位置，并在窗口末尾退回单字节判定。
func TestWindowMaskAppliesSecondByte(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("ab")}, {ID: 2, Value: []byte("cd")}})
	if m.Lanes() != 2 {
		t.Fatalf("最短文字长度为 2 时应启用 2 个 lane: %d", m.Lanes())
	}
	data := make([]byte, simd.SuperWidth)
	data[0], data[1] = 'a', 'b' // 命中
	data[4], data[5] = 'a', 'z' // 首字节命中、后继不命中
	data[6], data[7] = 'c', 'd' // 命中
	data[30], data[31] = 'a', 'z'
	data[31] = 'a' // 窗口末尾位置缺少后继字节，保留首字节判定
	mask, ok := windowMask(simd.GenericBackend{}, m, data, 0)
	if !ok {
		t.Fatal("窗口掩码计算失败")
	}
	for _, position := range []int{0, 6, 31} {
		if mask&(1<<uint(position)) == 0 {
			t.Fatalf("位置 %d 应保留为候选: %032b", position, mask)
		}
	}
	for _, position := range []int{4, 30} {
		if mask&(1<<uint(position)) != 0 {
			t.Fatalf("位置 %d 应被第二字节掩码过滤: %032b", position, mask)
		}
	}
}

// TestLaneMaskLimitedByShortestLiteral 验证 lane 数受最短文字长度约束。
func TestLaneMaskLimitedByShortestLiteral(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("a")}, {ID: 2, Value: []byte("bc")}})
	if m.Lanes() != 1 {
		t.Fatalf("存在单字节文字时应退回单 lane: %d", m.Lanes())
	}
	data := make([]byte, simd.SuperWidth)
	for i := range data {
		data[i] = byte('0' + i%10) // 不含首字节候选
	}
	mask, ok := windowMask(simd.GenericBackend{}, m, data, 0)
	if !ok {
		t.Fatal("窗口掩码计算失败")
	}
	if mask != 0 {
		t.Fatalf("输入不含首字节候选: %032b", mask)
	}
}

// TestWindowMaskUsesFourLanes 验证 lane 数上限为 4，且长文字不会互相干扰。
func TestWindowMaskUsesFourLanes(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("abcd")}, {ID: 2, Value: []byte("abcdEF")}})
	if m.Lanes() != 4 {
		t.Fatalf("最长 lane 数应为 4: %d", m.Lanes())
	}
	data := make([]byte, 32)
	copy(data, "abcd")
	data[8], data[9], data[10], data[11] = 'a', 'b', 'c', 'x' // 第四个 lane 不命中
	mask, ok := windowMask(simd.GenericBackend{}, m, data, 0)
	if !ok {
		t.Fatal("窗口掩码计算失败")
	}
	if mask&1 == 0 {
		t.Fatalf("位置 0 应保留为候选: %032b", mask)
	}
	if mask&(1<<8) != 0 {
		t.Fatalf("位置 8 应被第四 lane 过滤: %032b", mask)
	}
}

// TestFindMatchesBruteForce 验证启用第二字节掩码后候选集合与逐位置确认一致，
// 覆盖向量窗口、尾部和非对齐输入。
func TestFindMatchesBruteForce(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("ab")},
		{ID: 2, Value: []byte("ax")},
		{ID: 3, Value: []byte("Ab"), CaseInsensitive: true},
		{ID: 4, Value: []byte("cd")},
	}
	m := New(literals)
	if m.Lanes() != 2 {
		t.Fatalf("测试文字应启用 2 个 lane: %d", m.Lanes())
	}
	inputs := []string{
		"zzabzzaxzzabzzaxzzABzzabzzaxzzab",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab",
		"ab",
		"",
		"a",
		"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxab",
		"cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd",
	}
	for _, input := range inputs {
		data := []byte(input)
		got := m.Find(data)
		want := bruteForceMatches(data, m.literals)
		if len(got) != len(want) {
			t.Fatalf("输入 %q 候选数量不一致: got=%v want=%v", input, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("输入 %q 第 %d 个候选不一致: got=%v want=%v", input, i, got, want)
			}
		}
	}
}

// TestFindMatchesBruteForceFourLanes 验证 4 个 lane 的掩码与逐位置确认一致。
func TestFindMatchesBruteForceFourLanes(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("abcd")},
		{ID: 2, Value: []byte("ABCD"), CaseInsensitive: true},
		{ID: 3, Value: []byte("abce")},
	}
	m := New(literals)
	if m.Lanes() != 4 {
		t.Fatalf("最短文字长度为 4 时应启用 4 个 lane: %d", m.Lanes())
	}
	inputs := []string{
		"abcdABCDabceabcdef",
		"aaaaabcdABCDabceab",
		"abcd",
		"abc",
		"xxxxxxxxxxxxxxxxabcdxxxxxxxxxxxxxxxx",
		"abceabceabceabceabceabceabceabce",
	}
	for _, input := range inputs {
		data := []byte(input)
		got := m.Find(data)
		want := bruteForceMatches(data, m.literals)
		if len(got) != len(want) {
			t.Fatalf("输入 %q 候选数量不一致: got=%v want=%v", input, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("输入 %q 第 %d 个候选不一致: got=%v want=%v", input, i, got, want)
			}
		}
	}
}

// bruteForceMatches 按起点、编号和终点稳定排序全部真实命中，作为对照实现。
func bruteForceMatches(data []byte, literals []hwlm.Literal) []Match {
	var out []Match
	for from := range data {
		for _, literal := range literals {
			if !hwlm.ContainsAt(data, from, literal) {
				continue
			}
			out = append(out, Match{ID: literal.ID, From: from, To: from + len(literal.Value)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].To < out[j].To
	})
	unique := out[:0]
	for i, match := range out {
		if i > 0 && match == out[i-1] {
			continue
		}
		unique = append(unique, match)
	}
	return unique
}

// BenchmarkFindIntoByteMask 对比第二字节掩码与仅首字节掩码的候选扫描开销。
// 输入构造成首字节全部命中、第二字节几乎全部不命中，用于放大掩码过滤收益。
func BenchmarkFindIntoByteMask(b *testing.B) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("ab")},
		{ID: 2, Value: []byte("ac")},
		{ID: 3, Value: []byte("ad")},
	}
	m := New(literals)
	data := make([]byte, 4096)
	for i := range data {
		data[i] = 'a'
	}
	buf := make([]Match, 0, 64)
	b.Run("second_mask", func(b *testing.B) {
		m.lanes = 2
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf = m.FindInto(data, buf[:0])
		}
	})
	b.Run("first_only", func(b *testing.B) {
		m.lanes = 1
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf = m.FindInto(data, buf[:0])
		}
		m.lanes = 2
	})
}

// BenchmarkWindowMaskClassifiers 对比通用标量与宿主原生后端在窗口掩码上的开销。
func BenchmarkWindowMaskClassifiers(b *testing.B) {
	m := New([]hwlm.Literal{
		{ID: 1, Value: []byte("abcd")},
		{ID: 2, Value: []byte("abce")},
		{ID: 3, Value: []byte("abcf")},
	})
	data := make([]byte, simd.SuperWidth*64)
	for i := range data {
		data[i] = byte('a' + i%4)
	}
	for _, tc := range []struct {
		name    string
		backend simd.Backend
	}{
		{"generic", simd.Default()},
		{"host", dispatch.DefaultBackend()},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for off := 0; off+simd.SuperWidth <= len(data); off += simd.SuperWidth {
					if _, ok := windowMask(tc.backend, m, data, off); !ok {
						b.Fatal("窗口掩码不应越界")
					}
				}
			}
		})
	}
}

// TestFindMatchesRandomizedAgainstBruteForce 用随机文字与随机输入覆盖多个连续 32 字节
// 窗口、窗口末端退让、非对齐起点与大小写折叠，确保宽窗口候选筛选不漏报。
func TestFindMatchesRandomizedAgainstBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(20260924))
	alphabet := []byte("abcdABCDxyz")
	for trial := range 300 {
		literals := make([]hwlm.Literal, 0, 4)
		for i := 0; i < rng.Intn(4)+1; i++ {
			value := make([]byte, rng.Intn(6)+1)
			for j := range value {
				value[j] = alphabet[rng.Intn(len(alphabet))]
			}
			literals = append(literals, hwlm.Literal{
				ID:              uint32(i + 1),
				Value:           value,
				CaseInsensitive: rng.Intn(2) == 0,
			})
		}
		m := New(literals)
		data := make([]byte, rng.Intn(160)+1)
		for i := range data {
			data[i] = alphabet[rng.Intn(len(alphabet))]
		}
		got := m.Find(data)
		want := bruteForceMatches(data, m.literals)
		if len(got) != len(want) {
			t.Fatalf("迭代 %d 候选数量不一致: got=%d want=%d literals=%v data=%q", trial, len(got), len(want), m.literals, data)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("迭代 %d 第 %d 个候选不一致: got=%v want=%v data=%q", trial, i, got[i], want[i], data)
			}
		}
	}
}

// TestFindMatchesWideWindowBoundary 覆盖 64 字节宽窗口边界（63/64/65 字节及其倍数）
// 附近的候选起点：命中跨窗口、窗口末端退让与尾部逐字节回填三条路径必须都不漏报，
// 结果与逐位置暴力参考完全一致。
func TestFindMatchesWideWindowBoundary(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("abc")},
		{ID: 2, Value: []byte("cd"), CaseInsensitive: true},
	}
	m := New(literals)
	// 两组文字的落点互不重叠，且刻意跨越 64 字节宽窗口边界（64/128/192 附近）。
	wideStarts := []int{0, 8, 30, 38, 60, 68, 126, 134, 190}
	shortStarts := []int{4, 34, 64, 130, 194}
	for _, length := range []int{63, 64, 65, 127, 128, 129, 191, 192, 193} {
		data := make([]byte, length)
		for i := range data {
			data[i] = 'q'
		}
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
		got := m.Find(data)
		want := bruteForceMatches(data, m.literals)
		if len(got) != len(want) {
			t.Fatalf("长度=%d 候选数量不一致: got=%d want=%d", length, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("长度=%d 第 %d 个候选不一致: got=%v want=%v", length, i, got[i], want[i])
			}
		}
		if length < 128 {
			continue
		}
		// 长语料必须存在落在第二个及之后的宽窗口内的命中，避免测试空转。
		maxFrom := -1
		for _, match := range got {
			if match.From > maxFrom {
				maxFrom = match.From
			}
		}
		if maxFrom < simd.WideWidth {
			t.Fatalf("长度=%d 语料未覆盖 64 字节宽窗口之外，最大命中偏移=%d", length, maxFrom)
		}
	}
}
