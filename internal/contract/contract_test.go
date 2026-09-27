package contract

import "testing"

func TestManifestIsStable(t *testing.T) {
	if len(CompileFlags()) != 11 || len(ExtensionFlags()) != 5 {
		t.Fatal("公开标志清单不完整")
	}
	if OffsetUnit() != "byte" || len(MatchOrdering()) != 1 || MatchOrdering()[0] != "producer_order" {
		t.Fatal("匹配契约快照异常")
	}
}
