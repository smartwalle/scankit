package prefilter

import (
	"bytes"
	"strings"
	"testing"

	"github.com/smartwalle/scankit/internal/parser"
)

func mustRequired(t *testing.T, pattern string) Required {
	t.Helper()
	root, err := parser.Parse(pattern)
	if err != nil {
		t.Fatalf("解析 %q 失败: %v", pattern, err)
	}
	required, ok := FromAST(root)
	if !ok {
		t.Fatalf("模式 %q 未提取到候选文字", pattern)
	}
	return required
}

func TestFromASTLiteral(t *testing.T) {
	required := mustRequired(t, "abc")
	if len(required.Variants) != 1 {
		t.Fatalf("变体数量=%d", len(required.Variants))
	}
	v := required.Variants[0]
	if string(v.Value) != "abc" || v.MinOffset != 0 || v.MaxOffset != 0 || v.Back != nil {
		t.Fatalf("变体=%q,min=%d,max=%d,back=%v", v.Value, v.MinOffset, v.MaxOffset, v.Back)
	}
}

// 类重复前缀的偏移无上界，但左侧字节集合可把起点收缩到有限区间，
// 因此仍应产出带 Back 的候选文字。
func TestFromASTUnboundedClassPrefixKeepsBack(t *testing.T) {
	required := mustRequired(t, `[A-Za-z0-9._/-]+\.go`)
	if len(required.Variants) != 1 {
		t.Fatalf("变体数量=%d", len(required.Variants))
	}
	v := required.Variants[0]
	if string(v.Value) != ".go" {
		t.Fatalf("候选文字=%q", v.Value)
	}
	if v.MinOffset != 1 || v.MaxOffset >= v.MinOffset {
		t.Fatalf("偏移窗口=[%d,%d]", v.MinOffset, v.MaxOffset)
	}
	if v.Back == nil {
		t.Fatal("无上界窗口必须携带左侧字节集合")
	}
	for _, b := range []byte{'a', 'Z', '0', '.', '/', '-'} {
		if v.Back[b] == 0 {
			t.Fatalf("左侧字节集合缺少 %q", b)
		}
	}
	for _, b := range []byte{'\n', ' ', '@'} {
		if v.Back[b] != 0 {
			t.Fatalf("左侧字节集合不应包含 %q", b)
		}
	}
}

// 可变宽度元素不得与其后的文字拼接：数字前缀出现的次数不确定，
// 拼出的 "0abc" 之类的文字并不必然出现。
// TestFromASTPrefersFixedSeparatorAfterUnexpandableRepeat 断言后续元素无法逐
// 字节展开（如 `[0-9]{8}`）时仍会选中固定偏移上的分隔符，而不是退化成命中
// 密度极高的单字节类成员。
func TestFromASTPrefersFixedSeparatorAfterUnexpandableRepeat(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		offset  int
	}{
		{pattern: `[0-9]{4}(-[0-9]{4}){3}`, value: "-", offset: 4},
		{pattern: `[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}`, value: ":", offset: 2},
		{pattern: `[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}`, value: "-", offset: 8},
		{pattern: `[0-9]{4}/[0-9]{2}/[0-9]{2}`, value: "/", offset: 4},
	}
	for _, testCase := range cases {
		t.Run(testCase.pattern, func(t *testing.T) {
			required := mustRequired(t, testCase.pattern)
			if len(required.Variants) == 0 {
				t.Fatal("未提取到候选文字")
			}
			for index, variant := range required.Variants {
				if len(variant.Value) == 0 || string(variant.Value[:1]) != testCase.value {
					t.Fatalf("第 %d 个候选文字=%q，期望以 %q 开头", index, string(variant.Value), testCase.value)
				}
				if variant.MinOffset != testCase.offset || variant.MaxOffset != testCase.offset {
					t.Fatalf("第 %d 个候选文字偏移窗口=[%d,%d]，期望 [%d,%d]", index, variant.MinOffset, variant.MaxOffset, testCase.offset, testCase.offset)
				}
			}
		})
	}
}

