package scankit

import (
	"bytes"
	"testing"
)

func TestCloneAndMetadata(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "a", Flags: CompileCaseless, Ext: &ExpressionExt{Flags: ExtFlagMinLength, MinLength: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if clone := s.clone(); clone == nil || len(clone.ruleIDs()) != 1 {
		t.Fatal("clone failed")
	}
	if flags, ok := s.ruleFlags(1); !ok || flags != CompileCaseless {
		t.Fatalf("rule flags=%v %v", flags, ok)
	}
	ext, ok := s.ruleExtension(1)
	if !ok || ext == nil || ext.MinLength != 1 {
		t.Fatalf("rule extension=%#v %v", ext, ok)
	}
	ext.MinLength = 9
	ext, _ = s.ruleExtension(1)
	if ext.MinLength != 1 {
		t.Fatalf("rule extension leaked mutable state: %#v", ext)
	}
	data := Replace([]byte("abcd"), []Match{{Id: 1, From: 0, To: 2}, {Id: 2, From: 1, To: 3}}, func(buf *bytes.Buffer, _ Match, _ []byte) { buf.WriteString("X") })
	if string(data) != "Xcd" {
		t.Fatalf("重叠替换结果=%q", data)
	}
}

func TestNilEngineAPIsAreSafe(t *testing.T) {
	var e *Engine
	if got, err := e.Scan(nil); got != nil || err != nil {
		t.Fatal("空 Engine 接口应安全返回")
	}
}
