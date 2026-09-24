package scankit

import (
	"bytes"
	"strconv"
	"testing"
)

func TestSingleMatchAndInputImmutability(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "a+", Flags: CompileSingleMatch}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("aa baaa")
	original := append([]byte(nil), data...)
	got, err := s.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != 0 || got[0].To != 2 {
		t.Fatalf("matches=%#v", got)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("scanner modified input")
	}
}

func TestOffsetAndLengthConstraints(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "abc", Ext: &ExpressionExt{Flags: ExtFlagMinOffset | ExtFlagMaxOffset | ExtFlagMinLength, MinOffset: 2, MaxOffset: 6, MinLength: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("xxabc abc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].From != 2 || got[0].To != 5 {
		t.Fatalf("matches=%#v", got)
	}
}

func TestOffsetConstraintsUseMatchEnd(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "abc", Ext: &ExpressionExt{Flags: ExtFlagMinOffset | ExtFlagMaxOffset, MinOffset: 3, MaxOffset: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Scan([]byte("abc"))
	if err != nil || len(got) != 1 || got[0] != (Match{Id: 1, From: 0, To: 3}) {
		t.Fatalf("end-offset constraints mismatch: %v %#v", err, got)
	}
}

func BenchmarkScanRuleScales(b *testing.B) {
	data := bytes.Repeat([]byte("prefix abc suffix "), 64)
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run("rules_"+strconv.Itoa(count), func(b *testing.B) {
			expressions := make([]Expression, count)
			for i := range expressions {
				expressions[i] = Expression{Id: uint32(i + 1), Pattern: "abc"}
			}
			s, err := Compile(expressions)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err = s.Scan(data)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