// TestFromASTLeadingLiteralBounds 验证从元素起点推导的文字偏移仍落在前缀宽度
// 区间内：可变宽度前缀必须同时给出下界与上界。
func TestFromASTLeadingLiteralBounds(t *testing.T) {
	required := mustRequired(t, `[0-9]{2,4}-[0-9]{3}`)
	if len(required.Variants) == 0 {
		t.Fatal("未提取到候选文字")
	}
	for index, variant := range required.Variants {
		if len(variant.Value) == 0 || variant.Value[0] != '-' {
			t.Fatalf("第 %d 个候选文字=%q，期望以 - 开头", index, string(variant.Value))
		}
		if variant.MinOffset != 2 || variant.MaxOffset != 4 {
			t.Fatalf("第 %d 个偏移窗口=[%d,%d]，期望 [2,4]", index, variant.MinOffset, variant.MaxOffset)
		}
	}
}

func TestFromASTVariableWidthPrefixDoesNotJoinSuffix(t *testing.T) {
	required := mustRequired(t, `[0-9]{1,3}abc`)
	if len(required.Variants) != 1 {
		t.Fatalf("变体数量=%d", len(required.Variants))
	}
	v := required.Variants[0]
	if string(v.Value) != "abc" || v.MinOffset != 1 || v.MaxOffset != 3 {
		t.Fatalf("变体=%q,min=%d,max=%d", v.Value, v.MinOffset, v.MaxOffset)
	}
}

// 定长前缀仍可与其后的类拼接，偏移窗口收敛为 0。
func TestFromASTFixedPrefixJoinsClass(t *testing.T) {
	required := mustRequired(t, `abc[0-9]{1,3}`)
	if len(required.Variants) == 0 {
		t.Fatal("缺少变体")
	}
	for _, v := range required.Variants {
		if len(v.Value) != 4 || string(v.Value[:3]) != "abc" {
			t.Fatalf("变体=%q", v.Value)
		}
		if v.MinOffset != 0 || v.MaxOffset != 0 {
			t.Fatalf("偏移窗口=[%d,%d]", v.MinOffset, v.MaxOffset)
		}
		for _, b := range v.Value[3:] {
			if b < '0' || b > '9' {
				t.Fatalf("变体 %q 末字节不是数字", v.Value)
			}
		}
	}
}

func TestFromASTClassPrefixWithPunctuation(t *testing.T) {
	required := mustRequired(t, `rgb\([0-9]{1,3},`)
	if len(required.Variants) == 0 {
		t.Fatal("缺少变体")
	}
	for _, v := range required.Variants {
		if !bytes.HasPrefix(v.Value, []byte("rgb(")) {
			t.Fatalf("变体 %q 不以 rgb( 开头", v.Value)
		}
	}
}

func TestFromASTRejectsUnboundedWithoutBack(t *testing.T) {
	// 无上界前缀若无法给出字节超集（点号、取反类、引用等），候选窗口不受约束，
	// 必须拒绝；只有任意匹配都必然包含的候选文字才允许进入索引。
	for _, pattern := range []string{`[a-z]+`, `.+abc`, `[^\n]+abc`} {
		root, err := parser.Parse(pattern)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", pattern, err)
		}
		if required, ok := FromAST(root); ok {
			t.Fatalf("模式 %q 不应产出候选文字: %v", pattern, dumpVariants(required))
		}
	}
}

// 前缀由多个元素组成时，只要能够给出字节超集，无上界窗口依然可用；
// Back 集合必须覆盖前缀可能消费的全部字节，否则左侧收缩会越过真实起点。
func TestFromASTUnboundedMultiElementPrefixKeepsBack(t *testing.T) {
	required := mustRequired(t, `(?:ab)+\.go`)
	if len(required.Variants) != 1 {
		t.Fatalf("变体数量=%d", len(required.Variants))
	}
	v := required.Variants[0]
	if string(v.Value) != ".go" {
		t.Fatalf("候选文字=%q", v.Value)
	}
	if v.MaxOffset >= v.MinOffset {
		t.Fatalf("偏移窗口=[%d,%d]", v.MinOffset, v.MaxOffset)
	}
	if v.Back == nil {
		t.Fatal("无上界窗口必须携带左侧字节集合")
	}
	for _, b := range []byte{'a', 'b'} {
		if v.Back[b] == 0 {
			t.Fatalf("左侧字节集合缺少 %q", b)
		}
	}
	if v.Back['c'] != 0 {
		t.Fatalf("左侧字节集合不应包含 'c'")
	}
}

