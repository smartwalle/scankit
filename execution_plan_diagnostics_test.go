package scankit

import (
	"reflect"
	"testing"
)

// executionPlanDiagnostics 只用于测试和 profile 前的计划审计。它刻意不进入 Scanner
// 或 context，避免候选统计、原子计数等诊断逻辑污染扫描热路径。
type executionPlanDiagnostics struct {
	roleCount             uint32
	edgeCount             uint32
	triggerCount          uint32
	rootByteCount         uint8
	fixedTriggerBytes     uint16
	fixedAnchorBytes      uint16
	prefixRepeatBytes     uint16
	alwaysVerifierCount   uint16
	verifierCount         uint32
	reportCount           uint32
	maxVerifierStates     uint32
	staticPlanBytes       uint32
	needsPendingEvents    bool
	payloadPotentialCount uint64
	programs              []executionProgramDiagnostic
}

// executionProgramDiagnostic 是单条已编译正则的只读审计视图。它将选择结果保留在测试
// 路径，避免为了诊断在 Scanner 热路径中加入计数器或额外分支。
type executionProgramDiagnostic struct {
	expressionIndex uint32
	candidate       string
	executor        string
	fallback        byteRegexCandidateFallback
	anchorLength    uint16
	anchorSpan      int
	anchorScore     uint32
	requiredChecks  uint8
	verifierStates  uint16
	consumerCount   uint16
	directEligible  bool
}

func inspectExecutionPlan(scanner *Scanner, data []byte) executionPlanDiagnostics {
	graph := scanner.roleGraph
	diagnostics := executionPlanDiagnostics{
		roleCount:          graph.budget.roleCount,
		edgeCount:          graph.budget.edgeCount,
		triggerCount:       graph.budget.triggerCount,
		rootByteCount:      scanner.automaton.rootByteCount,
		verifierCount:      graph.budget.verifierCount,
		reportCount:        graph.budget.reportCount,
		maxVerifierStates:  graph.budget.maxVerifierStates,
		staticPlanBytes:    graph.budget.staticPlanBytes,
		needsPendingEvents: graph.budget.needsPendingEvents,
	}
	plan := scanner.blockScanPlan.unanchored
	for value := range plan.fixed {
		diagnostics.fixedTriggerBytes += uint16(len(plan.fixed[value]))
		diagnostics.fixedAnchorBytes += uint16(len(plan.fixedAnchor[value]))
		diagnostics.prefixRepeatBytes += uint16(len(plan.prefixRepeat[value]))
	}
	diagnostics.alwaysVerifierCount = uint16(len(plan.always))
	for _, value := range data {
		if scanner.automaton.rootByteFast && scanner.automaton.rootByteCount != 0 {
			for rootIndex := uint8(0); rootIndex < scanner.automaton.rootByteCount; rootIndex++ {
				if value == scanner.automaton.rootByteVals[rootIndex] {
					diagnostics.payloadPotentialCount++
					break
				}
			}
		}
		diagnostics.payloadPotentialCount += uint64(len(plan.fixed[value]))
		diagnostics.payloadPotentialCount += uint64(len(plan.fixedAnchor[value]))
		diagnostics.payloadPotentialCount += uint64(len(plan.prefixRepeat[value]))
		diagnostics.payloadPotentialCount += uint64(len(plan.always))
	}
	diagnostics.programs = make([]executionProgramDiagnostic, len(scanner.regexPrograms))
	for index, program := range scanner.regexPrograms {
		diagnostics.programs[index] = inspectExecutionProgram(scanner, uint32(index), program)
	}
	return diagnostics
}

