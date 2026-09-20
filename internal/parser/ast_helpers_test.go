package parser

import "testing"

func TestASTSummaryHelpers(t *testing.T) {
	r, err := Parse("(ab){2,4}")
	if err != nil {
		t.Fatal(err)
	}
	if MinLiteralLength(r) != 1 || MaxLiteralLength(r) != 1 || MaxRepeatCount(r) != 4 || CaptureCount(r) != 1 {
		t.Fatalf("摘要辅助不一致: min=%d max=%d repeat=%d captures=%d", MinLiteralLength(r), MaxLiteralLength(r), MaxRepeatCount(r), CaptureCount(r))
	}
	if HasAssertion(r) {
		t.Fatal("普通文字不应包含断言")
	}
}
