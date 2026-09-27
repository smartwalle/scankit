package nfa

import (
	"fmt"

	"github.com/smartwalle/scankit/internal/nfagraph"
)

// ConformanceCase 描述一个引擎族一致性样例。
type ConformanceCase struct {
	Name string
	Data []byte
}

// CompareEngineFamilies 对指定图在多个执行模型上的起点、区间和预算结果进行比对。
// 该验证器只使用当前 Go 实现，适合发布前固定语义样例。
func CompareEngineFamilies(g *nfagraph.Graph, kinds []EngineKind, cases []ConformanceCase) error {
	if g == nil || len(kinds) == 0 {
		return fmt.Errorf("empty conformance input")
	}
	seenKinds := make(map[EngineKind]struct{}, len(kinds))
	for _, kind := range kinds {
		if !kind.Valid() {
			return fmt.Errorf("invalid conformance engine kind %d", kind)
		}
		if _, exists := seenKinds[kind]; exists {
			return fmt.Errorf("duplicate conformance engine kind %d", kind)
		}
		seenKinds[kind] = struct{}{}
	}
	baseline, err := Compile(g)
	if err != nil {
		return err
	}
	for caseIndex, item := range cases {
		for _, kind := range kinds {
			engine, err := CompileEngine(g, kind)
			if err != nil {
				return fmt.Errorf("case %d (%s), %s: compile: %w", caseIndex, item.Name, kind, err)
			}
			if err := engine.Validate(); err != nil {
				return fmt.Errorf("case %d (%s), %s: validate: %w", caseIndex, item.Name, kind, err)
			}
			for start := 0; start <= len(item.Data); start++ {
				want := baseline.MatchAt(item.Data, start)
				got := engine.MatchAt(item.Data, start)
				if !sameIntResults(got, want) {
					return fmt.Errorf("case %d (%s), %s: start %d result mismatch", caseIndex, item.Name, kind, start)
				}
				for from := 0; from <= len(item.Data); from++ {
					for to := from; to <= len(item.Data); to++ {
						if !sameIntResults(engine.MatchAtRange(item.Data, start, from, to, 0), baseline.MatchAtRange(item.Data, start, from, to, 0)) {
							return fmt.Errorf("case %d (%s), %s: start %d end range [%d,%d) mismatch", caseIndex, item.Name, kind, start, from, to)
						}
						for _, limit := range []int{1, 2} {
							if !sameIntResults(engine.MatchAtRange(item.Data, start, from, to, limit), baseline.MatchAtRange(item.Data, start, from, to, limit)) {
								return fmt.Errorf("case %d (%s), %s: start %d end range [%d,%d] limit %d mismatch", caseIndex, item.Name, kind, start, from, to, limit)
							}
						}
					}
				}
				wantEnds := baseline.MatchAt(item.Data, start)
				gotEnds := engine.MatchAt(item.Data, start)
				wantFirst, wantOK := 0, len(wantEnds) > 0
				gotFirst, gotOK := 0, len(gotEnds) > 0
				if wantOK {
					wantFirst = wantEnds[0]
				}
				if gotOK {
					gotFirst = gotEnds[0]
				}
				if wantOK != gotOK || wantOK && wantFirst != gotFirst {
					return fmt.Errorf("case %d (%s), %s: start %d first result mismatch", caseIndex, item.Name, kind, start)
				}
				wantLongest, wantOK := 0, len(wantEnds) > 0
				gotLongest, gotOK := 0, len(gotEnds) > 0
				if wantOK {
					wantLongest = wantEnds[len(wantEnds)-1]
				}
				if gotOK {
					gotLongest = gotEnds[len(gotEnds)-1]
				}
				if wantOK != gotOK || wantOK && wantLongest != gotLongest {
					return fmt.Errorf("case %d (%s), %s: start %d longest result mismatch", caseIndex, item.Name, kind, start)
				}
			}
			// 非法起点和区间必须与基线统一返回空结果，不能让专用布局
			// 将越界输入解释为可接受状态。
			for _, start := range []int{-1, len(item.Data) + 1} {
				if got := engine.MatchAt(item.Data, start); got != nil {
					return fmt.Errorf("case %d (%s), %s: invalid start %d accepted", caseIndex, item.Name, kind, start)
				}
			}
			for _, limit := range []int{-1, -2} {
				if got := engine.MatchAtRange(item.Data, 0, 0, len(item.Data), limit); got != nil {
					return fmt.Errorf("case %d (%s), %s: negative range limit %d accepted", caseIndex, item.Name, kind, limit)
				}
			}
			wantSpans := baseline.Spans(item.Data)
			gotSpans := engine.Spans(item.Data)
			if !sameSpanResults(gotSpans, wantSpans) {
				return fmt.Errorf("case %d (%s), %s: span result mismatch", caseIndex, item.Name, kind)
			}
			for _, limit := range []int{1, 2} {
				wantLimited := limitSpans(wantSpans, limit)
				gotLimited := engine.SpansLimit(item.Data, limit)
				if !sameSpanResults(gotLimited, wantLimited) {
					return fmt.Errorf("case %d (%s), %s: span limit %d mismatch", caseIndex, item.Name, kind, limit)
				}
			}
			if got := engine.SpansLimit(item.Data, -1); got != nil {
				return fmt.Errorf("case %d (%s), %s: negative span limit accepted", caseIndex, item.Name, kind)
			}
			for from := 0; from <= len(item.Data); from++ {
				for to := from; to <= len(item.Data); to++ {
					wantRange := baseline.SpansRange(item.Data, from, to)
					gotRange := engine.SpansRange(item.Data, from, to)
					if !sameSpanResults(gotRange, wantRange) {
						return fmt.Errorf("case %d (%s), %s: range [%d,%d) mismatch", caseIndex, item.Name, kind, from, to)
					}
					for _, limit := range []int{1, 2} {
						wantLimited := limitSpans(baseline.SpansRange(item.Data, from, to), limit)
						gotLimited := engine.SpansRangeLimit(item.Data, from, to, limit)
						if !sameSpanResults(gotLimited, wantLimited) {
							return fmt.Errorf("case %d (%s), %s: range [%d,%d] limit %d mismatch", caseIndex, item.Name, kind, from, to, limit)
						}
					}
				}
			}
			// 结果上限属于语义约束，所有布局都必须返回同一结果前缀；
			// 步骤上限只用于资源治理，不要求不同布局具有相同步数。
			for _, limit := range []int{1, 2} {
				for start := 0; start <= len(item.Data); start++ {
					wantBudget, _, _ := baseline.MatchAtBudget(item.Data, start, 0, limit)
					gotBudget, _, _ := engine.MatchAtBudget(item.Data, start, 0, limit)
					if !sameIntResults(gotBudget, wantBudget) {
						return fmt.Errorf("case %d (%s), %s: start %d result limit %d mismatch", caseIndex, item.Name, kind, start, limit)
					}
				}
				wantSpans, _, _ := baseline.SpansBudget(item.Data, 0, limit)
				gotSpans, _, _ := engine.SpansBudget(item.Data, 0, limit)
				if !sameSpanResults(gotSpans, wantSpans) {
					return fmt.Errorf("case %d (%s), %s: span result limit %d mismatch", caseIndex, item.Name, kind, limit)
				}
			}
			if _, _, stopped := engine.MatchAtBudget(item.Data, 0, -1, 0); stopped {
				return fmt.Errorf("case %d (%s), %s: negative step budget accepted", caseIndex, item.Name, kind)
			}
			if _, _, stopped := engine.MatchAtBudget(item.Data, 0, 0, -1); stopped {
				return fmt.Errorf("case %d (%s), %s: negative result budget accepted", caseIndex, item.Name, kind)
			}
		}
	}
	return nil
}

func limitSpans(spans []Span, limit int) []Span {
	if limit > 0 && len(spans) > limit {
		return spans[:limit]
	}
	return spans
}

func sameIntResults(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameSpanResults(a, b []Span) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
