package parser

import "testing"

func TestSemanticHelpers(t *testing.T) {
	n, err := Parse(`(a)?b`)
	if err != nil {
		t.Fatal(err)
	}
	if CaptureCount(n) != 1 || !Nullable(Sequence{Elements: []Node{Repeat{Child: Literal{Value: []byte{'x'}}, Min: 0, Max: 1}}}) {
		t.Fatal("semantic helper mismatch")
	}
	min, max, ok := FixedWidth(n)
	if ok || min != -1 || max != -1 {
		t.Fatalf("unexpected width %d %d %v", min, max, ok)
	}
	copyNode := Clone(n).(Sequence)
	if len(copyNode.Elements) != 2 {
		t.Fatalf("clone %#v", copyNode)
	}
	if Depth(n) < 1 {
		t.Fatalf("depth=%d", Depth(n))
	}
}

func TestSummarizeAndChildren(t *testing.T) {
	node, err := Parse("(ab|cd)+")
	if err != nil {
		t.Fatal(err)
	}
	s := Summarize(node)
	if s.Nodes < 4 || s.Literals != 4 || s.MaxDepth < 2 || s.Nullable {
		t.Fatalf("摘要=%#v", s)
	}
}

func TestCaptureAndReferenceIDs(t *testing.T) {
	n, err := Parse(`((a)|b)(?(2)c|d)\1\2`)
	if err != nil {
		t.Fatal(err)
	}
	captures := CaptureIDs(n)
	if len(captures) != 2 || captures[0] != 1 || captures[1] != 2 {
		t.Fatalf("capture ids=%v", captures)
	}
	references := ReferenceIDs(n)
	if len(references) != 2 || references[0] != 2 || references[1] != 1 {
		t.Fatalf("reference ids=%v", references)
	}
}

func TestStatefulRuntimeRequirement(t *testing.T) {
	for _, pattern := range []string{`a(?=b)`, `a(*FAIL)`, `(?>a|ab)b`, `(a)\1`} {
		node, err := Parse(pattern)
		if err != nil || !RequiresStatefulRuntime(node) {
			t.Fatalf("stateful requirement %q: %v", pattern, err)
		}
	}
	node, err := Parse(`a(b|c)+`)
	if err != nil || RequiresStatefulRuntime(node) {
		t.Fatalf("ordinary expression unexpectedly stateful: %v", err)
	}
}

func TestFixedWidthUTF8(t *testing.T) {
	node := Literal{Value: []byte("é")}
	if min, max, ok := FixedWidthUTF8(node); !ok || min != 2 || max != 2 {
		t.Fatalf("宽度=%d,%d,%v", min, max, ok)
	}
	parsed, err := Parse(".")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FixedWidthUTF8(parsed); ok {
		t.Fatal("通配符宽度不应被判定为固定字节数")
	}
}

func TestValidateWithUTF8LookbehindWidth(t *testing.T) {
	node, err := Parse(`(?<=.)a`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWithUTF8(node, true); err == nil {
		t.Fatal("UTF-8 可变宽度反向断言不应通过")
	}
}

func TestValidateRejectsForwardReferences(t *testing.T) {
	for _, pattern := range []string{`\1(a)`, `(?(1)a|b)(a)`} {
		node, err := Parse(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(node); err == nil {
			t.Fatalf("forward reference accepted: %q", pattern)
		}
	}
	for _, pattern := range []string{`(a)\1`, `(?:(a)|b)\1`} {
		node, err := Parse(pattern)
		if err != nil || Validate(node) != nil {
			t.Fatalf("valid reference rejected: %q %v", pattern, err)
		}
	}
}
