package scankit

import (
	"fmt"
	"testing"
)

func TestPrefilterPreservesResults(t *testing.T) {
	expr := Expression{Id: 1, Pattern: `foo[0-9]+bar`}
	a, e := Compile([]Expression{expr})
	if e != nil {
		t.Fatal(e)
	}
	expr.Flags = CompilePrefilter
	b, e := Compile([]Expression{expr})
	if e != nil {
		t.Fatal(e)
	}
	data := []byte("foo12bar xfoo9bar yfooXbar")
	ma, _ := a.Scan(data)
	mb, _ := b.Scan(data)
	if len(ma) != len(mb) {
		t.Fatalf("%#v %#v", ma, mb)
	}
	for i := range ma {
		if ma[i] != mb[i] {
			t.Fatalf("%#v %#v", ma, mb)
		}
	}
}

func TestPrefilterPreservesCaselessResults(t *testing.T) {
	expr := Expression{Id: 1, Pattern: `foo[0-9]+bar`, Flags: CompileCaseless}
	without, err := Compile([]Expression{expr})
	if err != nil {
		t.Fatal(err)
	}
	expr.Flags |= CompilePrefilter
	with, err := Compile([]Expression{expr})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("FOO12BAR fOo9BaR fooXbar")
	want, err := without.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := with.Scan(data)
	if err != nil || len(got) != len(want) {
		t.Fatalf("prefilter changed caseless result: %v want=%#v got=%#v", err, want, got)
	}
	for index := range want {
		if want[index] != got[index] {
			t.Fatalf("prefilter changed caseless match: want=%#v got=%#v", want, got)
		}
	}
}

func TestPrefilterDoesNotRejectUnicodeCaselessLiteral(t *testing.T) {
	s, err := Compile([]Expression{{Id: 9, Pattern: "ÄBC", Flags: CompileUTF8 | CompileCaseless | CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("äbc"))
	if err != nil || len(matches) != 1 || matches[0].Id != 9 {
		t.Fatalf("Unicode 大小写候选过滤错误: %v %#v", err, matches)
	}
}

func TestPrefilterSquashesMinimumLength(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: "a", Flags: CompilePrefilter, Ext: &ExpressionExt{Flags: ExtFlagMinLength, MinLength: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("a"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("prefilter minimum length was not removed: %v %#v", err, matches)
	}
	ext, ok := s.ruleExtension(1)
	if !ok || ext == nil || ext.Enabled(ExtFlagMinLength) {
		t.Fatalf("prefilter extension snapshot=%#v", ext)
	}
}

func TestSharedCandidatesSupportASCIIInsensitiveRules(t *testing.T) {
	s, err := Compile([]Expression{
		{Id: 1, Pattern: "header-[0-9]+", Flags: CompileCaseless},
		{Id: 2, Pattern: "footer-[A-Z]+", Flags: CompileCaseless},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.Scan([]byte("HEADER-42 header-x FOOTER-OK footer-7"))
	if err != nil || s.literalCandidateBackend() == "" || len(matches) != 2 || matches[0].Id != 1 || matches[1].Id != 2 {
		t.Fatalf("insensitive candidates: backend=%q err=%v matches=%#v", s.literalCandidateBackend(), err, matches)
	}
}

func TestPrefilterBranchCandidateStillRunsFullConfirmation(t *testing.T) {
	with, err := Compile([]Expression{{Id: 1, Pattern: `(?:foo|foobar)baz`, Flags: CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	without, err := Compile([]Expression{{Id: 1, Pattern: `(?:foo|foobar)baz`}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("foobaz fooqux foobarbaz foobazx")
	want, err := without.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := with.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("候选过滤改变命中数量: want=%#v got=%#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("候选过滤改变确认结果: want=%#v got=%#v", want, got)
		}
	}
}

func TestSinglePrefilterUsesAllCandidateStarts(t *testing.T) {
	scanner, err := Compile([]Expression{{Id: 91, Pattern: `foo[0-9]`, Flags: CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanner.Scan([]byte("foo1 xfoo2 foo3"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{{Id: 91, From: 0, To: 4}, {Id: 91, From: 6, To: 10}, {Id: 91, From: 11, To: 15}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("候选起点结果=%v, want=%v", got, want)
	}
}
