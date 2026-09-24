package scankit

import "testing"

func TestScanIntegratesRoseOnlyForFullyConvertibleRules(t *testing.T) {
	direct, err := Compile([]Expression{
		{Id: 1, Pattern: "ab"},
		{Id: 2, Pattern: "bc", Flags: CompileCaseless},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("ABabc")
	scan, err := direct.Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	rose := direct.scanRoseInto(input, nil)
	if len(scan) != len(rose) {
		t.Fatalf("Rose 与 Scan 结果数量不一致: scan=%v rose=%v", scan, rose)
	}
	for i := range scan {
		if scan[i] != rose[i] {
			t.Fatalf("Rose 与 Scan 结果不一致: scan=%v rose=%v", scan, rose)
		}
	}

	lookbehind, err := Compile([]Expression{{Id: 3, Pattern: `(?<=a)b`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := lookbehind.Scan([]byte("ab xb"))
	if err != nil || len(got) != 1 || got[0] != (Match{Id: 3, From: 1, To: 2}) {
		t.Fatalf("不可转换规则未安全回退: err=%v matches=%v", err, got)
	}
}
