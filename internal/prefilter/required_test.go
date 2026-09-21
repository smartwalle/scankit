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
	for _, pattern := range []string{`[a-z]+`, `(?:ab)+\.go`} {
		root, err := parser.Parse(pattern)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", pattern, err)
		}
		if required, ok := FromAST(root); ok {
			t.Fatalf("模式 %q 不应产出候选文字: %v", pattern, dumpVariants(required))
		}
	}
}

func dumpVariants(required Required) string {
	var parts []string
	for _, v := range required.Variants {
		parts = append(parts, string(v.Value))
	}
	return strings.Join(parts, ",")
}
