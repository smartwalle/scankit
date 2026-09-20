package nfa

import "testing"

func TestEngineKindString(t *testing.T) {
	if EngineCastle.String() != "castle" {
		t.Fatal()
	}
	if !EngineLBR.Valid() || EngineKind(99).Valid() {
		t.Fatal("引擎类型有效性错误")
	}
}
