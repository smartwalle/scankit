package scankit

import "testing"

// executionPlanObservation 是扫描前的离线候选成本观测。它只在测试中遍历不可变计划，
// 绝不写入 Scanner 或 context，因此不会给生产扫描增加原子计数、分支或分配。
type executionPlanObservation struct {
	rootCandidates              uint64
	fixedCandidates             uint64
	fixedSequenceCandidates     uint64
	fixedSequenceGroups         uint64
	fixedAnchorCandidates       uint64
	fixedAnchorWindowRejected   uint64
	fixedAnchorChecksRejected   uint64
	fixedAnchorVerifierCalls    uint64
	fixedAnchorAlternationCalls uint64
	prefixRepeatCandidates      uint64
	boundedRepeatCandidates     uint64
	alternationCandidates       uint64
	alwaysCandidates            uint64
	finalEvents                 uint64
}

func observeExecutionPlan(scanner *Scanner, data []byte) (executionPlanObservation, error) {
	var observation executionPlanObservation
	plan := scanner.blockScanPlan.unanchored
	for offset, value := range data {
		if scanner.automaton.rootByteFast && scanner.automaton.rootByteCount != 0 {
			for rootIndex := uint8(0); rootIndex < scanner.automaton.rootByteCount; rootIndex++ {
				if value == scanner.automaton.rootByteVals[rootIndex] {
					observation.rootCandidates++
					break
				}
			}
		}
		for _, lane := range plan.fixed[value] {
			observation.fixedCandidates++
			triggerRange := lane.fixed.sequenceTrigger[value]
			observation.fixedSequenceCandidates += uint64(triggerRange.length())
			observation.fixedSequenceGroups += uint64(fixedByteRegexSequenceGroupCount(lane.fixed, value))
		}
		for _, lane := range plan.fixedAnchor[value] {
			anchor := lane.anchor
			start := offset - anchor.offset
			if start < 0 || start+anchor.width > len(data) {
				observation.fixedAnchorWindowRejected++
				continue
			}
			observation.fixedAnchorCandidates++
			if anchor.checks != nil && !fixedByteRegexAnchorChecksMatch(data, start, anchor.checks) {
				observation.fixedAnchorChecksRejected++
				continue
			}
			if lane.alternation != nil {
				observation.fixedAnchorAlternationCalls++
			} else {
				observation.fixedAnchorVerifierCalls++
			}
		}
		observation.prefixRepeatCandidates += uint64(len(plan.prefixRepeat[value]))
		observation.boundedRepeatCandidates += uint64(len(plan.boundedRepeat[value]))
		observation.alternationCandidates += uint64(len(plan.alternation[value]))
		observation.alwaysCandidates += uint64(len(plan.always))
	}
	matches, err := scanner.ScanInto(data, nil)
	if err != nil {
		return executionPlanObservation{}, err
	}
	observation.finalEvents = uint64(len(matches))
	return observation, nil
}

func fixedByteRegexSequenceGroupCount(program *fixedByteRegex, value byte) int {
	triggerRange := program.sequenceTrigger[value]
	if triggerRange.empty() {
		return 0
	}
	var seen [maxFixedByteRegexLength + 1]bool
	count := 0
	for index := triggerRange.start; index < triggerRange.end; index++ {
		sequenceIndex := program.sequenceIndexes[index]
		offset := program.sequences[sequenceIndex].triggerOffset
		if !seen[offset] {
			seen[offset] = true
			count++
		}
	}
	return count
}

func TestExecutionPlanRuntimeObservation(t *testing.T) {
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
	data := []byte(`ts=2026-08-20T09:30:00+08:00 mobile=13800138000 email=alice.zhang@example.com identity_no=11010520000101002X bank_card=6222021234567890 credit_card=4111111111111111 sensitive_token=zredaction`)
	observation, err := observeExecutionPlan(scanner, data)
	if err != nil {
		t.Fatal(err)
	}
	if observation.rootCandidates == 0 || observation.fixedCandidates == 0 || observation.prefixRepeatCandidates == 0 || observation.finalEvents != 6 {
		t.Fatalf("incomplete runtime observation: %#v", observation)
	}
	t.Logf("runtime candidates: %#v", observation)
}

func TestExecutionPlanObservationCountsFixedSequenceGroups(t *testing.T) {
	t.Parallel()
	scanner, err := Compile([]Expression{{Id: 1, Pattern: `a[0-9]{2}|b[0-9]{2}|c[0-9]{3}`}})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := observeExecutionPlan(scanner, []byte("a12 b34 c567"))
	if err != nil {
		t.Fatal(err)
	}
	if observation.fixedCandidates == 0 || observation.fixedSequenceCandidates == 0 || observation.fixedSequenceGroups == 0 {
		t.Fatalf("missing fixed sequence observation: %#v", observation)
	}
}
