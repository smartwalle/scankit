package noodle

import (
	"bytes"
	"testing"

	"github.com/smartwalle/scankit/internal/hwlm"
)

// TestFrozenChildLookupCoversAllBytes 验证压缩位图布局对全部 256 个字节
// 都能给出正确的子节点判定，覆盖位图分组与 popcount 定位的边界。
func TestFrozenChildLookupCoversAllBytes(t *testing.T) {
	literals := make([]hwlm.Literal, 0, 256)
	for value := 0; value < 256; value++ {
		literals = append(literals, hwlm.Literal{ID: uint32(value + 1), Value: []byte{byte(value)}})
	}
	// 逆序插入，迫使冻结阶段依赖字节序而不是插入顺序重建索引。
	for left, right := 0, len(literals)-1; left < right; left, right = left+1, right-1 {
		literals[left], literals[right] = literals[right], literals[left]
	}
	matcher := New(literals)
	if matcher.sensitive == nil {
		t.Fatal("missing frozen root")
	}
	for value := 0; value < 256; value++ {
		child := matcher.sensitive.child(byte(value))
		if child == nil {
			t.Fatalf("child for byte %d is missing", value)
		}
		if len(child.terminal) != 1 || !bytes.Equal(child.terminal[0].Value, []byte{byte(value)}) {
			t.Fatalf("child for byte %d resolves to %+v", value, child.terminal)
		}
	}
	// 重复子节点不应被重复登记。
	if count := len(matcher.sensitive.kids); count != 256 {
		t.Fatalf("root child count = %d, want 256", count)
	}
}

// TestCompressedTrieMatchesReference 用暴力扫描校验压缩前缀树的候选结果，
// 覆盖大小写折叠、重复文字、互为前缀的文字与乱序插入。
func TestCompressedTrieMatchesReference(t *testing.T) {
	literals := []hwlm.Literal{
		{ID: 7, Value: []byte("abc")},
		{ID: 3, Value: []byte("ab")},
		{ID: 9, Value: []byte("abc")},
		{ID: 4, Value: []byte("ABCD"), CaseInsensitive: true},
		{ID: 5, Value: []byte("b")},
		{ID: 6, Value: []byte("zab")},
		{ID: 8, Value: []byte("aBcD")},
		{ID: 11, Value: []byte{0x00, 'x'}},
		{ID: 12, Value: []byte("abx")},
	}
	matcher := New(literals)
	data := []byte("xxabcabABCDabcd\x00x aB zabc ab")
	got := matcher.Find(data)
	want := referenceMatches(literals, data)
	if len(got) != len(want) {
		t.Fatalf("candidate count = %d, want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("candidate %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

// referenceMatches 逐位置逐文字做朴素扫描，作为压缩前缀树的行为基准。
func referenceMatches(literals []hwlm.Literal, data []byte) []Match {
	out := make([]Match, 0)
	for from := 0; from < len(data); from++ {
		for _, literal := range literals {
			value := literal.Value
			if from+len(value) > len(data) {
				continue
			}
			window := data[from : from+len(value)]
			if literal.CaseInsensitive {
				if !bytes.EqualFold(window, value) {
					continue
				}
			} else if !bytes.Equal(window, value) {
				continue
			}
			out = append(out, Match{ID: literal.ID, From: from, To: from + len(value)})
		}
	}
	// 与 FindInto 保持一致的排序与去重规则：先按起点，再按编号，最后按结束。
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			less := out[j].From < out[i].From ||
				out[j].From == out[i].From && out[j].ID < out[i].ID ||
				out[j].From == out[i].From && out[j].ID == out[i].ID && out[j].To < out[i].To
			if less {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	write := 0
	for _, match := range out {
		if write > 0 && out[write-1] == match {
			continue
		}
		out[write] = match
		write++
	}
	return out[:write]
}
