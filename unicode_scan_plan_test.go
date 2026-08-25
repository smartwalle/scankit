package scankit

import "testing"

func TestCompileSelectsUnicodeScanPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expressions []Expression
		want        unicodeScanPlan
		data        []byte
	}{
		{
			name:        "pure UCP",
			expressions: []Expression{{Id: 1, Pattern: `\p{Han}+`, Flags: CompileUTF8 | CompileUnicodeProperties}},
			want:        unicodeScanPlanPure,
			data:        []byte("张"),
		},
		{
			name: "UCP plus literals",
			expressions: []Expression{
				{Id: 1, Pattern: `\p{Han}+`, Flags: CompileUTF8 | CompileUnicodeProperties},
				{Id: 2, Pattern: "token"},
			},
			want: unicodeScanPlanLiteralAC,
			data: []byte("张token"),
		},
		{
			name: "UCP plus simple repeats",
			expressions: []Expression{
				{Id: 1, Pattern: `\p{Han}+`, Flags: CompileUTF8 | CompileUnicodeProperties},
				{Id: 2, Pattern: `\d+`},
			},
			want: unicodeScanPlanSimpleRepeats,
			data: []byte("张12"),
		},
		{
			name: "UCP plus anchored byte regex",
			expressions: []Expression{
				{Id: 1, Pattern: `\p{Han}+`, Flags: CompileUTF8 | CompileUnicodeProperties},
				{Id: 2, Pattern: `ID:\d+`},
			},
			want: unicodeScanPlanGeneric,
			data: []byte("张ID:12"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := Compile(tt.expressions)
			if err != nil {
				t.Fatal(err)
			}
			if db.unicodeScanPlan != tt.want {
				t.Fatalf("unicode scan plan = %d, want %d", db.unicodeScanPlan, tt.want)
			}
			if _, err := db.Scan(tt.data); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnicodeGenericPlanUsesSharedFixedByteLanes(t *testing.T) {
	db, err := Compile([]Expression{
		{Id: 1, Pattern: `\p{Han}+`, Flags: CompileUTF8 | CompileUnicodeProperties},
		{Id: 2, Pattern: `\d{2}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if db.unicodeScanPlan != unicodeScanPlanGeneric || !db.blockScanPlan.unanchored.hasLanes() {
		t.Fatalf("unicode plan = %d with byte lanes=%t, want generic shared lanes", db.unicodeScanPlan, db.blockScanPlan.unanchored.hasLanes())
	}
	matches, err := db.Scan([]byte("张123"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{
		{Id: 1, From: 0, To: 3},
		{Id: 2, From: 3, To: 5},
		{Id: 2, From: 4, To: 6},
	}
	if len(matches) != len(want) {
		t.Fatalf("matches = %#v, want %#v", matches, want)
	}
	for index := range want {
		if matches[index] != want[index] {
			t.Fatalf("match %d = %#v, want %#v", index, matches[index], want[index])
		}
	}
}