func dumpVariants(required Required) string {
	var parts []string
	for _, v := range required.Variants {
		parts = append(parts, string(v.Value))
	}
	return strings.Join(parts, ",")
}

// TestFromASTMergesCaseClassWithTrailingToken 验证落在字符类上的弱候选会与前序
// 元素末尾必然出现的字节合并成多字节候选：科学计数法规则里的 `[eE]` 单独展开只
// 能得到单字节 `e`/`E`，在自然文本上命中密度极高；合并前一个必现的数字后得到
// `0e`…`9E` 这 20 个双字节候选，命中密度回到与输入内容无关的水平。
func TestFromASTMergesCaseClassWithTrailingToken(t *testing.T) {
	required := mustRequired(t, `[+-]?[0-9]+(\.[0-9]+)?[eE][+-]?[0-9]+`)
	if len(required.Variants) != 20 {
		t.Fatalf("变体数量=%d，期望 20: %s", len(required.Variants), dumpVariants(required))
	}
	seen := make(map[string]struct{}, len(required.Variants))
	for index, variant := range required.Variants {
		if len(variant.Value) != 2 {
			t.Fatalf("第 %d 个候选文字=%q，期望双字节", index, variant.Value)
		}
		if variant.Value[0] < '0' || variant.Value[0] > '9' {
			t.Fatalf("第 %d 个候选文字=%q，首字节应为数字", index, variant.Value)
		}
		if variant.Value[1] != 'e' && variant.Value[1] != 'E' {
			t.Fatalf("第 %d 个候选文字=%q，次字节应为 e 或 E", index, variant.Value)
		}
		if variant.MaxOffset >= variant.MinOffset {
			t.Fatalf("第 %d 个候选文字窗口=[%d,%d]，期望无上界", index, variant.MinOffset, variant.MaxOffset)
		}
		if variant.Back == nil {
			t.Fatalf("第 %d 个候选文字缺少左侧字节集合", index)
		}
		seen[string(variant.Value)] = struct{}{}
	}
	if len(seen) != 20 {
		t.Fatalf("去重后的候选数量=%d，期望 20", len(seen))
	}
}

// TestTrailingLiteralsUnionsNullableBranches 验证左合并候选在跨越可空元素时按
// 分支取并集：`a(?:b)?[0-9]` 中数字前的字节可能是 `a`（可空分支为空），也可能是
// `b`（可空分支非空）。只保留其中一支会让另一类匹配在候选索引里漏检。
func TestTrailingLiteralsUnionsNullableBranches(t *testing.T) {
	root, err := parser.Parse(`a(?:b)?[0-9]`)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	sequence, ok := root.(parser.Sequence)
	if !ok {
		t.Fatalf("根节点类型=%T，期望序列", root)
	}
	steps, length, ok := trailingUniform(sequence.Elements[:2])
	if !ok {
		t.Fatal("可空前缀应能推导出末尾候选")
	}
	if length != 1 {
		t.Fatalf("末尾候选长度=%d，期望 1", length)
	}
	seen := make(map[string]struct{}, len(steps))
	for _, step := range steps {
		if len(step.value) != 1 {
			t.Fatalf("末尾候选=%q，期望单字节", step.value)
		}
		seen[string(step.value)] = struct{}{}
	}
	for _, want := range []string{"a", "b"} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("末尾候选缺少 %q: %v", want, seen)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("末尾候选数量=%d，期望 2: %v", len(seen), seen)
	}
}
