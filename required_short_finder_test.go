package scankit

import (
	"sort"
	"testing"

	"github.com/smartwalle/scankit/internal/hwlm"
)

// refShortHits 是候选文字匹配器的朴素参考实现：逐起点逐文字比较。
func refShortHits(literals []hwlm.Literal, data []byte) []requiredHit {
	var out []requiredHit
	for position := 0; position < len(data); position++ {
		for _, literal := range literals {
			value := literal.Value
			if len(value) < 2 || len(value) > 3 {
				continue
			}
			if position+len(value) > len(data) {
				continue
			}
			if string(data[position:position+len(value)]) != string(value) {
				continue
			}
			out = append(out, requiredHit{slot: int(literal.ID) - 1, pos: position})
		}
	}
	return out
}

func sortHits(hits []requiredHit) []requiredHit {
	out := append([]requiredHit(nil), hits...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].pos != out[j].pos {
			return out[i].pos < out[j].pos
		}
		return out[i].slot < out[j].slot
	})
	return out
}

func equalHits(got, want []requiredHit) bool {
	if len(got) != len(want) {
		return false
	}
	g, w := sortHits(got), sortHits(want)
	for index := range g {
		if g[index] != w[index] {
			return false
		}
	}
	return true
}

// TestRequiredShortFinderMatchesReference 用「同一批文字、任意长度的输入前缀」
// 覆盖窗口边界：宽窗口扫描把输入切成 64 字节段，最后一段还不足一个完整窗口，
// 逐字节回退与窗口内部两条路径必须在每个截断长度上都给出与朴素实现一致的结果。
func TestRequiredShortFinderMatchesReference(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("18")},
		{ID: 2, Value: []byte("19")},
		{ID: 3, Value: []byte("20")},
		// 与 "18" 共享「首字节 + 次字节」前缀的 3 字节文字。
		{ID: 4, Value: []byte("189")},
		{ID: 5, Value: []byte("620")},
		{ID: 6, Value: []byte("621")},
	}
	finder := newRequiredShortFinder(literals)
	if finder == nil {
		t.Fatal("finder for 2~3 byte literals must not be nil")
	}
	pattern := []byte("x18y19z20w189620q62118918")
	for length := 0; length <= len(pattern); length++ {
		data := pattern[:length]
		got := finder(data, nil)
		want := refShortHits(literals, data)
		if !equalHits(got, want) {
			t.Fatalf("length %d\ngot  %v\nwant %v", length, sortHits(got), sortHits(want))
		}
	}
}

// TestRequiredShortFinderRepeatedData 用更长的重复输入确认窗口内部与窗口之间
// 的候选都不会漏报或重复。
func TestRequiredShortFinderRepeatedData(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 1, Value: []byte("13")},
		{ID: 2, Value: []byte("34")},
		{ID: 3, Value: []byte("345")},
	}
	finder := newRequiredShortFinder(literals)
	if finder == nil {
		t.Fatal("finder for 2~3 byte literals must not be nil")
	}
	unit := []byte("013450134513")
	var data []byte
	for range 40 {
		data = append(data, unit...)
	}
	got := finder(data, nil)
	want := refShortHits(literals, data)
	if !equalHits(got, want) {
		t.Fatalf("hit mismatch: got %d hits want %d hits", len(got), len(want))
	}
}

// TestRequiredShortFinderRejectsLongLiterals 确认专用匹配器只认领自己能处理的
// 长度：集合里没有 2~3 字节文字时必须返回 nil，让调用方回退通用匹配器。
func TestRequiredShortFinderRejectsLongLiterals(t *testing.T) {
	for _, literals := range [][]hwlm.Literal{
		nil,
		{{ID: 1, Value: []byte("a")}},
		{{ID: 1, Value: []byte("abcd")}},
	} {
		if finder := newRequiredShortFinder(literals); finder != nil {
			t.Fatalf("literals %v: want nil finder", literals)
		}
	}
}

// TestRequiredShortFinderSharedPrefix 校验同一「首字节 + 次字节」前缀同时终结
// 2 字节文字并延伸出 3 字节文字时两类命中都会产出，且不会互相覆盖。
func TestRequiredShortFinderSharedPrefix(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 7, Value: []byte("18")},
		{ID: 8, Value: []byte("189")},
	}
	finder := newRequiredShortFinder(literals)
	if finder == nil {
		t.Fatal("finder must not be nil")
	}
	data := []byte("189")
	got := finder(data, nil)
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 hits", got)
	}
	sorted := sortHits(got)
	if sorted[0] != (requiredHit{slot: 6, pos: 0}) || sorted[1] != (requiredHit{slot: 7, pos: 0}) {
		t.Fatalf("got %v, want both prefix and extended literal hits", sorted)
	}
}
