package scankit

import "testing"

func TestBuildByteRegexCandidatePlanPrefersBoundedAnchor(t *testing.T) {
	t.Parallel()
	analysis := regexAnalysis{anchors: []regexAnchor{
		{literal: "long-unbounded", minOffset: 0, maxOffset: unboundedRepeat},
		{literal: "id", minOffset: 2, maxOffset: 4},
	}}
	plan := buildByteRegexCandidatePlan(analysis)
	if !plan.bounded || plan.anchor.literal != "id" || plan.anchorSpan != 2 {
		t.Fatalf("candidate plan = %#v", plan)
	}
}

func TestBuildByteRegexCandidatePlanUsesLongestThenNarrowestAnchor(t *testing.T) {
	t.Parallel()
	analysis := regexAnalysis{anchors: []regexAnchor{
		{literal: "tag", minOffset: 0, maxOffset: 12},
		{literal: "tag", minOffset: 0, maxOffset: 2},
		{literal: "long", minOffset: 0, maxOffset: 64},
	}}
	plan := buildByteRegexCandidatePlan(analysis)
	if plan.anchor.literal != "long" || plan.anchorSpan != 64 {
		t.Fatalf("candidate plan = %#v", plan)
	}
}

func TestByteRegexCompilePlanKeepsCandidateMetadata(t *testing.T) {
	t.Parallel()
	root, err := parseRegexWithFlags(`[a-z]{1,8}@example\.com`, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := compileByteRegexPlan(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.candidate.bounded || plan.candidate.anchor.literal == "" || plan.candidate.anchorSpan != 7 {
		t.Fatalf("candidate metadata = %#v", plan.candidate)
	}
}
