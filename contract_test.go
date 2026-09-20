package scankit

import "testing"

func TestCompileFlagsValues(t *testing.T) {
	tests := []struct {
		name  string
		value CompileFlag
		want  CompileFlag
	}{
		{"caseless", FlagCaseless, 1},
		{"dotall", FlagDotAll, 2},
		{"multiline", FlagMultiline, 4},
		{"singlematch", FlagSingleMatch, 8},
		{"allowempty", FlagAllowEmpty, 16},
		{"utf8", FlagUTF8, 32},
		{"ucp", FlagUCP, 64},
		{"prefilter", FlagPrefilter, 128},
		{"som_leftmost", FlagSOMLeftmost, 256},
		{"combination", FlagCombination, 512},
		{"quiet", FlagQuiet, 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.value != test.want {
				t.Fatalf("%s = %d, want %d", test.name, test.value, test.want)
			}
		})
	}
}

func TestExpressionExtFlagsValues(t *testing.T) {
	if ExtFlagMinOffset != 1 || ExtFlagMaxOffset != 2 || ExtFlagMinLength != 4 ||
		ExtFlagEditDistance != 8 || ExtFlagHammingDistance != 16 {
		t.Fatalf("unexpected ExpressionExtFlag values")
	}
}

func TestScannerMetadataSnapshot(t *testing.T) {
	s, err := Compile([]Expression{{Id: 7, Pattern: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	ids := s.ruleIDs()
	if len(ids) != 1 || ids[0] != 7 {
		t.Fatal(ids)
	}
	info, ok := s.expressionInfo(7)
	if !ok || info.ID != 7 || info.Graph == nil {
		t.Fatalf("元数据缺失: %#v", info)
	}
	info.Graph.Nodes[info.Graph.Start].Kind = 0
	if again, _ := s.expressionInfo(7); again.Graph.Nodes[again.Graph.Start].Kind == 0 {
		t.Fatal("元数据未隔离")
	}
}

func TestScannerUsesCompiledBackend(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.backendName(1) == "" {
		t.Fatal("规则未绑定执行后端")
	}
	if s.usage.ProgramBytes == 0 {
		t.Fatal("资源统计为空")
	}
	if s.clone().usage != s.usage {
		t.Fatal("克隆未保留资源统计")
	}
}

func TestScanIntoPreservesExistingPrefix(t *testing.T) {
	s, err := Compile([]Expression{{Id: 2, Pattern: "b"}, {Id: 1, Pattern: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	prefix := []Match{{Id: 9, From: 99, To: 100}}
	matches, err := s.ScanInto([]byte("ab"), prefix)
	if err != nil || len(matches) != 3 || matches[0] != prefix[0] || matches[1].Id != 1 || matches[2].Id != 2 {
		t.Fatalf("scan into mismatch: %v %#v", err, matches)
	}
}