func inspectExecutionProgram(scanner *Scanner, programIndex uint32, program compiledRegexProgram) executionProgramDiagnostic {
	diagnostic := executionProgramDiagnostic{
		expressionIndex: program.expressionIndex,
		fallback:        program.candidate.fallback,
		anchorLength:    uint16(len(program.candidate.anchor.literal)),
		anchorSpan:      program.candidate.anchorSpan,
		anchorScore:     program.candidate.anchorScore,
		requiredChecks:  program.candidate.requiredChecks,
		verifierStates:  uint16(len(program.program.states)),
		directEligible:  scanner.directSingleEvent && !expressionRequiresDeferredDelivery(scanner.expressions[program.expressionIndex]),
	}
	switch {
	case program.internalAnchor:
		diagnostic.candidate = "internal-literal"
	case program.candidate.bounded:
		diagnostic.candidate = "bounded-literal"
	case program.candidate.hasAnchor:
		diagnostic.candidate = "unbounded-literal"
	default:
		diagnostic.candidate = "every-byte"
	}
	switch {
	case program.boundedRepeat != nil:
		diagnostic.executor = "bounded-repeat"
	case program.fixed != nil:
		diagnostic.executor = "fixed"
	case program.fixedAnchor != nil && program.alternation != nil:
		diagnostic.executor = "equal-alternation"
	case program.fixedAnchor != nil:
		diagnostic.executor = "fixed-anchor"
	case program.hasPrefixRepeat:
		diagnostic.executor = "prefix-repeat"
	case program.hasSimpleRepeat:
		diagnostic.executor = "simple-repeat"
	default:
		diagnostic.executor = "nfa-dfa-fallback"
	}
	for _, edge := range scanner.roleGraph.edges {
		if edge.kind != executionRoleReports || edge.from >= uint32(len(scanner.roleGraph.roles)) {
			continue
		}
		role := scanner.roleGraph.roles[edge.from]
		if role.contextIndex == programIndex || role.expressionIndex == program.expressionIndex && role.contextIndex == 0 {
			diagnostic.consumerCount++
		}
	}
	return diagnostic
}

func TestExecutionPlanDiagnosticsExplainMixedRules(t *testing.T) {
	t.Parallel()
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `phone=1[3-9][0-9]{9}`},
		{Id: 2, Pattern: `62[0-9]{14,17}`},
		{Id: 3, Pattern: `[z][a-z]{9,}`},
		{Id: 4, Pattern: `alice@example\.com`},
	})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := inspectExecutionPlan(scanner, []byte(`phone=13800138000 card=6222021234567890 token=zredaction alice@example.com`))
	if diagnostics.roleCount == 0 || diagnostics.edgeCount == 0 || diagnostics.verifierCount == 0 {
		t.Fatalf("incomplete diagnostics: %#v", diagnostics)
	}
	if diagnostics.reportCount != 4 {
		t.Fatalf("report count = %d, want 4", diagnostics.reportCount)
	}
	if diagnostics.payloadPotentialCount == 0 {
		t.Fatalf("potential candidates = 0, want non-zero: %#v", diagnostics)
	}
	if diagnostics.staticPlanBytes == 0 || diagnostics.maxVerifierStates == 0 {
		t.Fatalf("missing resource budget: %#v", diagnostics)
	}
	if len(diagnostics.programs) != len(scanner.regexPrograms) {
		t.Fatalf("program diagnostics = %d, want %d", len(diagnostics.programs), len(scanner.regexPrograms))
	}
	for _, program := range diagnostics.programs {
		if program.executor == "" || program.candidate == "" || program.verifierStates == 0 || program.consumerCount == 0 {
			t.Fatalf("incomplete program diagnostic: %#v", program)
		}
	}
}

func TestExecutionPlanDiagnosticsDoesNotEnterScanPath(t *testing.T) {
	t.Parallel()
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `[1-9][0-9]{5}`}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`value=110105`)
	before := inspectExecutionPlan(scanner, data)
	matches, err := scanner.ScanInto(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	after := inspectExecutionPlan(scanner, data)
	if len(matches) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("scan changed diagnostics or results: before=%#v after=%#v matches=%#v", before, after, matches)
	}
}

func TestExecutionPlanDiagnosticsDescribeCandidateAndExecutor(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `.*error`},
		{Id: 2, Pattern: `[A-Z][0-9]{2,3}[A-Z]`},
		{Id: 3, Pattern: `(?:ab|ac|zz)-X`},
		{Id: 4, Pattern: `[a-z]+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := inspectExecutionPlan(scanner, []byte("error A12X ac-X words"))
	if len(diagnostics.programs) != 4 {
		t.Fatalf("program diagnostics = %#v", diagnostics.programs)
	}
	want := map[uint32]struct {
		candidate string
		executor  string
	}{
		0: {candidate: "internal-literal", executor: "nfa-dfa-fallback"},
		1: {candidate: "every-byte", executor: "bounded-repeat"},
		2: {candidate: "bounded-literal", executor: "equal-alternation"},
		3: {candidate: "every-byte", executor: "simple-repeat"},
	}
	for _, program := range diagnostics.programs {
		expected := want[program.expressionIndex]
		if program.candidate != expected.candidate || program.executor != expected.executor {
			t.Fatalf("expression %d diagnostic = %#v, want candidate=%q executor=%q", program.expressionIndex, program, expected.candidate, expected.executor)
		}
	}
}

// TestExecutionPlanDiagnosticsLogMixedDispatch 保留六种不同结构的规则组合，便于在 profile
// 前确认候选通道没有意外退化。该测试不把诊断数据接入 Scanner 热路径。
func TestExecutionPlanDiagnosticsLogMixedDispatch(t *testing.T) {
	scanner, err := Compile([]Expression{
		{Id: 1, Pattern: `(?:\b|^)(?:\+86|86)?1[3-9]\d{9}(?:\b|$)`},
		{Id: 2, Pattern: `[A-Za-z0-9.!#$%&'*+/?^_` + "`" + `{|}~-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+\b`},
		{Id: 3, Pattern: `[1-9][0-9]{5}(18|19|20)[0-9]{2}(0[1-9]|1[0-2])(0[1-9]|[12][0-9]|3[01])[0-9]{3}[0-9Xx]`},
		{Id: 4, Pattern: `62[0-9]{14,17}`},
		{Id: 5, Pattern: `4[0-9]{15}|5[1-5][0-9]{14}|3[47][0-9]{13}`},
		{Id: 6, Pattern: `[z][a-z]{9,}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range inspectExecutionPlan(scanner, nil).programs {
		t.Logf("expression=%d candidate=%s executor=%s checks=%d states=%d", program.expressionIndex+1, program.candidate, program.executor, program.requiredChecks, program.verifierStates)
	}
	for index, program := range scanner.regexPrograms {
		if program.fixed != nil {
			t.Logf("expression=%d fixed sequences=%d duplicate=%t", index+1, len(program.fixed.sequences), program.fixed.mayDuplicateMatches)
			for value, shared := range program.fixed.sharedSuffix {
				if shared != 0 {
					t.Logf("expression=%d fixed shared suffix byte=%q length=%d", index+1, byte(value), shared)
				}
			}
		}
	}
	for value := range scanner.blockScanPlan.unanchored.fixed {
		if lanes := scanner.blockScanPlan.unanchored.fixed[value]; len(lanes) != 0 {
			t.Logf("fixed trigger byte=%q lanes=%d", byte(value), len(lanes))
		}
	}
	for value := range scanner.blockScanPlan.unanchored.fixedAnchor {
		if lanes := scanner.blockScanPlan.unanchored.fixedAnchor[value]; len(lanes) != 0 {
			t.Logf("fixed-anchor trigger byte=%q lanes=%d", byte(value), len(lanes))
		}
	}
	t.Logf("mixed word=%t range=%q-%q remainder=%q prefix=%q bounded=%t", scanner.blockScanPlan.mixed.wordScan, scanner.blockScanPlan.mixed.rangeMinimum, scanner.blockScanPlan.mixed.rangeMaximum, scanner.blockScanPlan.mixed.remainder[:scanner.blockScanPlan.mixed.remainderLen], scanner.blockScanPlan.mixed.prefixByte, scanner.blockScanPlan.unanchored.hasBoundedRepeatLanes())
}
